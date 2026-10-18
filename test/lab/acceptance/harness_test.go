//go:build lab

// Package acceptance holds the lab's acceptance tests (lab test environment
// spec, Acceptance tests): lab checks, which pass or fail, and gate
// sessions, which collect evidence and fail only when the probe itself
// fails. They drive the lab through lab.sh, as an operator would, and need
// a lab that is up and deployed: run them with "lab.sh test".
package acceptance

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

// lab is the lab under test: its locations and its env.json.
type lab struct {
	dir      string // test/lab
	repo     string
	state    string
	evidence string
	env      labEnv
}

// labEnv is the part of env.json the tests read.
type labEnv struct {
	Servers map[string]struct {
		Version           string `json:"version"`
		APIExtensionsHash string `json:"api_extensions_hash"`
		Fingerprint       string `json:"fingerprint"`
		Endpoint          string `json:"endpoint"`
		Cluster           string `json:"cluster"`
		Members           map[string]struct {
			Endpoint string `json:"endpoint"`
		} `json:"members"`
		Instances map[string]struct {
			Type     string `json:"type"`
			Location string `json:"location"`
		} `json:"instances"`
		Auth struct {
			Bearer     string            `json:"bearer"`
			Identities map[string]string `json:"identities"`
		} `json:"auth"`
	} `json:"servers"`
	Probe struct {
		Version string `json:"version"`
		SHA256  string `json:"sha256"`
	} `json:"probe"`
}

// servers returns the lab's standalone servers in name order; the gate
// sessions run on them. Clusters are separate (clusters).
func (l *lab) servers() []string {
	var out []string
	for name, s := range l.env.Servers {
		if s.Cluster != "yes" {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// clusterSiblings maps each cluster to the standalone server whose LXD
// revision it runs, and to the LAB_CLUSTER values that run it.
var clusterSiblings = map[string]struct {
	sibling  string
	chosenBy []string
}{
	"c5": {"lxd5", []string{"5.21", "both"}},
	"c6": {"lxd6", []string{"6.9", "both"}},
}

// clusters returns the clusters LAB_CLUSTER runs, among those env.json
// records, in name order.
func (l *lab) clusters() []string {
	chosen := setting("LAB_CLUSTER", "no")
	var out []string
	for name, s := range l.env.Servers {
		if s.Cluster == "yes" && slices.Contains(clusterSiblings[name].chosenBy, chosen) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// members returns a cluster's members in name order.
func (l *lab) members(cluster string) []string {
	var out []string
	for m := range l.env.Servers[cluster].Members {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

// identities returns the server's probe identities in name order, bearer
// last where the server has one.
func (l *lab) identities(server string) []string {
	var out []string
	for id := range l.env.Servers[server].Auth.Identities {
		out = append(out, id)
	}
	slices.Sort(out)
	if l.env.Servers[server].Auth.Bearer == "yes" {
		out = append(out, "bearer")
	}
	return out
}

// setting returns a lab setting exported by lab.sh, or def.
func setting(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// newLab finds the lab from the variables lab.sh exports, or from this
// package's location when run with go test directly, and reads env.json.
func newLab(t *testing.T) *lab {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := setting("LAB_DIR", filepath.Dir(wd))
	l := &lab{
		dir:      dir,
		repo:     setting("REPO_DIR", filepath.Join(dir, "..", "..")),
		state:    setting("LAB_STATE_DIR", filepath.Join(dir, ".state")),
		evidence: setting("LAB_EVIDENCE_DIR", filepath.Join(dir, "..", "..", "evidence")),
	}
	data, err := os.ReadFile(filepath.Join(l.state, "env.json"))
	if err != nil {
		t.Fatalf("no lab: %v (lab.sh up, then lab.sh test)", err)
	}
	if err := json.Unmarshal(data, &l.env); err != nil {
		t.Fatalf("env.json: %v", err)
	}
	if l.env.Probe.SHA256 == "" {
		t.Fatal("lxd-probe is not deployed: lab.sh deploy")
	}
	return l
}

// run is one lxd-probe invocation through lab.sh: its first record, and
// all of them for verbs that write several (guest devlxd dump).
type run struct {
	code   int
	stdout string
	stderr string
	rec    *evidence.Record
	recs   []*evidence.Record
}

// String describes the invocation's result for failure messages.
func (r *run) String() string {
	var b strings.Builder
	b.WriteString("exit " + strconv.Itoa(r.code))
	if r.rec != nil {
		b.WriteString(", record " + r.rec.ID + ", outcome " + string(r.rec.Outcome))
		obs, _ := json.MarshalIndent(r.rec.Observations, "  ", "  ")
		b.WriteString("\n  observations: " + string(obs))
	}
	if s := strings.TrimSpace(r.stderr); s != "" {
		b.WriteString("\n  stderr: " + s)
	}
	return b.String()
}

// observation returns the value of the record's first observation key.
func (r *run) observation(key string) (any, bool) {
	if r.rec == nil {
		return nil, false
	}
	for _, o := range r.rec.Observations {
		if o.Key == key {
			return o.Value, true
		}
	}
	return nil, false
}

// probeFailure reports the exit codes that mean the probe itself failed
// (spec: gate sessions fail only on these, or on a missing record).
func probeFailure(code int) bool {
	switch code {
	case exit.Internal, exit.Connection, exit.Refused, exit.Sanitize, exit.Timeout:
		return true
	}
	return false
}

// session is how a test runs the probe: where evidence goes and what tags it.
type session struct {
	evidenceDir string
	runID       string
}

// probe runs "lab.sh probe SERVER --as IDENTITY -- ARGS --json" and fails
// the test when the probe itself fails or writes no record.
func (l *lab) probe(t *testing.T, s session, server, identity string, args ...string) *run {
	t.Helper()
	return l.probeMember(t, s, server, "", identity, args...)
}

// probeMember is probe through a cluster member (none: the first).
func (l *lab) probeMember(t *testing.T, s session, server, member, identity string, args ...string) *run {
	t.Helper()
	full := []string{"probe", server}
	if member != "" {
		full = append(full, "--member", member)
	}
	full = append(full, "--as", identity, "--")
	full = append(full, args...)
	full = append(full, "--json", "--evidence-dir", s.evidenceDir)
	return l.lxdProbe(t, s, full)
}

// lxdProbe runs lab.sh with args (a probe or guest command) and parses the
// record lxd-probe prints with --json.
func (l *lab) lxdProbe(t *testing.T, s session, args []string) *run {
	t.Helper()
	cmd := exec.Command(filepath.Join(l.dir, "lab.sh"), args...)
	cmd.Env = append(os.Environ(), "LXD_PROBE_RUN_ID="+s.runID)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return l.parseRun(t, args, err, stdout.String(), stderr.String())
}

// parseRun turns a finished lab.sh invocation into a run: its exit code and
// the records lxd-probe printed with --json. JSON objects that are not
// records, such as a rehearsal's challenge or answer line (PRS-53), are
// skipped. It fails the test when the probe itself failed.
func (l *lab) parseRun(t *testing.T, args []string, err error, stdout, stderr string) *run {
	t.Helper()
	r := &run{stdout: stdout, stderr: stderr}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	default:
		t.Fatalf("lab.sh %s: %v", strings.Join(args, " "), err)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	for dec.More() {
		var rec evidence.Record
		if dec.Decode(&rec) != nil {
			break
		}
		if rec.ID != "" {
			r.recs = append(r.recs, &rec)
		}
	}
	if len(r.recs) > 0 {
		r.rec = r.recs[0]
	}
	if probeFailure(r.code) || r.rec == nil {
		t.Fatalf("lab.sh %s: the probe failed: %s", strings.Join(args, " "), r)
	}
	return r
}

// guest runs "lab.sh guest SERVER INSTANCE --project P -- ARGS --json" as
// root; the guest writes its evidence in the instance (lab.sh pull).
func (l *lab) guest(t *testing.T, s session, server, project, instance string, args ...string) *run {
	t.Helper()
	full := append([]string{"guest", server, instance, "--project", project, "--"}, args...)
	return l.lxdProbe(t, s, append(full, "--json"))
}

// pull copies a guest run into the evidence directory (lab.sh pull).
func (l *lab) pull(t *testing.T, server, project, instance, runID string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(l.dir, "lab.sh"), "pull", server, instance, "--project", project, runID)
	cmd.Env = append(os.Environ(), "LAB_EVIDENCE_DIR="+l.evidence)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lab.sh pull %s %s/%s %s: %v\n%s", server, project, instance, runID, err, out)
	}
}

// util runs "lxd-probe util ARGS --json" on this host in the session's
// run, as the util environment, and fails the test like probe does.
func (l *lab) util(t *testing.T, s session, args ...string) *run {
	t.Helper()
	full := append([]string{"util"}, args...)
	full = append(full, "--evidence-dir", s.evidenceDir, "--run-id", s.runID, "--json")
	cmd := exec.Command(filepath.Join(l.state, "bin", "lxd-probe"), full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := &run{stdout: stdout.String(), stderr: stderr.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	default:
		t.Fatalf("lxd-probe %s: %v", strings.Join(full, " "), err)
	}
	var rec evidence.Record
	if json.Unmarshal(stdout.Bytes(), &rec) == nil && rec.ID != "" {
		r.rec, r.recs = &rec, []*evidence.Record{&rec}
	}
	if probeFailure(r.code) || r.rec == nil {
		t.Fatalf("lxd-probe %s: the probe failed: %s", strings.Join(full, " "), r)
	}
	return r
}

// labsh runs another lab.sh command and returns its standard output.
func (l *lab) labsh(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(l.dir, "lab.sh"), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("lab.sh %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String()
}

// trySSH runs a shell command on a lab VM and returns its standard output,
// or an error with its standard error, for steps that may fail.
func (l *lab) trySSH(vm, command string) (string, error) {
	cmd := exec.Command(filepath.Join(l.dir, "lab.sh"), "ssh", vm, command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// ssh runs a shell command on a lab VM and returns its standard output.
func (l *lab) ssh(t *testing.T, vm, command string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(l.dir, "lab.sh"), "ssh", vm, command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("lab.sh ssh %s %q: %v\n%s", vm, command, err, stderr.String())
	}
	return stdout.String()
}

// untrustedCert writes a self-signed client certificate that no lab server
// trusts, and returns its certificate and key files.
func untrustedCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	return clientCert(t, "lxd-lab-untrusted")
}

// clientCert writes a new self-signed client certificate with common name
// cn, and returns its certificate and key files.
func clientCert(t *testing.T, cn string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, cn+".crt"), filepath.Join(dir, cn+".key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
