package server

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

var update = flag.Bool("update", false, "update golden files")

// options returns the options of a server verb against fake, with the
// defaults go-flags would set.
func options(t *testing.T, fake *fakelxd.Server) probe.ServerCommand {
	t.Helper()
	return probe.ServerCommand{
		GlobalOptions: probe.GlobalOptions{EvidenceDir: t.TempDir(), RunID: "run1", Timeout: 10 * time.Second},
		Endpoint:      fake.URL,
		Pin:           fake.Fingerprint(),
		Auth:          "tls",
		Project:       "default",
	}
}

// record reads the record numbered n of the run.
func record(t *testing.T, opts probe.ServerCommand, n int) evidence.Record {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(opts.EvidenceDir, "run1", "server", "records", fmt.Sprintf("%04d-*.json", n)))
	if err != nil || len(files) != 1 {
		t.Fatalf("record %d: %v, %v", n, files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var rec evidence.Record
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// observation returns the value of the observation key, decoded from the
// record.
func observation(t *testing.T, rec evidence.Record, key string) any {
	t.Helper()
	for _, o := range rec.Observations {
		if o.Key == key {
			return o.Value
		}
	}
	t.Errorf("record %s has no observation %q; has %v", rec.ID, key, keys(rec))
	return nil
}

func keys(rec evidence.Record) []string {
	var out []string
	for _, o := range rec.Observations {
		out = append(out, o.Key)
	}
	return out
}

func hasKeyPrefix(rec evidence.Record, prefix string) bool {
	return slices.ContainsFunc(keys(rec), func(k string) bool { return strings.HasPrefix(k, prefix) })
}

// golden compares rec with testdata/<name>.golden.json, after removing what
// changes between runs: time, duration, tool version and cipher.
func golden(t *testing.T, name string, rec evidence.Record) {
	t.Helper()
	rec.Time = time.Time{}
	rec.DurationMS = 0
	rec.ToolVersion = ""
	for i, o := range rec.Observations {
		if o.Key == "tls.cipher" {
			rec.Observations[i].Value = "*"
		}
	}
	got, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// the fake's fingerprint changes with every run
	got = bytes.ReplaceAll(got, []byte(fingerprint), []byte("<client-fingerprint>"))
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
		t.Errorf("record differs from %s:\n%s", path, got)
	}
}

// clientCert writes a self-signed client certificate and key and returns
// their paths and the certificate's SHA-256 fingerprint.
func clientCert(t *testing.T) (certFile, keyFile, fingerprint string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "probe"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
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
	certFile, keyFile = filepath.Join(dir, "probe.crt"), filepath.Join(dir, "probe.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return certFile, keyFile, hex.EncodeToString(sum[:])
}

// fingerprint is set by tests whose golden record holds a client
// certificate fingerprint.
var fingerprint = "\x00"

func TestInfo(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.SetExtensions("projects", "access_management")
	var out bytes.Buffer
	cmd := &Info{ServerCommand: options(t, fake), stdout: &out}
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := record(t, cmd.ServerCommand, 1)
	if rec.Outcome != evidence.OutcomeOK || rec.Verb != "info" {
		t.Errorf("outcome %q verb %q; observations %v", rec.Outcome, rec.Verb, rec.Observations)
	}
	if rec.Server == nil || rec.Server.Version != "6.5" || rec.Server.APIExtensionsHash != evidence.HashExtensions([]string{"access_management", "projects"}) {
		t.Errorf("server = %+v", rec.Server)
	}
	if got := observation(t, rec, "extensions.required.missing"); !reflect.DeepEqual(got, []any{"instance_generation_id"}) {
		t.Errorf("missing extensions = %v", got)
	}
	if got := observation(t, rec, "tls.version"); got != "TLS 1.3" {
		t.Errorf("tls.version = %v", got)
	}
	if !strings.HasPrefix(out.String(), "server/0001 info: ok (") || !strings.Contains(out.String(), "  server.version = \"6.5\"\n") {
		t.Errorf("output:\n%s", out.String())
	}
	golden(t, "info", rec)
}

func TestInfoProbeTLS12(t *testing.T) {
	fake := fakelxd.NewAssumed(t) // accepts TLS 1.2, unlike LXD
	cmd := &Info{ServerCommand: options(t, fake), ProbeTLS12: true, stdout: &bytes.Buffer{}}
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := record(t, cmd.ServerCommand, 1)
	if rec.Outcome != evidence.OutcomeUnexpected || observation(t, rec, "tls12.accepted") != true || observation(t, rec, "tls12.version") != "TLS 1.2" || observation(t, rec, "tls12.pin_match") != true {
		t.Errorf("outcome %q, observations %v", rec.Outcome, rec.Observations)
	}
	if !slices.Equal(rec.Args, []string{"--probe-tls12"}) {
		t.Errorf("args = %v", rec.Args)
	}

	cmd = &Info{ServerCommand: options(t, fake), ProbeTLS12: true, stdout: &bytes.Buffer{}}
	cmd.Strict = true
	if err := cmd.Execute(nil); exit.Code(err) != exit.Strict {
		t.Errorf("--strict: exit %d (%v); want %d", exit.Code(err), err, exit.Strict)
	}

	cmd = &Info{ServerCommand: options(t, fake), ProbeTLS12: true}
	cmd.Endpoint = "unix:///var/snap/lxd/common/lxd/unix.socket"
	if err := cmd.Execute(nil); exit.Code(err) != exit.Usage {
		t.Errorf("unix endpoint: exit %d (%v); want %d", exit.Code(err), err, exit.Usage)
	}
}

func TestWhoami(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	opts := options(t, fake)
	cert, key, fp := clientCert(t)
	opts.ClientCert, opts.ClientKey = cert, key
	fingerprint = fp
	defer func() { fingerprint = "\x00" }()
	cmd := &Whoami{ServerCommand: opts, stdout: &bytes.Buffer{}}
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := record(t, opts, 1)
	if rec.Outcome != evidence.OutcomeOK || observation(t, rec, "identity.id") != fp || !reflect.DeepEqual(observation(t, rec, "identity.groups"), []any{"spire"}) {
		t.Errorf("outcome %q, observations %v", rec.Outcome, rec.Observations)
	}
	golden(t, "whoami", rec)
}

func TestWhoamiWithoutExtension(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.SetExtensions("projects", "instance_generation_id")
	cmd := &Whoami{ServerCommand: options(t, fake), stdout: &bytes.Buffer{}}
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := record(t, cmd.ServerCommand, 1)
	if rec.Outcome != evidence.OutcomeOK || observation(t, rec, "identity.skipped") == nil {
		t.Errorf("outcome %q, observations %v", rec.Outcome, rec.Observations)
	}
	for _, r := range fake.Requests() {
		if strings.HasPrefix(r.Path, "/1.0/auth") {
			t.Errorf("called %s without the extension", r.Path)
		}
	}
}

func TestInstanceShow(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.AddInstance("default", "c1", map[string]string{"volatile.uuid": "8d4e1c2a", "volatile.uuid.generation": "8d4e1c2b", "user.secret": "hunter2", "user.lxd-probe.t1": "v"})
	opts := options(t, fake)
	cmd := &InstanceShow{ServerCommand: opts, stdout: &bytes.Buffer{}}
	cmd.Args.Name = "c1"
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := record(t, opts, 1)
	if rec.Outcome != evidence.OutcomeOK || !slices.Equal(rec.Expectations, []string{"instance", "instance-full"}) {
		t.Errorf("outcome %q, expectations %v, observations %v", rec.Outcome, rec.Expectations, rec.Observations)
	}
	if observation(t, rec, "instance.etag") == "" || observation(t, rec, "etag.equal") != true {
		t.Errorf("etag observations: %v", rec.Observations)
	}
	config, _ := observation(t, rec, "instance.config").(map[string]any)
	if config["volatile.uuid"] != "8d4e1c2a" || config["user.secret"] != evidence.Redacted || config["user.lxd-probe.t1"] != "v" {
		t.Errorf("config = %v", config)
	}
	want := []string{"artifacts/0001-server.json", "artifacts/0001-instance.json", "artifacts/0001-instance-full.json"}
	if !slices.Equal(rec.Raw, want) {
		t.Errorf("raw = %v; want %v", rec.Raw, want)
	}
	// the recursion=1 artifact holds the state, the plain one does not
	full, err := os.ReadFile(filepath.Join(opts.EvidenceDir, "run1", "server", "artifacts", "0001-instance-full.json"))
	if err != nil || !strings.Contains(string(full), `"state"`) || strings.Contains(string(full), "hunter2") {
		t.Errorf("instance-full artifact: %v\n%s", err, full)
	}
	reqs := fake.Requests()
	if len(reqs) != 3 || reqs[1].Query.Has("recursion") || reqs[2].Query.Get("recursion") != "1" || reqs[1].Query.Get("project") != "default" {
		t.Errorf("requests: %+v", reqs)
	}
	golden(t, "instance-show", rec)
}

func TestInstanceShowNotFound(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	cmd := &InstanceShow{ServerCommand: options(t, fake), stdout: &bytes.Buffer{}}
	cmd.Args.Name = "missing"
	if err := cmd.Execute(nil); err != nil {
		t.Fatalf("a 404 is a finding, not an error: %v", err)
	}
	rec := record(t, cmd.ServerCommand, 1)
	if rec.Outcome != evidence.OutcomeUnexpected || !hasKeyPrefix(rec, "expectation.status") || observation(t, rec, "instance.error") != "Instance not found" {
		t.Errorf("outcome %q, observations %v", rec.Outcome, rec.Observations)
	}

	cmd.Strict = true
	if err := cmd.Execute(nil); exit.Code(err) != exit.Strict {
		t.Errorf("--strict: exit %d (%v)", exit.Code(err), err)
	}
}

func TestDenied(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.AddInstance("default", "c1", nil)
	fake.Deny("/1.0/instances")

	cmd := &InstanceShow{ServerCommand: options(t, fake), stdout: &bytes.Buffer{}}
	cmd.Args.Name = "c1"
	if err := cmd.Execute(nil); exit.Code(err) != exit.Auth {
		t.Fatalf("exit %d (%v); want %d", exit.Code(err), err, exit.Auth)
	}
	rec := record(t, cmd.ServerCommand, 1)
	if rec.Outcome != evidence.OutcomeUnexpected {
		t.Errorf("unexpected 403: outcome %q", rec.Outcome)
	}

	cmd.ExpectDenied = true
	if err := cmd.Execute(nil); err != nil {
		t.Fatalf("--expect-denied: %v", err)
	}
	rec = record(t, cmd.ServerCommand, 2)
	if rec.Outcome != evidence.OutcomeOK || !slices.Equal(rec.Expectations, []string{"instance+denied", "instance-full+denied"}) || !slices.Contains(rec.Args, "--expect-denied") {
		t.Errorf("--expect-denied: outcome %q, expectations %v, args %v, observations %v", rec.Outcome, rec.Expectations, rec.Args, rec.Observations)
	}

	// a success under --expect-denied is a finding
	open := fakelxd.NewAssumed(t)
	open.AddInstance("default", "c1", nil)
	cmd = &InstanceShow{ServerCommand: options(t, open), stdout: &bytes.Buffer{}}
	cmd.Args.Name = "c1"
	cmd.ExpectDenied = true
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	if rec := record(t, cmd.ServerCommand, 1); rec.Outcome != evidence.OutcomeUnexpected {
		t.Errorf("200 under --expect-denied: outcome %q", rec.Outcome)
	}
}

func TestHidden(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.AddInstance("restricted", "c1", nil)
	fake.HideProject("restricted")
	show := func(fake *fakelxd.Server, flags func(*probe.ServerCommand)) (*InstanceShow, error) {
		cmd := &InstanceShow{ServerCommand: options(t, fake), stdout: &bytes.Buffer{}}
		cmd.Project = "restricted"
		cmd.Args.Name = "c1"
		flags(&cmd.ServerCommand)
		return cmd, cmd.Execute(nil)
	}

	// a 404 without the flag is a finding, as for a missing instance
	cmd, err := show(fake, func(*probe.ServerCommand) {})
	if err != nil {
		t.Fatalf("a 404 is a finding, not an error: %v", err)
	}
	if rec := record(t, cmd.ServerCommand, 1); rec.Outcome != evidence.OutcomeUnexpected {
		t.Errorf("unexpected 404: outcome %q", rec.Outcome)
	}

	cmd, err = show(fake, func(o *probe.ServerCommand) { o.ExpectHidden = true })
	if err != nil {
		t.Fatalf("--expect-hidden: %v", err)
	}
	rec := record(t, cmd.ServerCommand, 1)
	if rec.Outcome != evidence.OutcomeOK || !slices.Equal(rec.Expectations, []string{"instance+hidden", "instance-full+hidden"}) || !slices.Contains(rec.Args, "--expect-hidden") {
		t.Errorf("--expect-hidden: outcome %q, expectations %v, args %v, observations %v", rec.Outcome, rec.Expectations, rec.Args, rec.Observations)
	}

	// a success under --expect-hidden is a finding
	open := fakelxd.NewAssumed(t)
	open.AddInstance("restricted", "c1", nil)
	cmd, err = show(open, func(o *probe.ServerCommand) { o.ExpectHidden = true })
	if err != nil {
		t.Fatal(err)
	}
	if rec := record(t, cmd.ServerCommand, 1); rec.Outcome != evidence.OutcomeUnexpected {
		t.Errorf("200 under --expect-hidden: outcome %q", rec.Outcome)
	}

	// so is a 403, which stays an authorization failure (exit 4)
	denied := fakelxd.NewAssumed(t)
	denied.AddInstance("restricted", "c1", nil)
	denied.Deny("/1.0/instances")
	cmd, err = show(denied, func(o *probe.ServerCommand) { o.ExpectHidden = true })
	if exit.Code(err) != exit.Auth {
		t.Fatalf("403 under --expect-hidden: exit %d (%v); want %d", exit.Code(err), err, exit.Auth)
	}
	if rec := record(t, cmd.ServerCommand, 1); rec.Outcome != evidence.OutcomeUnexpected || !hasKeyPrefix(rec, "expectation.status") {
		t.Errorf("403 under --expect-hidden: outcome %q, observations %v", rec.Outcome, rec.Observations)
	}

	// the two expectations exclude each other
	if _, err := show(fake, func(o *probe.ServerCommand) { o.ExpectHidden, o.ExpectDenied = true, true }); exit.Code(err) != exit.Usage {
		t.Errorf("--expect-hidden with --expect-denied: exit %d (%v); want %d", exit.Code(err), err, exit.Usage)
	}
}

func TestPinMismatch(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	cmd := &Info{ServerCommand: options(t, fake), stdout: &bytes.Buffer{}}
	cmd.Pin = strings.Repeat("ab", 32)
	if err := cmd.Execute(nil); exit.Code(err) != exit.Connection {
		t.Fatalf("exit %d (%v); want %d", exit.Code(err), err, exit.Connection)
	}
	rec := record(t, cmd.ServerCommand, 1)
	if rec.Outcome != evidence.OutcomeError || observation(t, rec, "error") == nil {
		t.Errorf("outcome %q, observations %v", rec.Outcome, rec.Observations)
	}
	if len(fake.Requests()) != 0 {
		t.Errorf("a request reached a server with the wrong certificate")
	}
}

func TestUsage(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	noEndpoint := options(t, fake)
	noEndpoint.Endpoint = ""
	noPin := options(t, fake)
	noPin.Pin = ""
	bearer := options(t, fake)
	bearer.Auth = "bearer"
	t.Setenv("LXD_PROBE_TOKEN", "")
	for name, opts := range map[string]probe.ServerCommand{"no endpoint": noEndpoint, "no pin": noPin, "bearer without token": bearer} {
		cmd := &Whoami{ServerCommand: opts, stdout: &bytes.Buffer{}}
		if err := cmd.Execute(nil); exit.Code(err) != exit.Usage {
			t.Errorf("%s: exit %d (%v); want %d", name, exit.Code(err), err, exit.Usage)
		}
	}
}

// TestBearer checks that the token from --token-file is sent and appears
// nowhere in the evidence or the fixtures.
func TestBearer(t *testing.T) {
	const token = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJwcm9iZSJ9.c2lnbmF0dXJl"
	fake := fakelxd.NewAssumed(t)
	opts := options(t, fake)
	opts.Auth = "bearer"
	opts.TokenFile = filepath.Join(t.TempDir(), "token")
	opts.Record = filepath.Join(t.TempDir(), "fixtures")
	if err := os.WriteFile(opts.TokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &Whoami{ServerCommand: opts, stdout: &bytes.Buffer{}}
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	if got := fake.Requests()[0].Header.Get("Authorization"); got != "Bearer "+token {
		t.Errorf("Authorization = %q", got)
	}
	assertAbsent(t, token, opts.EvidenceDir, opts.Record)
}

// TestRecordAndReplay records fixtures with --record and replays them:
// the same verb against the replaying fake gives the same record (PRE-10,
// PRE-12).
func TestRecordAndReplay(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.AddInstance("default", "c1", map[string]string{"volatile.uuid": "8d4e1c2a"})
	opts := options(t, fake)
	opts.Record = filepath.Join(t.TempDir(), "fixtures")
	cmd := &InstanceShow{ServerCommand: opts, stdout: &bytes.Buffer{}}
	cmd.Args.Name = "c1"
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	var names []string
	entries, err := os.ReadDir(filepath.Join(opts.Record, "6.5"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{
		"0001-01-get-1.0.request.json", "0001-01-get-1.0.response.json",
		"0001-02-get-1.0-instances-c1.request.json", "0001-02-get-1.0-instances-c1.response.json",
		"0001-03-get-1.0-instances-c1.request.json", "0001-03-get-1.0-instances-c1.response.json",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("fixtures = %v; want %v", names, want)
	}
	recorded := record(t, opts, 1)

	replay := fakelxd.NewReplay(t, opts.Record)
	ropts := options(t, replay)
	cmd = &InstanceShow{ServerCommand: ropts, stdout: &bytes.Buffer{}}
	cmd.Args.Name = "c1"
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	replayed := record(t, ropts, 1)
	if replayed.Outcome != recorded.Outcome || !reflect.DeepEqual(replayed.Observations, recorded.Observations) {
		t.Errorf("replayed record differs:\n%v\n%v", replayed.Observations, recorded.Observations)
	}
}

func TestRedactNames(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.AddInstance("default", "secret-box", nil)
	opts := options(t, fake)
	opts.RedactNames = true
	opts.Record = filepath.Join(t.TempDir(), "fixtures")
	cmd := &InstanceShow{ServerCommand: opts, stdout: &bytes.Buffer{}}
	cmd.Args.Name = "secret-box"
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := record(t, opts, 1)
	if !slices.Equal(rec.Args, []string{"instance-1"}) {
		t.Errorf("args = %v", rec.Args)
	}
	assertAbsent(t, "secret-box", opts.EvidenceDir, opts.Record)
}

// assertAbsent fails if text appears in a file under dirs, other than the
// run's alias map.
func assertAbsent(t *testing.T, text string, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() == "aliases.json" {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), text) {
				t.Errorf("%s contains %q", path, text)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
