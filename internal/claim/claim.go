// Package claim holds what the guest says about itself: the syntax of the
// claimed names (attestor spec FR-S2) and the agent's collection of the
// claim (FR-A2). A claim is an untrusted lookup hint: the server resolves
// it through LXD and never offers it as a selector (SEC-15).
package claim

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dihedron/lxd-spiffe/internal/limits"
)

// ErrInvalid is wrapped by every refusal of a claim value.
var ErrInvalid = errors.New("invalid claim")

// Source records where the instance name came from, for diagnostics.
type Source int

// The sources of FR-A2, in order of precedence.
const (
	SourceUnspecified Source = iota
	SourceConfigured
	SourceDevLXDMetaData
	SourceHostname
)

// Claim is the claim the agent sends.
type Claim struct {
	Project             string // empty: the server looks in every allowed project
	InstanceName        string
	CloudInitInstanceID string // empty when devLXD gave none
	NameSource          Source
}

// ValidInstanceName applies LXD's instance name rule, the SDK's
// shared/validate.IsHostname, reimplemented: that package would bring four
// more modules and os/exec into the agent for these few lines. It is
// stricter in one corner: LXD's number test is a ParseUint, so it accepts
// an all-digit name too long for 64 bits, which is refused here.
func ValidInstanceName(name string) error {
	switch {
	case name == "" || len(name) > limits.MaxName:
		return fmt.Errorf("%w: instance name of %d bytes", ErrInvalid, len(name))
	case name[0] == '-' || name[len(name)-1] == '-':
		return fmt.Errorf("%w: instance name starting or ending with a hyphen", ErrInvalid)
	}
	digits := true
	for _, c := range []byte(name) {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-':
			digits = false
		default:
			return fmt.Errorf("%w: instance name with character %q", ErrInvalid, c)
		}
	}
	if digits {
		return fmt.Errorf("%w: instance name that is a number", ErrInvalid)
	}
	return nil
}

// ValidProject applies LXD's project name rule (lxd/project.ValidName,
// reimplemented: NFR-03) restricted to printable ASCII, with the plugin's
// cap of limits.MaxName bytes.
func ValidProject(name string) error {
	switch {
	case name == "" || len(name) > limits.MaxName:
		return fmt.Errorf("%w: project name of %d bytes", ErrInvalid, len(name))
	case name == "*" || name == "." || name == "..":
		return fmt.Errorf("%w: reserved project name", ErrInvalid)
	}
	for _, c := range []byte(name) {
		if c < '!' || c > '~' || strings.IndexByte(`\/_'"`, c) >= 0 {
			return fmt.Errorf("%w: project name with character %q", ErrInvalid, c)
		}
	}
	return nil
}

// ValidCloudInitID accepts a canonical lowercase UUID, the form of LXD's
// volatile.cloud-init.instance-id.
func ValidCloudInitID(id string) error {
	if len(id) != 36 {
		return fmt.Errorf("%w: cloud-init instance id of %d bytes", ErrInvalid, len(id))
	}
	for i, c := range []byte(id) {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return fmt.Errorf("%w: cloud-init instance id is not a UUID", ErrInvalid)
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return fmt.Errorf("%w: cloud-init instance id is not a lowercase UUID", ErrInvalid)
			}
		}
	}
	return nil
}

// MetaData is what the agent uses of devLXD's /1.0/meta-data.
type MetaData struct {
	InstanceID    string
	LocalHostname string
}

// ParseMetaData parses devLXD's /1.0/meta-data, cloud-init "key: value"
// lines (FR-A2, gate L3). Unknown keys, comments and blank lines are
// ignored; a repeated key or a line without a separator is refused.
func ParseMetaData(text string) (MetaData, error) {
	var md MetaData
	seen := map[string]bool{}
	for line := range strings.Lines(text) {
		line = strings.TrimRight(line, "\r\n")
		if strings.IndexByte(line, 0) >= 0 {
			return MetaData{}, fmt.Errorf("%w: meta-data with a NUL byte", ErrInvalid)
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return MetaData{}, fmt.Errorf("%w: meta-data line without a separator", ErrInvalid)
		}
		key = strings.TrimSpace(key)
		if seen[key] {
			return MetaData{}, fmt.Errorf("%w: meta-data key %q repeated", ErrInvalid, key)
		}
		seen[key] = true
		switch key {
		case "instance-id":
			md.InstanceID = strings.TrimSpace(value)
		case "local-hostname":
			md.LocalHostname = strings.TrimSpace(value)
		}
	}
	return md, nil
}

// MetaDataReader reads devLXD's meta-data.
type MetaDataReader interface {
	MetaData(ctx context.Context) (MetaData, error)
}

// Config is the agent's claim configuration (FR-A2, FR-A3).
type Config struct {
	Project        string // claim.project
	InstanceName   string // claim.instance_name
	DevLXDRequired bool   // use_devlxd = "required"
}

// Sources are the claim sources besides configuration; DevLXD is nil when
// use_devlxd is "off" or no socket exists.
type Sources struct {
	DevLXD   MetaDataReader
	Hostname func() (string, error)
}

// Collect builds the claim, per field from configuration, then devLXD's
// meta-data, then the OS hostname (name only); the project comes from
// configuration only, since devLXD gives none (gate L3). A devLXD failure
// is fatal only when devLXD is required. The result is validated (FR-S2).
func Collect(ctx context.Context, cfg Config, src Sources) (Claim, error) {
	c := Claim{Project: cfg.Project, InstanceName: cfg.InstanceName}
	if c.InstanceName != "" {
		c.NameSource = SourceConfigured
	}
	var md MetaData
	switch {
	case src.DevLXD != nil:
		var err error
		if md, err = src.DevLXD.MetaData(ctx); err != nil {
			if cfg.DevLXDRequired {
				return Claim{}, fmt.Errorf("reading devLXD meta-data: %w", err)
			}
			md = MetaData{}
		}
	case cfg.DevLXDRequired:
		return Claim{}, errors.New("devLXD is required but not available")
	}
	c.CloudInitInstanceID = md.InstanceID
	if c.InstanceName == "" && md.LocalHostname != "" {
		c.InstanceName, c.NameSource = md.LocalHostname, SourceDevLXDMetaData
	}
	if c.InstanceName == "" && src.Hostname != nil {
		name, err := src.Hostname()
		if err != nil {
			return Claim{}, fmt.Errorf("reading the hostname: %w", err)
		}
		c.InstanceName, c.NameSource = name, SourceHostname
	}
	if err := c.Validate(); err != nil {
		return Claim{}, err
	}
	return c, nil
}

// Validate checks the claim's syntax (FR-S2): a valid instance name, and a
// project and cloud-init instance id that are empty or valid.
func (c Claim) Validate() error {
	if err := ValidInstanceName(c.InstanceName); err != nil {
		return err
	}
	if c.Project != "" {
		if err := ValidProject(c.Project); err != nil {
			return err
		}
	}
	if c.CloudInitInstanceID != "" {
		if err := ValidCloudInitID(c.CloudInitInstanceID); err != nil {
			return err
		}
	}
	return nil
}
