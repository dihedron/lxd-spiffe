// Package selectors builds the lxd_instance selectors of attestor spec
// Server-side plugin → Selectors (FR-S8) from one instance read. Every value
// comes from LXD (SEC-15): Build takes no claim.
package selectors

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/api"
)

// MaxValue bounds a selector value, "<name>:<value>", in bytes.
const MaxValue = 256

// Names are the names selectors.enable takes, in the order of the spec's
// table; config and user selectors are enabled by listing their keys.
var Names = []string{"server", "uuid", "project", "type", "arch", "privileged", "profile", "network", "member", "base-image", "image", "secureboot", "generation", "name"}

// DefaultEnable is the default of selectors.enable.
var DefaultEnable = []string{"server", "uuid", "project", "type", "privileged", "profile", "network"}

// ErrNoNetworkProject: the instance has a managed NIC, the network selector
// is enabled, and the caller gave no network project.
var ErrNoNetworkProject = errors.New("the network selector needs the project that holds the instance's networks")

// Config is the selectors block of the server configuration.
type Config struct {
	// Enable lists the enabled selectors; nil means DefaultEnable, an empty
	// slice none.
	Enable []string
	// ConfigKeys are full config keys, not user.*, for config selectors.
	ConfigKeys []string
	// UserKeys are user keys without their "user." prefix, for user selectors.
	UserKeys []string
}

// Validate checks the configuration at Configure.
func (c Config) Validate() error {
	enabled := c.enabled()
	seen := map[string]bool{}
	for _, n := range enabled {
		if !slices.Contains(Names, n) {
			return fmt.Errorf("selectors.enable: unknown selector %q", n)
		}
		if seen[n] {
			return fmt.Errorf("selectors.enable: %q listed twice", n)
		}
		seen[n] = true
	}
	if err := validKeys("selectors.config_keys", c.ConfigKeys); err != nil {
		return err
	}
	for _, k := range c.ConfigKeys {
		if strings.HasPrefix(k, "user.") {
			return fmt.Errorf("selectors.config_keys: %q is a user key: list it in user_keys without its prefix", k)
		}
	}
	if err := validKeys("selectors.user_keys", c.UserKeys); err != nil {
		return err
	}
	for _, k := range c.UserKeys {
		if strings.HasPrefix(k, "user.") {
			return fmt.Errorf("selectors.user_keys: %q: give the key without its user. prefix", k)
		}
	}
	return nil
}

func validKeys(field string, keys []string) error {
	seen := map[string]bool{}
	for _, k := range keys {
		if k == "" || len(k) > MaxValue {
			return fmt.Errorf("%s: key of %d bytes", field, len(k))
		}
		for _, c := range []byte(k) {
			if c <= ' ' || c > '~' || c == ':' {
				return fmt.Errorf("%s: key %q with character %q", field, k, c)
			}
		}
		if seen[k] {
			return fmt.Errorf("%s: %q listed twice", field, k)
		}
		seen[k] = true
	}
	return nil
}

func (c Config) enabled() []string {
	if c.Enable == nil {
		return DefaultEnable
	}
	return c.Enable
}

// Input is what one attestation contributes.
type Input struct {
	ServerID string
	Instance *api.Instance
	// NetworkProject is the project that holds the instance's networks: the
	// instance's project when it has features.networks=true, default
	// otherwise (Selectors → Network project). The server reads it only when
	// NeedsNetworkProject says so.
	NetworkProject string
}

// Omitted is a selector left out by the value rule, for the WARN.
type Omitted struct {
	Selector string // "<name>" or "<name>:<key>"
	Reason   string
}

// Build returns the enabled selector values, "<name>:<value>", in the order
// of the spec's table, and the selectors the value rule left out. Keys
// without an LXD default that are unset give no selector.
func Build(cfg Config, in Input) ([]string, []Omitted, error) {
	inst := in.Instance
	ec := inst.ExpandedConfig
	enabled := cfg.enabled()
	on := func(name string) bool { return slices.Contains(enabled, name) }
	if NeedsNetworkProject(cfg, inst) && in.NetworkProject == "" {
		return nil, nil, ErrNoNetworkProject
	}
	b := builder{}
	if on("server") {
		b.add("server", "server", in.ServerID)
	}
	if on("uuid") {
		b.add("uuid", "uuid", ec["volatile.uuid"])
	}
	if on("project") {
		b.add("project", "project", inst.Project)
	}
	if on("type") {
		b.add("type", "type", inst.Type)
	}
	if on("arch") {
		b.add("arch", "arch", inst.Architecture)
	}
	if on("privileged") && inst.Type == string(api.InstanceTypeContainer) {
		b.add("privileged", "privileged", strconv.FormatBool(shared.IsTrue(ec["security.privileged"])))
	}
	if on("profile") {
		for _, p := range inst.Profiles {
			b.add("profile", "profile", p)
		}
	}
	if on("network") {
		for _, name := range slices.Sorted(maps.Keys(inst.ExpandedDevices)) {
			d := inst.ExpandedDevices[name]
			switch {
			case d["type"] != "nic":
			case d["network"] != "":
				b.add("network", "network", in.NetworkProject+"/"+d["network"])
			case d["parent"] != "":
				b.add("network", "nic-parent", d["parent"])
			}
		}
	}
	if on("member") && inst.Location != "none" {
		b.add("member", "member", inst.Location)
	}
	if on("base-image") {
		b.add("base-image", "base-image", ec["volatile.base_image"])
	}
	if on("image") && ec["image.os"] != "" && ec["image.release"] != "" {
		b.add("image", "image", ec["image.os"]+"/"+ec["image.release"])
	}
	if on("secureboot") && inst.Type == string(api.InstanceTypeVM) {
		b.add("secureboot", "secureboot", strconv.FormatBool(secureBoot(ec)))
	}
	for _, k := range cfg.ConfigKeys {
		if v, ok := ec[k]; ok {
			b.add("config:"+k, "config", k+":"+v)
		}
	}
	for _, k := range cfg.UserKeys {
		if v, ok := ec["user."+k]; ok {
			b.add("user:"+k, "user", k+":"+v)
		}
	}
	if on("generation") {
		b.add("generation", "generation", ec["volatile.uuid.generation"])
	}
	if on("name") {
		b.add("name", "name", inst.Name)
	}
	return b.values, b.omitted, nil
}

// NeedsNetworkProject reports whether Build needs the project that holds
// the instance's networks: the network selector is enabled and the
// instance has a NIC with a managed network. Only then does the server
// read the project.
func NeedsNetworkProject(cfg Config, inst *api.Instance) bool {
	if !slices.Contains(cfg.enabled(), "network") {
		return false
	}
	for _, d := range inst.ExpandedDevices {
		if d["type"] == "nic" && d["network"] != "" {
			return true
		}
	}
	return false
}

// secureBoot derives the VM's secure boot state: boot.mode on LXD 6.x,
// security.secureboot and security.csm on 5.21, on when none is set.
func secureBoot(ec map[string]string) bool {
	if mode, ok := ec["boot.mode"]; ok {
		return mode == "uefi-secureboot"
	}
	if shared.IsTrue(ec["security.csm"]) {
		return false
	}
	if v, ok := ec["security.secureboot"]; ok {
		return shared.IsTrue(v)
	}
	return true
}

type builder struct {
	values  []string
	omitted []Omitted
}

// add appends "<name>:<value>" unless value is empty; a value that breaks
// the value rule is reported under label instead.
func (b *builder) add(label, name, value string) {
	if value == "" {
		return
	}
	v := name + ":" + value
	switch {
	case !utf8.ValidString(v):
		b.omitted = append(b.omitted, Omitted{label, "not valid UTF-8"})
	case strings.ContainsFunc(v, unicode.IsControl):
		b.omitted = append(b.omitted, Omitted{label, "control character"})
	case len(v) > MaxValue:
		b.omitted = append(b.omitted, Omitted{label, fmt.Sprintf("%d bytes, over %d", len(v), MaxValue)})
	default:
		b.values = append(b.values, v)
	}
}
