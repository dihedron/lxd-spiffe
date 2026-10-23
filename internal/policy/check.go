package policy

import (
	"fmt"
	"slices"

	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/api"
)

// Mode is a proof mode of attestor spec Trust model → Proof of co-location.
type Mode string

// The proof modes.
const (
	FilePull   Mode = "file_pull"
	ConfigPush Mode = "config_push"
	// ClaimOnly is proof_mode none (SEC-02): no proof, so no checks of a
	// proof mode.
	ClaimOnly Mode = "none"
)

// Code is the reason code of a denial (attestor spec Observability).
type Code string

// Reason codes of the policy checks.
const (
	NotRunning   Code = "not_running"
	ScopeDenied  Code = "scope_denied"
	PolicyDenied Code = "policy_denied"
)

// Denial is a failed policy check; Detail is for the server log only
// (SEC-10).
type Denial struct {
	Code   Code
	Detail string
}

func (d *Denial) Error() string {
	return fmt.Sprintf("attestation denied (%s): %s", d.Code, d.Detail)
}

func deny(code Code, format string, args ...any) error {
	return &Denial{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// Policy is the part of one server's configuration the checks use.
type Policy struct {
	Projects        []string // the server's project allow-list
	AllowPrivileged bool     // policy.allow_privileged
	DenyConfigKeys  []string // policy.deny_config_keys
	ProofDir        string   // proof_dir, for the isolation check
	// HostSideOwners selects the UID translation fallback: for an LXD
	// version that reports host-side owners. None tested does (gate L1).
	HostSideOwners bool
}

// Decision is what an accepted instance contributes to the proof.
type Decision struct {
	// ExpectedOwner is the single owner the file_pull proof file must
	// have, as the files API reports it.
	ExpectedOwner uint32
}

// Check runs the policy checks of FR-S5 on one instance read: running,
// project allowed, type as hinted, a valid volatile.uuid, privilege,
// denied config keys, and the checks of the proof mode. typeHint is the
// agent's instance type, empty when it gave none.
func Check(inst *api.Instance, p Policy, mode Mode, typeHint string) (Decision, error) {
	ec := inst.ExpandedConfig
	switch {
	case inst.Status != "Running":
		return Decision{}, deny(NotRunning, "status %q", inst.Status)
	case !slices.Contains(p.Projects, inst.Project):
		return Decision{}, deny(ScopeDenied, "project %q is not allowed", inst.Project)
	case typeHint != "" && typeHint != string(api.InstanceTypeContainer) && typeHint != string(api.InstanceTypeVM):
		return Decision{}, deny(PolicyDenied, "unknown type hint %q", typeHint)
	case typeHint != "" && typeHint != inst.Type:
		return Decision{}, deny(PolicyDenied, "type %q, the agent says %q", inst.Type, typeHint)
	case !isUUID(ec["volatile.uuid"]):
		return Decision{}, deny(PolicyDenied, "volatile.uuid missing or not a UUID")
	case shared.IsTrue(ec["security.privileged"]) && !p.AllowPrivileged:
		return Decision{}, deny(PolicyDenied, "privileged container")
	}
	for _, k := range p.DenyConfigKeys {
		if ec[k] != "" {
			return Decision{}, deny(PolicyDenied, "denied config key %q is set", k)
		}
	}
	switch mode {
	case FilePull:
		if iso := CheckIsolation(inst.ExpandedDevices, p.ProofDir); iso.Reject {
			return Decision{}, deny(PolicyDenied, "devices overlap the proof directory: %v", iso.Causes)
		}
		owner, err := ExpectedOwner(inst, p.HostSideOwners)
		if err != nil {
			return Decision{}, err
		}
		return Decision{ExpectedOwner: owner}, nil
	case ConfigPush:
		if shared.IsFalse(ec["security.devlxd"]) {
			return Decision{}, deny(PolicyDenied, "security.devlxd is off")
		}
		return Decision{}, nil
	case ClaimOnly:
		return Decision{}, nil
	default:
		return Decision{}, deny(PolicyDenied, "unknown proof mode %q", mode)
	}
}

// ExpectedOwner is the single owner accepted for the file_pull proof file
// (attestor spec Mode file_pull, step 6): uid 0, as the files API reports
// the in-container owner (gate L1). With hostSide, for an LXD version that
// would report host-side owners, an unprivileged container's owner is the
// host uid of namespace uid 0 through volatile.idmap.current, raw.idmap is
// refused, and every failure fails closed.
func ExpectedOwner(inst *api.Instance, hostSide bool) (uint32, error) {
	ec := inst.ExpandedConfig
	if !hostSide || inst.Type != string(api.InstanceTypeContainer) || shared.IsTrue(ec["security.privileged"]) {
		return 0, nil
	}
	if ec["raw.idmap"] != "" {
		return 0, deny(PolicyDenied, "raw.idmap is set and owners are host-side")
	}
	entries, err := ParseIDMap(ec["volatile.idmap.current"])
	if err != nil {
		return 0, deny(PolicyDenied, "%v", err)
	}
	owner, err := TranslateUID(entries, 0)
	if err != nil {
		return 0, deny(PolicyDenied, "%v", err)
	}
	return owner, nil
}

// isUUID accepts a canonical lowercase UUID, the form of volatile.uuid.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range []byte(s) {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}
