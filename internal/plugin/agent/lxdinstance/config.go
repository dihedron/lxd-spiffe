package lxdinstance

import (
	"cmp"
	"fmt"
	"os"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/claim"
	"github.com/dihedron/lxd-spiffe/internal/devlxd"
	"github.com/dihedron/lxd-spiffe/internal/plugin/config"
	"github.com/dihedron/lxd-spiffe/internal/proof"
)

// Config is the plugin_data block of the plugin in the SPIRE Agent
// configuration (attestor spec, Configuration → Agent).
type Config struct {
	Claim              *ClaimConfig `hcl:"claim"`
	UseDevLXD          string       `hcl:"use_devlxd"`
	DevLXDSocket       string       `hcl:"devlxd_socket"`
	ProofDir           string       `hcl:"proof_dir"`
	ProofTimeout       string       `hcl:"proof_timeout"`
	AttestationTimeout string       `hcl:"attestation_timeout"`
}

// ClaimConfig is the claim block (FR-A2).
type ClaimConfig struct {
	Project      string `hcl:"project"`
	InstanceName string `hcl:"instance_name"`
}

// configKeys lists the keys Config accepts: any other key is an error.
var configKeys = []string{
	"claim.project", "claim.instance_name",
	"use_devlxd", "devlxd_socket", "proof_dir", "proof_timeout", "attestation_timeout",
}

const defaultProofDir = "/run/spire/lxd"

// The values of use_devlxd (FR-A3).
const (
	devLXDAuto     = "auto"
	devLXDRequired = "required"
	devLXDOff      = "off"
)

// requiredUID is the euid the agent must run as and the owner of the proof
// directory (FR-A7); tests set it to their own uid.
var requiredUID = 0

// settings is a validated configuration; Configure replaces it as a whole.
type settings struct {
	claim              claim.Config
	devLXD             string
	socket             string
	proofDir           string
	proofTimeout       time.Duration
	attestationTimeout time.Duration
}

// invalid wraps a configuration error as config.ErrInvalid.
func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", config.ErrInvalid, fmt.Sprintf(format, a...))
}

// newSettings validates c and the process's euid (FR-A7: file_pull, the
// only mode yet, needs root).
func newSettings(c *Config) (*settings, error) {
	if euid := os.Geteuid(); euid != requiredUID {
		return nil, fmt.Errorf("the agent plugin must run as uid %d (file_pull writes a root-owned proof), not %d", requiredUID, euid)
	}
	s := &settings{devLXD: cmp.Or(c.UseDevLXD, devLXDAuto), socket: cmp.Or(c.DevLXDSocket, devlxd.DefaultSocket), proofDir: cmp.Or(c.ProofDir, defaultProofDir)}
	if c.Claim != nil {
		s.claim.Project, s.claim.InstanceName = c.Claim.Project, c.Claim.InstanceName
	}
	if s.claim.Project != "" {
		if err := claim.ValidProject(s.claim.Project); err != nil {
			return nil, invalid("claim.project: %v", err)
		}
	}
	if s.claim.InstanceName != "" {
		if err := claim.ValidInstanceName(s.claim.InstanceName); err != nil {
			return nil, invalid("claim.instance_name: %v", err)
		}
	}
	switch s.devLXD {
	case devLXDAuto, devLXDOff:
	case devLXDRequired:
		s.claim.DevLXDRequired = true
	default:
		return nil, invalid("use_devlxd must be auto, required or off")
	}
	if err := proof.ValidDir(s.proofDir); err != nil {
		return nil, invalid("%v", err)
	}
	var err error
	// as long as the server's, which bounds its proof_timeout to 2 min
	if s.proofTimeout, err = config.Duration("proof_timeout", c.ProofTimeout, 15*time.Second, time.Second, 2*time.Minute); err != nil {
		return nil, err
	}
	if s.attestationTimeout, err = config.Duration("attestation_timeout", c.AttestationTimeout, 30*time.Second, 2*time.Second, 5*time.Minute); err != nil {
		return nil, err
	}
	if s.proofTimeout >= s.attestationTimeout {
		return nil, invalid("proof_timeout (%v) must be shorter than attestation_timeout (%v)", s.proofTimeout, s.attestationTimeout)
	}
	return s, nil
}
