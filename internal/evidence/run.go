package evidence

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Environments of lxd-probe; each has its own run directory (PRE-07).
var Environments = []string{"guest", "server", "util"}

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// RunInfo is the content of run.json.
type RunInfo struct {
	// Schema is the format version.
	Schema int `json:"schema"`
	// RunID identifies the run.
	RunID string `json:"run_id"`
	// Env is the environment of this run directory.
	Env string `json:"env"`
	// Created is when the run directory was created, in UTC.
	Created time.Time `json:"created"`
	// ToolVersion is the version of lxd-probe.
	ToolVersion string `json:"tool_version"`
	// GitCommit is the commit lxd-probe was built from.
	GitCommit string `json:"git_commit"`
	// Host is the host name of the machine running the probe.
	Host string `json:"host"`
	// Endpoint is the LXD endpoint of a server run (never credentials).
	Endpoint string `json:"endpoint,omitempty"`
}

// Run is the run directory of one environment: evidence/<run_id>/<env>/.
type Run struct {
	// Dir is the run directory.
	Dir string
	// ID is the run identifier.
	ID string
	// Env is the environment.
	Env string
	// Sanitizer sanitizes everything written to the run; its aliases are
	// kept in the run's aliases.json.
	Sanitizer *Sanitizer
	// ToolVersion is copied into every record.
	ToolVersion string
}

// NewRunID returns a run identifier: the UTC time and 4 random hex digits.
func NewRunID(now time.Time) (string, error) {
	b := make([]byte, 2)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating run id: %w", err)
	}
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b), nil
}

// Open creates, or reopens, the run directory root/runID/env with its
// records and artifacts directories (mode 0700), and writes run.json if the
// run directory is new. info supplies the descriptive fields of run.json.
func Open(root, runID, env string, info RunInfo) (*Run, error) {
	if !runIDPattern.MatchString(runID) {
		return nil, fmt.Errorf("invalid run id %q", runID)
	}
	if !slices.Contains(Environments, env) {
		return nil, fmt.Errorf("invalid environment %q", env)
	}
	dir := filepath.Join(root, runID, env)
	for _, d := range []string{dir, filepath.Join(dir, "records"), filepath.Join(dir, "artifacts")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("creating run directory: %w", err)
		}
	}
	aliases, err := OpenAliases(filepath.Join(dir, "aliases.json"))
	if err != nil {
		return nil, err
	}
	run := &Run{Dir: dir, ID: runID, Env: env, Sanitizer: NewSanitizer(aliases), ToolVersion: info.ToolVersion}

	info.Schema = Schema
	info.RunID = runID
	info.Env = env
	if info.Created.IsZero() {
		info.Created = time.Now().UTC()
	}
	info.Endpoint = run.Sanitizer.String(info.Endpoint)
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding run.json: %w", err)
	}
	if err := createFile(filepath.Join(dir, "run.json"), append(data, '\n')); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("writing run.json: %w", err)
	}
	return run, nil
}

// Entry is a record being written: its number is reserved, artifacts can be
// added, and Commit writes the record.
type Entry struct {
	// Number is the record number, NNNN.
	Number    int
	run       *Run
	file      *os.File
	artifacts []string
	// reservation is records/NNNN, removed when the entry ends
	reservation string
}

// Begin reserves the next record number by creating records/NNNN with
// O_EXCL, retrying on collision, then creates records/NNNN-<verb>.json
// (PRE-07). The number is reserved on its own: a reservation through the
// record's name would let two processes with different verbs share a
// number, and so the names of their artifacts. The reservation is removed
// when the entry ends; one left by a crashed process only skips a number.
func (r *Run) Begin(verb string) (*Entry, error) {
	records := filepath.Join(r.Dir, "records")
	n, err := nextNumber(records)
	if err != nil {
		return nil, err
	}
	name := fileName(verb)
	for ; ; n++ {
		reservation := filepath.Join(records, fmt.Sprintf("%04d", n))
		if err := createFile(reservation, nil); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return nil, fmt.Errorf("reserving record: %w", err)
		}
		// a process that held this number removes its reservation only
		// once its record exists, so a record here means the number is taken
		if taken, _ := filepath.Glob(filepath.Join(records, fmt.Sprintf("%04d-*.json", n))); len(taken) > 0 {
			os.Remove(reservation) //nolint:errcheck // best effort
			continue
		}
		//nolint:gosec // G304: a new record in the run's records directory, created with O_EXCL
		f, err := os.OpenFile(filepath.Join(records, fmt.Sprintf("%04d-%s.json", n, name)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			os.Remove(reservation) //nolint:errcheck // best effort
			if errors.Is(err, fs.ErrExist) {
				continue // a record of an older probe, which reserved by name
			}
			return nil, fmt.Errorf("reserving record: %w", err)
		}
		return &Entry{Number: n, run: r, file: f, reservation: reservation}, nil
	}
}

// nextNumber returns one plus the highest record number in dir.
func nextNumber(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("reading records: %w", err)
	}
	highest := 0
	for _, e := range entries {
		prefix, _, _ := strings.Cut(e.Name(), "-")
		if n, err := strconv.Atoi(prefix); err == nil && n > highest {
			highest = n
		}
	}
	return highest + 1, nil
}

// fileName turns a verb or artifact name into a file name component.
func fileName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return '-'
	}, s)
}

// ArtifactJSON sanitizes a JSON document and stores it as
// artifacts/NNNN-<name>.json; it returns the path relative to the run
// directory.
func (e *Entry) ArtifactJSON(name string, data []byte) (string, error) {
	clean, err := e.run.Sanitizer.JSON(data)
	if err != nil {
		return "", fmt.Errorf("artifact %s: %w", name, err)
	}
	return e.artifact(name+".json", clean)
}

// ArtifactText sanitizes text and stores it as artifacts/NNNN-<name>.txt.
func (e *Entry) ArtifactText(name, text string) (string, error) {
	return e.artifact(name+".txt", []byte(e.run.Sanitizer.String(text)))
}

// ArtifactResponse stores an HTTP response, sanitized, as
// artifacts/NNNN-<name>.json in the format of a fixture response: status,
// headers, and the body as JSON or as text (PRE-08).
func (e *Entry) ArtifactResponse(name string, status int, header http.Header, body []byte) (string, error) {
	data, err := json.MarshalIndent(sanitizeResponse(e.run.Sanitizer, status, header, body), "", "  ")
	if err != nil {
		return "", fmt.Errorf("artifact %s: %w", name, err)
	}
	return e.artifact(name+".json", append(data, '\n'))
}

func (e *Entry) artifact(name string, data []byte) (string, error) {
	rel := filepath.Join("artifacts", fmt.Sprintf("%04d-%s", e.Number, fileName(name)))
	if err := createFile(filepath.Join(e.run.Dir, rel), data); err != nil {
		return "", fmt.Errorf("writing artifact: %w", err)
	}
	e.artifacts = append(e.artifacts, rel)
	return rel, nil
}

// Commit completes rec (schema, id, environment, tool version, artifacts),
// sanitizes its free-form fields and writes it.
func (e *Entry) Commit(rec *Record) (err error) {
	defer func() {
		if err != nil {
			e.file.Close() //nolint:errcheck // the caller aborts the entry
		}
	}()
	s := e.run.Sanitizer
	rec.Schema = Schema
	rec.ID = fmt.Sprintf("%s/%04d", e.run.Env, e.Number)
	rec.Env = e.run.Env
	if rec.ToolVersion == "" {
		rec.ToolVersion = e.run.ToolVersion
	}
	rec.Time = rec.Time.UTC()
	rec.Raw = append(rec.Raw, e.artifacts...)
	if rec.Args == nil {
		rec.Args = []string{}
	}
	for i, arg := range rec.Args {
		rec.Args[i] = s.String(arg)
	}
	rec.Gate = s.String(rec.Gate)
	rec.Cell = s.String(rec.Cell)
	if rec.Observations == nil {
		rec.Observations = []Observation{}
	}
	for i, o := range rec.Observations {
		v, err := roundTrip(o.Value)
		if err != nil {
			return fmt.Errorf("observation %s: %w", o.Key, err)
		}
		rec.Observations[i] = Observation{Key: s.String(o.Key), Value: s.Value(v)}
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding record: %w", err)
	}
	if _, err := e.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing record: %w", err)
	}
	if e.reservation != "" {
		os.Remove(e.reservation) //nolint:errcheck // the record holds the number now
	}
	return e.file.Close()
}

// Path returns the path of the record file.
func (e *Entry) Path() string {
	return e.file.Name()
}

// Abort releases the reserved record number by removing its file.
func (e *Entry) Abort() {
	e.file.Close()           //nolint:errcheck // best effort
	os.Remove(e.file.Name()) //nolint:errcheck // best effort
	if e.reservation != "" {
		os.Remove(e.reservation) //nolint:errcheck // best effort
	}
}

// roundTrip turns any value into its generic JSON form, so that the
// sanitizer sees every string of a struct.
func roundTrip(v any) (any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return decodeJSON(data)
}

// createFile creates path with O_EXCL and mode 0600.
func createFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: a new file in the run directory, created with O_EXCL
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close() //nolint:errcheck // the write error is the one to report
		return err
	}
	return f.Close()
}
