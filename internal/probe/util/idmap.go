package util

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/policy"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

// IdmapTranslate is util idmap translate (PRU-02): the attestor's uid
// translation, from internal/policy.
type IdmapTranslate struct {
	probe.GlobalOptions
	VolatileIdmap string `long:"volatile-idmap" description:"volatile.idmap.current: a file or the JSON itself." required:"yes"`
	UID           uint32 `long:"uid" description:"Namespace uid to translate." required:"yes"`
	UIDMap        string `long:"uid-map" description:"The guest's uid_map, a file or the map itself, to check against."`

	stdout io.Writer
}

// IdmapCheck is util idmap check INSTANCE_RECORD PREFLIGHT_RECORD (PRU-03).
type IdmapCheck struct {
	probe.GlobalOptions
	Args struct {
		Instance  string `positional-arg-name:"INSTANCE_RECORD" description:"Record of server instance show."`
		Preflight string `positional-arg-name:"PREFLIGHT_RECORD" description:"Record of guest preflight."`
	} `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// fileOrValue returns the content of the file named v, or v itself when no
// such file exists.
func fileOrValue(v string) (string, error) {
	data, err := os.ReadFile(v) //nolint:gosec // G304: a file the operator names
	switch {
	case err == nil:
		return string(data), nil
	case errors.Is(err, os.ErrNotExist) || strings.ContainsAny(v, "[\n"):
		return v, nil
	}
	return "", err
}

// Execute runs the command.
func (cmd *IdmapTranslate) Execute(args []string) error {
	raw, err := fileOrValue(cmd.VolatileIdmap)
	if err != nil {
		return exit.New(exit.Usage, fmt.Errorf("util idmap translate: %w", err))
	}
	recorded := []string{"--uid", fmt.Sprint(cmd.UID)}
	s, err := probe.NewSession(&cmd.GlobalOptions, cmd.stdout, "util", "idmap translate", recorded)
	if err != nil {
		return err
	}
	s.Observe("idmap.uid", cmd.UID)
	host, terr := translate(raw, cmd.UID)
	var r *policy.Refusal
	switch {
	case errors.As(terr, &r):
		s.Observe("idmap.refusal", map[string]string{"reason": string(r.Reason), "detail": r.Detail})
	case terr != nil:
		return s.Finish(terr)
	default:
		s.Observe("idmap.host_uid", host)
	}
	if cmd.UIDMap != "" {
		text, err := fileOrValue(cmd.UIDMap)
		if err != nil {
			return s.Finish(err)
		}
		proc, err := policy.ParseProcIDMap(text)
		if err != nil {
			return s.Finish(err)
		}
		lxdMap, _ := policy.ParseIDMap(raw)
		agrees, diff := policy.CompareUIDMaps(lxdMap, proc)
		if agrees {
			s.Observe("idmap.uid_map_agrees", true)
		} else {
			s.Finding("idmap.uid_map_agrees", false)
			s.Observe("idmap.uid_map_difference", diff)
		}
	}
	return s.Finish(nil)
}

func translate(raw string, uid uint32) (uint32, error) {
	entries, err := policy.ParseIDMap(raw)
	if err != nil {
		return 0, err
	}
	return policy.TranslateUID(entries, uid)
}

// Execute runs the command.
func (cmd *IdmapCheck) Execute(args []string) error {
	inst, err := readRecord(cmd.Args.Instance)
	if err != nil {
		return exit.New(exit.Usage, fmt.Errorf("util idmap check: %w", err))
	}
	pre, err := readRecord(cmd.Args.Preflight)
	if err != nil {
		return exit.New(exit.Usage, fmt.Errorf("util idmap check: %w", err))
	}
	// file names only: a full path may hold a user's name
	recorded := []string{filepath.Base(cmd.Args.Instance), filepath.Base(cmd.Args.Preflight)}
	s, err := probe.NewSession(&cmd.GlobalOptions, cmd.stdout, "util", "idmap check", recorded)
	if err != nil {
		return err
	}
	s.Observe("idmap.records", map[string]string{"instance": inst.ID, "preflight": pre.ID})
	return s.Finish(check(s, inst, pre))
}

func check(s *probe.Session, inst, pre *evidence.Record) error {
	var config map[string]string
	if err := observationAs(inst, "instance.config", &config); err != nil {
		return err
	}
	var procLines []struct{ Inside, Outside, Count int64 }
	if err := observationAs(pre, "idmap.uid_map", &procLines); err != nil {
		return err
	}
	proc := make([]policy.IDMapEntry, len(procLines))
	for i, l := range procLines {
		proc[i] = policy.IDMapEntry{Isuid: true, Nsid: l.Inside, Hostid: l.Outside, Maprange: l.Count}
	}
	identity := len(proc) == 1 && proc[0].Nsid == 0 && proc[0].Hostid == 0 && proc[0].Maprange == 1<<32-1
	var typ string
	_ = observationAs(inst, "instance.type", &typ) // absent in older records
	current, ok := config["volatile.idmap.current"]
	switch {
	case typ == "virtual-machine" || !ok:
		s.Observe("idmap.check", "not-applicable")
		s.Observe("idmap.detail", "a VM: no idmap")
		return nil
	case strings.TrimSpace(current) == "[]" && identity:
		s.Observe("idmap.check", "not-applicable")
		s.Observe("idmap.detail", "a privileged container: an empty map and the identity map")
		return nil
	}
	lxdMap, err := policy.ParseIDMap(current)
	if err != nil {
		s.Finding("idmap.check", "mismatch")
		s.Observe("idmap.detail", err.Error())
		return nil
	}
	if agrees, diff := policy.CompareUIDMaps(lxdMap, proc); agrees {
		s.Observe("idmap.check", "match")
	} else {
		s.Finding("idmap.check", "mismatch")
		s.Observe("idmap.detail", diff)
	}
	if host, err := policy.TranslateUID(lxdMap, 0); err == nil {
		s.Observe("idmap.root_host_uid", host)
	}
	return nil
}

// observationAs decodes the record's first observation key into v.
func observationAs(rec *evidence.Record, key string, v any) error {
	for _, o := range rec.Observations {
		if o.Key == key {
			data, err := json.Marshal(o.Value)
			if err != nil {
				return err
			}
			return json.Unmarshal(data, v)
		}
	}
	return fmt.Errorf("record %s has no observation %s", rec.ID, key)
}

// readRecord reads an evidence record the operator names; an empty one is
// evidence.ErrAborted.
func readRecord(path string) (*evidence.Record, error) { return evidence.ReadRecord(path) }
