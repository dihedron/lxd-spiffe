package lxdiid

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/spiffe/spire-plugin-sdk/plugintest"
	agentv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/agent/nodeattestor/v1"
	serverv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/server/nodeattestor/v1"
	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
	server "github.com/dihedron/lxd-spiffe/internal/plugin/server/lxdiid"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

// lab is both plugins in process: the server plugin against a fake LXD
// whose instances' proof directory is the agents' real one, read through
// the files API (fakelxd Mount), and a relay in place of the SPIRE Agent
// and Server cores.
type lab struct {
	t        *testing.T
	fake     *fakelxd.Server
	proofDir string
	server   *serverv1.NodeAttestorPluginClient
	log      *syncBuffer
}

// newLab configures the server plugin with the top-level lines top and one
// server, "lab", for project default.
func newLab(t *testing.T, top string) *lab {
	t.Helper()
	l := &lab{t: t, fake: fakelxd.NewAssumed(t), proofDir: filepath.Join(t.TempDir(), "spire", "lxd"), server: new(serverv1.NodeAttestorPluginClient), log: &syncBuffer{}}
	l.fake.SetExtensions(lxd.RequiredExtensions...)
	cert, key := fakelxd.ClientCredentials(t)
	hcl := fmt.Sprintf(`proof_dir = %q
%s
servers = {
  "lab" = {
    url = %q
    client_cert_file = %q
    client_key_file = %q
    server_cert_fingerprint = %q
    projects = ["default"]
  }
}`, l.proofDir, top, l.fake.URL, cert, key, l.fake.Fingerprint())
	p := server.New()
	if _, err := p.Configure(context.Background(), &configv1.ConfigureRequest{HclConfiguration: hcl, CoreConfiguration: &configv1.CoreConfiguration{TrustDomain: "example.org"}}); err != nil {
		t.Fatal(err)
	}
	plugintest.ServeInBackground(t, plugintest.Config{
		PluginServer: serverv1.NodeAttestorPluginServer(p),
		PluginClient: l.server,
		Logger:       l.logger(),
	})
	return l
}

// logger is the one log of both plugins: each sets log/slog's default.
func (l *lab) logger() hclog.Logger {
	return hclog.New(&hclog.LoggerOptions{Output: l.log, Level: hclog.Debug})
}

// cloudInitID is the cloud-init instance id of fakelxd's devLXD.
const cloudInitID = "7c64d5b4-63d7-4bd6-9a5e-0d3b5a1c2f10"

// instance adds a running instance whose proof directory is the agents'
// one when inside is true, and an empty directory otherwise. Owners are
// shifted so that the test's uid reads as 0, as root's in a guest.
func (l *lab) instance(name, typ, uuid string, inside bool) {
	l.fake.AddInstance("default", name, map[string]string{"volatile.uuid": uuid, "volatile.uuid.generation": uuid, "volatile.cloud-init.instance-id": cloudInitID})
	l.fake.SetType("default", name, typ)
	host := l.proofDir
	if !inside {
		host = l.t.TempDir()
	}
	l.fake.Mount("default", name, l.proofDir, host, os.Geteuid())
}

// agent starts an agent plugin with the lines hcl and the lab's proof_dir
// unless hcl sets one.
func (l *lab) agent(hcl string) *agentv1.NodeAttestorPluginClient {
	l.t.Helper()
	if !strings.Contains(hcl, "proof_dir") {
		hcl = fmt.Sprintf("proof_dir = %q\n%s", l.proofDir, hcl)
	}
	p, err := configure(hcl)
	if err != nil {
		l.t.Fatal(err)
	}
	client := new(agentv1.NodeAttestorPluginClient)
	plugintest.ServeInBackground(l.t, plugintest.Config{
		PluginServer: agentv1.NodeAttestorPluginServer(p),
		PluginClient: client,
		Logger:       l.logger(),
	})
	return client
}

// attest relays one attestation as the SPIRE cores do, and closes the
// agent's stream once the server has answered. The error is the agent's
// when it failed first, the server's otherwise.
func (l *lab) attest(agent *agentv1.NodeAttestorPluginClient) (*serverv1.AgentAttributes, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	actx, closeAgent := context.WithCancel(ctx)
	defer closeAgent()
	as, err := agent.AidAttestation(actx)
	if err != nil {
		return nil, err
	}
	ss, err := l.server.Attest(ctx)
	if err != nil {
		return nil, err
	}
	m, err := as.Recv()
	if err != nil {
		return nil, err
	}
	if err := ss.Send(&serverv1.AttestRequest{Request: &serverv1.AttestRequest_Payload{Payload: m.GetPayload()}}); err != nil {
		return nil, err
	}
	for {
		resp, err := ss.Recv()
		if err != nil {
			return nil, err
		}
		if attrs := resp.GetAgentAttributes(); attrs != nil {
			return attrs, nil
		}
		if err := as.Send(&agentv1.Challenge{Challenge: resp.GetChallenge()}); err != nil {
			return nil, err
		}
		m, err := as.Recv()
		if err != nil {
			return nil, err
		}
		if err := ss.Send(&serverv1.AttestRequest{Request: &serverv1.AttestRequest_ChallengeResponse{ChallengeResponse: m.GetChallengeResponse()}}); err != nil {
			return nil, err
		}
	}
}

// checkEmpty waits for the proof directory to hold no proof.
func (l *lab) checkEmpty() {
	l.t.Helper()
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		entries, _ := os.ReadDir(l.proofDir)
		if len(entries) == 0 {
			return
		}
		if time.Now().After(deadline) {
			l.t.Errorf("proofs left in %s: %v", l.proofDir, entries)
			return
		}
	}
}

func uuidOf(i int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
}

// TestInProcessFilePull: a container and a VM, each claimed through
// devLXD, get their SPIFFE ID and selectors; no proof stays behind.
func TestInProcessFilePull(t *testing.T) {
	for _, typ := range []string{"container", "virtual-machine"} {
		t.Run(typ, func(t *testing.T) {
			l := newLab(t, "")
			l.instance("c1", typ, uuidOf(1), true)
			dev := fakelxd.NewDevLXD(t)
			dev.InstanceType = typ
			attrs, err := l.attest(l.agent(fmt.Sprintf("devlxd_socket = %q", dev.Socket)))
			if err != nil {
				t.Fatalf("attestation failed: %v\n%s", err, l.log)
			}
			if want := "spiffe://example.org/spire/agent/lxd_iid/lab/" + uuidOf(1); attrs.GetSpiffeId() != want {
				t.Errorf("SPIFFE ID %q, want %q", attrs.GetSpiffeId(), want)
			}
			for _, sel := range []string{"server:lab", "uuid:" + uuidOf(1), "project:default", "type:" + typ} {
				if !slices.Contains(attrs.GetSelectorValues(), sel) {
					t.Errorf("selector %q missing from %v", sel, attrs.GetSelectorValues())
				}
			}
			l.checkEmpty()
		})
	}
}

// TestInProcessNotColocated: an agent in c1 that claims c2 writes its proof
// where c2's files API cannot see it, and is refused at once (a container's
// missing file is not retried).
func TestInProcessNotColocated(t *testing.T) {
	l := newLab(t, "")
	l.instance("c1", "container", uuidOf(1), true)
	l.instance("c2", "container", uuidOf(2), false)
	_, err := l.attest(l.agent(`use_devlxd = "off"
claim { instance_name = "c2" }`))
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("attestation = %v; want PermissionDenied\n%s", err, l.log)
	}
	if !strings.Contains(l.log.String(), "reason_code=proof_mismatch") {
		t.Errorf("no proof_mismatch reason:\n%s", l.log)
	}
	l.checkEmpty()
}

// TestInProcessProofDirMismatch: an agent whose proof_dir is not the
// server's refuses the challenge and says why.
func TestInProcessProofDirMismatch(t *testing.T) {
	l := newLab(t, "")
	l.instance("c1", "container", uuidOf(1), true)
	_, err := l.attest(l.agent(fmt.Sprintf("use_devlxd = \"off\"\nclaim { instance_name = \"c1\" }\nproof_dir = %q", filepath.Join(t.TempDir(), "other"))))
	if status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "check that proof_dir is the same on the server") {
		t.Fatalf("attestation = %v; want the agent's proof_dir error", err)
	}
}

// TestInProcessVMProofTimeout checks Operational guidance item 16: while a
// VM's lxd-agent starts, the server retries the files API; an agent whose
// proof_timeout is shorter than that wait removes the proof too early.
func TestInProcessVMProofTimeout(t *testing.T) {
	for proofTimeout, ok := range map[string]bool{"1s": false, "15s": true} {
		t.Run(proofTimeout, func(t *testing.T) {
			l := newLab(t, `proof_timeout = "3s"`)
			l.instance("v1", "virtual-machine", uuidOf(1), true)
			// retries at about 0.25, 0.75 and 1.75 s: the read succeeds at 1.75 s
			l.fake.FailFiles("default", "v1", 3, http.StatusInternalServerError, "LXD VM agent is not currently running")
			_, err := l.attest(l.agent(fmt.Sprintf("use_devlxd = \"off\"\nclaim { instance_name = \"v1\" }\nproof_timeout = %q", proofTimeout)))
			if ok && err != nil {
				t.Fatalf("attestation failed: %v\n%s", err, l.log)
			}
			if !ok && (status.Code(err) != codes.PermissionDenied || !strings.Contains(l.log.String(), "reason_code=proof_timeout")) {
				t.Fatalf("attestation = %v; want PermissionDenied for proof_timeout\n%s", err, l.log)
			}
			l.checkEmpty()
		})
	}
}

// TestInProcessConcurrent: eight agents, configured claims, attest at once
// through one server plugin; each gets its own instance's ID.
func TestInProcessConcurrent(t *testing.T) {
	const n = 8
	l := newLab(t, "")
	agents := make([]*agentv1.NodeAttestorPluginClient, n)
	for i := range n {
		l.instance(fmt.Sprintf("c%d", i), "container", uuidOf(i), true)
		agents[i] = l.agent(fmt.Sprintf("use_devlxd = \"off\"\nclaim { instance_name = \"c%d\" }", i))
	}
	var wg sync.WaitGroup
	ids := make([]string, n)
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			attrs, err := l.attest(agents[i])
			ids[i], errs[i] = attrs.GetSpiffeId(), err
		})
	}
	wg.Wait()
	for i := range n {
		if want := "spiffe://example.org/spire/agent/lxd_iid/lab/" + uuidOf(i); errs[i] != nil || ids[i] != want {
			t.Errorf("agent %d: %q, %v; want %q", i, ids[i], errs[i], want)
		}
	}
	l.checkEmpty()
}

// pushAgent starts an agent whose devLXD shows the fake LXD's config of
// instance name, as LXD shows the server's writes to the guest.
func (l *lab) pushAgent(name, typ string) *agentv1.NodeAttestorPluginClient {
	dev := fakelxd.NewDevLXD(l.t)
	dev.InstanceType = typ
	dev.MetaData = "instance-id: " + cloudInitID + "\nlocal-hostname: " + name + "\n"
	dev.ConfigFrom = func() map[string]string {
		config, _ := l.fake.Config("default", name)
		return config
	}
	return l.agent(fmt.Sprintf("devlxd_socket = %q", dev.Socket))
}

// checkNoKeys: no challenge key is left on the instance.
func (l *lab) checkNoKeys(name string) {
	l.t.Helper()
	config, _ := l.fake.Config("default", name)
	for k := range config {
		if strings.HasPrefix(k, "user.spire.challenge.") {
			l.t.Errorf("challenge key %s left on %s", k, name)
		}
	}
}

// TestInProcessConfigPush: a container and a VM prove co-location through
// devLXD; a stale key left by an earlier attestation goes too, and the
// proof directory is not used.
func TestInProcessConfigPush(t *testing.T) {
	for _, typ := range []string{"container", "virtual-machine"} {
		t.Run(typ, func(t *testing.T) {
			l := newLab(t, `proof_mode = "config_push"`)
			l.instance("c1", typ, uuidOf(1), true)
			l.fake.SetInstanceConfig("default", "c1", "user.spire.challenge.0123456789abcdef", strings.Repeat("a", 64))
			attrs, err := l.attest(l.pushAgent("c1", typ))
			if err != nil {
				t.Fatalf("attestation failed: %v\n%s", err, l.log)
			}
			if want := "spiffe://example.org/spire/agent/lxd_iid/lab/" + uuidOf(1); attrs.GetSpiffeId() != want {
				t.Errorf("SPIFFE ID %q, want %q", attrs.GetSpiffeId(), want)
			}
			if !strings.Contains(l.log.String(), "config_push_stale_keys_removed_total=1") {
				t.Errorf("stale key not counted:\n%s", l.log)
			}
			l.checkNoKeys("c1")
			if _, err := os.Stat(l.proofDir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("proof directory used: %v", err)
			}
		})
	}
}

// TestInProcessAuto: with auto, an agent that offers both modes proves
// with file_pull.
func TestInProcessAuto(t *testing.T) {
	l := newLab(t, `proof_mode = "auto"`)
	l.instance("c1", "container", uuidOf(1), true)
	if _, err := l.attest(l.pushAgent("c1", "container")); err != nil {
		t.Fatalf("attestation failed: %v\n%s", err, l.log)
	}
	if !strings.Contains(l.log.String(), "proof_mode=file_pull") {
		t.Errorf("not file_pull:\n%s", l.log)
	}
	l.checkNoKeys("c1")
}

// TestInProcessConfigPushSameInstance: attestations running at once on one
// instance keep each other's keys and all succeed, through the 412s of
// each other's writes.
func TestInProcessConfigPushSameInstance(t *testing.T) {
	const n = 4
	l := newLab(t, `proof_mode = "config_push"`)
	l.instance("c1", "container", uuidOf(1), true)
	agents := make([]*agentv1.NodeAttestorPluginClient, n)
	for i := range agents {
		agents[i] = l.pushAgent("c1", "container")
	}
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { _, errs[i] = l.attest(agents[i]) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("agent %d: %v", i, err)
		}
	}
	if t.Failed() {
		t.Log(l.log)
	}
	l.checkNoKeys("c1")
}
