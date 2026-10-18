package selectors

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/canonical/lxd/shared/api"
)

// fixture loads an instance recorded from real LXD (gate L15).
func fixture(t *testing.T, name string) *api.Instance {
	t.Helper()
	data, err := os.ReadFile("../../test/fixtures/" + name + ".response.json")
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
	cUnpriv  = "6.9/0003-02-get-1.0-instances-c-unpriv"
	cPriv    = "6.9/0004-02-get-1.0-instances-c-priv"
	vUbuntu6 = "6.9/0007-02-get-1.0-instances-v-ubuntu"
	vUbuntu5 = "5.21.8/0007-02-get-1.0-instances-v-ubuntu"
)

// TestFRS8Defaults: the default set on real instances.
func TestFRS8Defaults(t *testing.T) {
	for name, tt := range map[string]struct {
		fixture string
		want    []string
	}{
		"unprivileged container": {cUnpriv, []string{
			"server:lxd-prod", "uuid:fbf73246-b07c-4291-afd9-12ae2be70828", "project:default", "type:container",
			"privileged:false", "profile:default", "network:default/lxdbr0"}},
		"privileged container": {cPriv, []string{
			"server:lxd-prod", "uuid:" + fixture(t, cPriv).ExpandedConfig["volatile.uuid"], "project:default", "type:container",
			"privileged:true", "profile:default", "network:default/lxdbr0"}},
		"VM on 6.9: no privileged selector": {vUbuntu6, []string{
			"server:lxd-prod", "uuid:" + fixture(t, vUbuntu6).ExpandedConfig["volatile.uuid"], "project:default", "type:virtual-machine",
			"profile:default", "network:default/lxdbr0"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, omitted, err := Build(Config{}, Input{ServerID: "lxd-prod", Instance: fixture(t, tt.fixture), NetworkProject: "default"})
			if err != nil || len(omitted) > 0 {
				t.Fatalf("omitted %v, error %v", omitted, err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("selectors\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// TestFRS8AllEnabled: every selector, in the order of the spec's table.
func TestFRS8AllEnabled(t *testing.T) {
	inst := fixture(t, cUnpriv)
	inst.ExpandedConfig["user.team"] = "payments"
	inst.ExpandedConfig["limits.cpu"] = "2"
	cfg := Config{Enable: Names, ConfigKeys: []string{"limits.cpu", "limits.memory"}, UserKeys: []string{"team", "absent"}}
	got, omitted, err := Build(cfg, Input{ServerID: "lxd-prod", Instance: inst, NetworkProject: "default"})
	if err != nil || len(omitted) > 0 {
		t.Fatalf("omitted %v, error %v", omitted, err)
	}
	want := []string{
		"server:lxd-prod", "uuid:fbf73246-b07c-4291-afd9-12ae2be70828", "project:default", "type:container",
		"arch:x86_64", "privileged:false", "profile:default", "network:default/lxdbr0",
		"base-image:ee3016f85cc452b34d0ad0ff5e2638948e6c905fa5068b68b36f45ef53fd628e", "image:ubuntu/noble",
		"config:limits.cpu:2", "user:team:payments",
		"generation:fbf73246-b07c-4291-afd9-12ae2be70828", "name:c-unpriv",
	}
	if !slices.Equal(got, want) {
		t.Errorf("selectors\n got %q\nwant %q", got, want)
	}
}

func TestMember(t *testing.T) {
	inst := fixture(t, cUnpriv)
	cfg := Config{Enable: []string{"member"}}
	if got, _, _ := Build(cfg, Input{Instance: inst}); len(got) != 0 {
		t.Errorf("standalone (location none): %q", got)
	}
	inst.Location = "c6n2"
	if got, _, _ := Build(cfg, Input{Instance: inst}); !slices.Equal(got, []string{"member:c6n2"}) {
		t.Errorf("cluster member: %q", got)
	}
}

// TestSecureBoot: boot.mode on 6.x, security.secureboot and security.csm on
// 5.21, secure boot on when neither is set; VMs only.
func TestSecureBoot(t *testing.T) {
	cfg := Config{Enable: []string{"secureboot"}}
	for name, tt := range map[string]struct {
		fixture string
		config  map[string]string
		want    []string
	}{
		"6.9 VM, keys unset":          {vUbuntu6, nil, []string{"secureboot:true"}},
		"5.21 VM, keys unset":         {vUbuntu5, nil, []string{"secureboot:true"}},
		"boot.mode uefi-secureboot":   {vUbuntu6, map[string]string{"boot.mode": "uefi-secureboot"}, []string{"secureboot:true"}},
		"boot.mode uefi-nosecureboot": {vUbuntu6, map[string]string{"boot.mode": "uefi-nosecureboot"}, []string{"secureboot:false"}},
		"boot.mode bios":              {vUbuntu6, map[string]string{"boot.mode": "bios"}, []string{"secureboot:false"}},
		"boot.mode wins":              {vUbuntu6, map[string]string{"boot.mode": "uefi-nosecureboot", "security.secureboot": "true"}, []string{"secureboot:false"}},
		"security.secureboot false":   {vUbuntu5, map[string]string{"security.secureboot": "false"}, []string{"secureboot:false"}},
		"security.secureboot true":    {vUbuntu5, map[string]string{"security.secureboot": "true"}, []string{"secureboot:true"}},
		"security.csm true":           {vUbuntu5, map[string]string{"security.csm": "true"}, []string{"secureboot:false"}},
		"container: none":             {cUnpriv, nil, nil},
	} {
		t.Run(name, func(t *testing.T) {
			inst := fixture(t, tt.fixture)
			for k, v := range tt.config {
				inst.ExpandedConfig[k] = v
			}
			if got, _, _ := Build(cfg, Input{Instance: inst}); !slices.Equal(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestNetwork: one selector per NIC, in device name order, with the
// project that holds the networks (Selectors → Network project, L16).
func TestNetwork(t *testing.T) {
	inst := fixture(t, cUnpriv)
	inst.ExpandedDevices["eth1"] = map[string]string{"type": "nic", "nictype": "macvlan", "parent": "enp5s0"}
	inst.ExpandedDevices["a0"] = map[string]string{"type": "nic", "network": "ovn-a"}
	inst.ExpandedDevices["e9"] = map[string]string{"type": "nic"}
	inst.ExpandedDevices["data"] = map[string]string{"type": "disk", "source": "/srv", "path": "/srv"}
	cfg := Config{Enable: []string{"network"}}
	if !NeedsNetworkProject(cfg, inst) {
		t.Error("a managed NIC needs the network project")
	}
	if got, _, _ := Build(cfg, Input{Instance: inst, NetworkProject: "team.a"}); !slices.Equal(got, []string{"network:team.a/ovn-a", "network:team.a/lxdbr0", "nic-parent:enp5s0"}) {
		t.Errorf("selectors %q", got)
	}
	if _, _, err := Build(cfg, Input{Instance: inst}); !errors.Is(err, ErrNoNetworkProject) {
		t.Errorf("without the network project: %v", err)
	}
}

// TestNoNetworkProjectRead: no project read when the network selector is
// disabled or the instance has no managed NIC.
func TestNoNetworkProjectRead(t *testing.T) {
	inst := fixture(t, cUnpriv)
	if NeedsNetworkProject(Config{Enable: []string{"uuid"}}, inst) {
		t.Error("network selector disabled")
	}
	inst.ExpandedDevices = map[string]map[string]string{"eth0": {"type": "nic", "nictype": "macvlan", "parent": "enp5s0"}}
	cfg := Config{Enable: []string{"network"}}
	if NeedsNetworkProject(cfg, inst) {
		t.Error("unmanaged NIC only")
	}
	if got, _, err := Build(cfg, Input{Instance: inst}); err != nil || !slices.Equal(got, []string{"nic-parent:enp5s0"}) {
		t.Errorf("selectors %q, %v", got, err)
	}
}

// TestSelectorValueRule: invalid UTF-8, control characters and values over
// 256 bytes are left out and reported, never truncated (Selectors).
func TestSelectorValueRule(t *testing.T) {
	inst := fixture(t, cUnpriv)
	inst.ExpandedConfig["user.ok"] = "fine: with colons"
	inst.ExpandedConfig["user.long"] = strings.Repeat("x", 256)
	inst.ExpandedConfig["user.edge"] = strings.Repeat("y", 256-len("user:edge:"))
	inst.ExpandedConfig["user.ctl"] = "a\nb"
	inst.ExpandedConfig["user.utf"] = "\xff"
	got, omitted, err := Build(Config{Enable: []string{}, UserKeys: []string{"ok", "long", "edge", "ctl", "utf"}}, Input{Instance: inst})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"user:ok:fine: with colons", "user:edge:" + strings.Repeat("y", 246)}; !slices.Equal(got, want) {
		t.Errorf("kept %q", got)
	}
	var names []string
	for _, o := range omitted {
		names = append(names, o.Selector)
		if o.Reason == "" {
			t.Errorf("%s omitted without a reason", o.Selector)
		}
	}
	if !slices.Equal(names, []string{"user:long", "user:ctl", "user:utf"}) {
		t.Errorf("omitted %v", omitted)
	}
}

func TestConfigValidate(t *testing.T) {
	if err := (Config{}).Validate(); err != nil {
		t.Errorf("default: %v", err)
	}
	if err := (Config{Enable: Names, ConfigKeys: []string{"limits.cpu", "volatile.eth0.hwaddr"}, UserKeys: []string{"team"}}).Validate(); err != nil {
		t.Errorf("all: %v", err)
	}
	for name, cfg := range map[string]Config{
		"unknown name":                 {Enable: []string{"hostname"}},
		"config is not an enable name": {Enable: []string{"config"}},
		"duplicate name":               {Enable: []string{"uuid", "uuid"}},
		"user key among config keys":   {ConfigKeys: []string{"user.team"}},
		"empty config key":             {ConfigKeys: []string{""}},
		"config key with a colon":      {ConfigKeys: []string{"a:b"}},
		"user key with its prefix":     {UserKeys: []string{"user.team"}},
		"user key with a space":        {UserKeys: []string{"te am"}},
		"duplicate user key":           {UserKeys: []string{"team", "team"}},
	} {
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
