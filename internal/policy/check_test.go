package policy

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/canonical/lxd/shared/api"
)

// instance loads an instance recorded from real LXD 6.9 (gate L15).
func instance(t *testing.T, name string) *api.Instance {
	t.Helper()
	data, err := os.ReadFile("../../test/fixtures/6.9/" + name + ".response.json")
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Body struct {
			Metadata api.Instance `json:"metadata"`
		} `json:"body"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	return &rec.Body.Metadata
}

const (
	fxUnpriv = "0003-02-get-1.0-instances-c-unpriv"
	fxPriv   = "0004-02-get-1.0-instances-c-priv"
	fxIso    = "0005-02-get-1.0-instances-c-iso"
	fxRaw    = "0006-02-get-1.0-instances-c-raw"
	fxVM     = "0007-02-get-1.0-instances-v-ubuntu"
)

func basePolicy() Policy {
	return Policy{Projects: []string{"default"}, DenyConfigKeys: []string{"raw.lxc"}, ProofDir: "/run/spire/lxd"}
}

func code(err error) Code {
	var d *Denial
	if errors.As(err, &d) {
		return d.Code
	}
	return ""
}

// TestFRS5Accepts: real instances that pass, with expected owner uid 0 in
// every class (gate L1: the files API reports the in-container owner).
func TestFRS5Accepts(t *testing.T) {
	for _, tt := range []struct {
		fixture string
		mode    Mode
		hint    string
	}{
		{fxUnpriv, FilePull, ""},
		{fxUnpriv, FilePull, "container"},
		{fxIso, FilePull, "container"},
		{fxRaw, ConfigPush, ""},
		{fxVM, FilePull, "virtual-machine"},
		{fxVM, ConfigPush, ""},
	} {
		d, err := Check(instance(t, tt.fixture), basePolicy(), tt.mode, tt.hint)
		if err != nil {
			t.Errorf("%s %s: %v", tt.fixture, tt.mode, err)
			continue
		}
		if tt.mode == FilePull && d.ExpectedOwner != 0 {
			t.Errorf("%s: expected owner %d", tt.fixture, d.ExpectedOwner)
		}
	}
}

func TestFRS5Denies(t *testing.T) {
	for name, tt := range map[string]struct {
		fixture string
		mutate  func(*api.Instance, *Policy)
		mode    Mode
		hint    string
		want    Code
	}{
		"stopped":               {fxUnpriv, func(i *api.Instance, _ *Policy) { i.Status = "Stopped" }, FilePull, "", NotRunning},
		"frozen":                {fxUnpriv, func(i *api.Instance, _ *Policy) { i.Status = "Frozen" }, FilePull, "", NotRunning},
		"project not allowed":   {fxUnpriv, func(_ *api.Instance, p *Policy) { p.Projects = []string{"spire-nodes"} }, FilePull, "", ScopeDenied},
		"no allowed project":    {fxUnpriv, func(_ *api.Instance, p *Policy) { p.Projects = nil }, FilePull, "", ScopeDenied},
		"type hint differs":     {fxVM, func(*api.Instance, *Policy) {}, FilePull, "container", PolicyDenied},
		"unknown type hint":     {fxUnpriv, func(*api.Instance, *Policy) {}, FilePull, "lxc", PolicyDenied},
		"no volatile.uuid":      {fxUnpriv, func(i *api.Instance, _ *Policy) { delete(i.ExpandedConfig, "volatile.uuid") }, FilePull, "", PolicyDenied},
		"volatile.uuid garbled": {fxUnpriv, func(i *api.Instance, _ *Policy) { i.ExpandedConfig["volatile.uuid"] = "not-a-uuid" }, FilePull, "", PolicyDenied},
		"privileged":            {fxPriv, func(*api.Instance, *Policy) {}, FilePull, "", PolicyDenied},
		"denied config key":     {fxUnpriv, func(i *api.Instance, _ *Policy) { i.ExpandedConfig["raw.lxc"] = "lxc.apparmor.profile=unconfined" }, FilePull, "", PolicyDenied},
		"shared disk at proof dir": {fxUnpriv, func(i *api.Instance, _ *Policy) {
			i.ExpandedDevices["spire"] = map[string]string{"type": "disk", "source": "/srv/shared", "path": "/run/spire/lxd"}
		}, FilePull, "", PolicyDenied},
		"shared disk above proof dir": {fxUnpriv, func(i *api.Instance, _ *Policy) {
			i.ExpandedDevices["run"] = map[string]string{"type": "disk", "source": "/srv/run", "path": "/run"}
		}, FilePull, "", PolicyDenied},
		"devLXD off for config_push": {fxVM, func(i *api.Instance, _ *Policy) { i.ExpandedConfig["security.devlxd"] = "false" }, ConfigPush, "", PolicyDenied},
		"unknown mode":               {fxUnpriv, func(*api.Instance, *Policy) {}, Mode("ready_signal"), "", PolicyDenied},
	} {
		t.Run(name, func(t *testing.T) {
			inst, p := instance(t, tt.fixture), basePolicy()
			tt.mutate(inst, &p)
			_, err := Check(inst, p, tt.mode, tt.hint)
			if got := code(err); got != tt.want {
				t.Errorf("code %q (%v), want %q", got, err, tt.want)
			}
		})
	}
}

func TestFRS5PrivilegedAllowed(t *testing.T) {
	p := basePolicy()
	p.AllowPrivileged = true
	if _, err := Check(instance(t, fxPriv), p, FilePull, ""); err != nil {
		t.Error(err)
	}
}

// The shared disk check is file_pull's: config_push does not read files.
func TestIsolationIsFilePullOnly(t *testing.T) {
	inst := instance(t, fxUnpriv)
	inst.ExpandedDevices["spire"] = map[string]string{"type": "disk", "source": "/srv/shared", "path": "/run/spire/lxd"}
	if _, err := Check(inst, basePolicy(), ConfigPush, ""); err != nil {
		t.Error(err)
	}
}

// TestExpectedOwnerHostSide: the fallback for an LXD version that reports
// host-side owners (none tested does): the host uid of namespace uid 0 for
// unprivileged containers, uid 0 for privileged containers and VMs, and a
// refusal with raw.idmap or a map that does not translate.
func TestExpectedOwnerHostSide(t *testing.T) {
	for name, tt := range map[string]struct {
		fixture string
		mutate  func(*api.Instance)
		want    uint32
		denied  bool
	}{
		"unprivileged":  {fxUnpriv, nil, 1000000, false},
		"isolated":      {fxIso, nil, 1065536, false},
		"privileged":    {fxPriv, nil, 0, false},
		"VM":            {fxVM, nil, 0, false},
		"raw.idmap":     {fxRaw, nil, 0, true},
		"idmap missing": {fxUnpriv, func(i *api.Instance) { delete(i.ExpandedConfig, "volatile.idmap.current") }, 0, true},
		"idmap garbled": {fxUnpriv, func(i *api.Instance) { i.ExpandedConfig["volatile.idmap.current"] = "{" }, 0, true},
	} {
		t.Run(name, func(t *testing.T) {
			inst := instance(t, tt.fixture)
			if tt.mutate != nil {
				tt.mutate(inst)
			}
			got, err := ExpectedOwner(inst, true)
			if tt.denied {
				if code(err) != PolicyDenied {
					t.Fatalf("owner %d, error %v: want policy_denied", got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("owner %d, error %v, want %d", got, err, tt.want)
			}
		})
	}
	// and through Check, which passes the policy's setting
	p := basePolicy()
	p.HostSideOwners = true
	if d, err := Check(instance(t, fxIso), p, FilePull, ""); err != nil || d.ExpectedOwner != 1065536 {
		t.Errorf("Check with host-side owners: %+v %v", d, err)
	}
}

func TestDenialError(t *testing.T) {
	_, err := Check(instance(t, fxPriv), basePolicy(), FilePull, "")
	if got := err.Error(); got != "attestation denied (policy_denied): privileged container" {
		t.Errorf("%q", got)
	}
}

func TestIsUUID(t *testing.T) {
	for s, want := range map[string]bool{
		"fbf73246-b07c-4291-afd9-12ae2be70828": true,
		"FBF73246-B07C-4291-AFD9-12AE2BE70828": false,
		"fbf73246xb07c-4291-afd9-12ae2be70828": false,
		"fbf73246-b07c-4291-afd9-12ae2be7082":  false,
		"fbf73246-b07c-4291-afd9-12ae2be7082g": false,
	} {
		if got := isUUID(s); got != want {
			t.Errorf("isUUID(%q) = %v", s, got)
		}
	}
}

// A map that does not cover namespace uid 0 fails closed, also through Check.
func TestExpectedOwnerNoEntry(t *testing.T) {
	inst := instance(t, fxUnpriv)
	inst.ExpandedConfig["volatile.idmap.current"] = `[{"Isuid":true,"Isgid":false,"Hostid":1000000,"Nsid":1,"Maprange":10}]`
	p := basePolicy()
	p.HostSideOwners = true
	if _, err := Check(inst, p, FilePull, ""); code(err) != PolicyDenied {
		t.Errorf("error %v", err)
	}
}

// TestSEC02ClaimOnly: proof_mode none keeps every check that is not a
// proof mode's (SEC-02 issues on the claim, not on a weaker policy).
func TestSEC02ClaimOnly(t *testing.T) {
	if _, err := Check(instance(t, fxUnpriv), basePolicy(), ClaimOnly, ""); err != nil {
		t.Errorf("running, allowed: %v", err)
	}
	stopped := instance(t, fxUnpriv)
	stopped.Status = "Stopped"
	var d *Denial
	if _, err := Check(stopped, basePolicy(), ClaimOnly, ""); !errors.As(err, &d) || d.Code != NotRunning {
		t.Errorf("stopped: %v", err)
	}
}
