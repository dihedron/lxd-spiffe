package util

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/policy"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

// Overlap is util overlap RECORD... (PRU-04): the attestor's isolation
// check, from internal/policy, on the expanded_devices of instance show
// records.
type Overlap struct {
	probe.GlobalOptions
	ProofDir string `long:"proof-dir" description:"Proof directory to check." default:"/run/spire/lxd"`
	Args     struct {
		Records []string `positional-arg-name:"RECORD" description:"Records of server instance show." required:"1"`
	} `positional-args:"yes"`

	stdout io.Writer
}

// overlapResult is the check of one record as recorded.
type overlapResult struct {
	Record   string                 `json:"record"`
	Instance string                 `json:"instance,omitempty"`
	Verdict  string                 `json:"verdict"`
	Causes   []policy.DeviceFinding `json:"causes"`
	Info     []policy.DeviceFinding `json:"info"`
}

// Execute runs the command.
func (cmd *Overlap) Execute(args []string) error {
	type input struct {
		rec     *evidence.Record
		devices map[string]map[string]string
	}
	var inputs []input
	// file names only: a full path may hold a user's name
	recorded := []string{"--proof-dir", cmd.ProofDir}
	for _, path := range cmd.Args.Records {
		rec, err := readRecord(path)
		if err != nil {
			return exit.New(exit.Usage, fmt.Errorf("util overlap: %w", err))
		}
		var devices map[string]map[string]string
		if err := observationAs(rec, "instance.expanded_devices", &devices); err != nil {
			return exit.New(exit.Usage, fmt.Errorf("util overlap: %s: %w", filepath.Base(path), err))
		}
		inputs = append(inputs, input{rec, devices})
		recorded = append(recorded, filepath.Base(path))
	}
	s, err := probe.NewSession(&cmd.GlobalOptions, cmd.stdout, "util", "overlap", recorded)
	if err != nil {
		return err
	}
	s.Observe("overlap.proof_dir", cmd.ProofDir)
	results := []overlapResult{}
	sources := map[string][]string{}
	for _, in := range inputs {
		iso := policy.CheckIsolation(in.devices, cmd.ProofDir)
		r := overlapResult{Record: in.rec.ID, Verdict: "pass", Causes: iso.Causes, Info: iso.Info}
		if len(in.rec.Args) > 0 {
			r.Instance = in.rec.Args[0]
		}
		if iso.Reject {
			r.Verdict = "reject"
		}
		results = append(results, r)
		for _, d := range in.devices {
			if src := policy.DiskSource(d); src != "" && !slices.Contains(sources[src], in.rec.ID) {
				sources[src] = append(sources[src], in.rec.ID)
			}
		}
	}
	s.Observe("overlap.results", results)
	shared := map[string][]string{}
	for src, ids := range sources {
		if len(ids) > 1 {
			slices.Sort(ids)
			shared[src] = ids
		}
	}
	s.Observe("overlap.shared_sources", shared)
	return s.Finish(nil)
}
