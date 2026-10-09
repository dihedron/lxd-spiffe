package evidence

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Fixture is one recorded HTTP exchange (PRE-10): the request and the
// response, sanitized. A body that is JSON is kept as JSON, any other body
// as text.
type Fixture struct {
	// Name is the file name stem, set by LoadFixtures.
	Name     string          `json:"-"`
	Request  FixtureRequest  `json:"-"`
	Response FixtureResponse `json:"-"`
}

// FixtureRequest is the request half of a fixture.
type FixtureRequest struct {
	Method   string          `json:"method"`
	Path     string          `json:"path"`
	Query    string          `json:"query"`
	Header   http.Header     `json:"header"`
	Body     json.RawMessage `json:"body,omitempty"`
	BodyText string          `json:"body_text,omitempty"`
}

// FixtureResponse is the response half of a fixture.
type FixtureResponse struct {
	Status   int             `json:"status"`
	Header   http.Header     `json:"header"`
	Body     json.RawMessage `json:"body,omitempty"`
	BodyText string          `json:"body_text,omitempty"`
}

// FixtureWriter writes sanitized fixtures under Dir/<lxd-version>/ as
// NNNN-SS-<method>-<path>.request.json and .response.json, where NNNN is the
// record number and SS the exchange's sequence within the command.
type FixtureWriter struct {
	Dir       string
	Sanitizer *Sanitizer
}

// Write stores one exchange; body is the raw request or response body.
func (w *FixtureWriter) Write(version string, record, seq int, method, path, query string, reqHeader http.Header, reqBody []byte, status int, respHeader http.Header, respBody []byte) error {
	if version == "" {
		version = "unknown"
	}
	dir := filepath.Join(w.Dir, fileName(version))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating fixtures directory: %w", err)
	}
	slug := fileName(strings.Trim(path, "/"))
	if len(slug) > 80 {
		slug = slug[:80]
	}
	stem := fmt.Sprintf("%04d-%02d-%s-%s", record, seq, strings.ToLower(method), slug)

	req := FixtureRequest{Method: method, Path: w.Sanitizer.String(path), Query: w.Sanitizer.String(query), Header: w.Sanitizer.Headers(reqHeader)}
	req.Body, req.BodyText = w.body(reqBody)
	resp := FixtureResponse{Status: status, Header: w.Sanitizer.Headers(respHeader)}
	resp.Body, resp.BodyText = w.body(respBody)

	for suffix, v := range map[string]any{".request.json": req, ".response.json": resp} {
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding fixture: %w", err)
		}
		if err := createFile(filepath.Join(dir, stem+suffix), append(data, '\n')); err != nil {
			return fmt.Errorf("writing fixture: %w", err)
		}
	}
	return nil
}

func (w *FixtureWriter) body(data []byte) (json.RawMessage, string) {
	if len(data) == 0 {
		return nil, ""
	}
	if v, err := decodeJSON(data); err == nil {
		if out, err := json.Marshal(w.Sanitizer.Value(v)); err == nil {
			return out, ""
		}
	}
	return nil, w.Sanitizer.String(string(data))
}

// LoadFixtures reads every fixture pair under dir, sorted by file name.
func LoadFixtures(dir string) ([]Fixture, error) {
	var fixtures []Fixture
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		stem, ok := strings.CutSuffix(path, ".request.json")
		if d.IsDir() || !ok {
			return nil
		}
		f := Fixture{Name: filepath.Base(stem)}
		if err := readFixtureFile(path, &f.Request); err != nil {
			return err
		}
		if err := readFixtureFile(stem+".response.json", &f.Response); err != nil {
			return err
		}
		fixtures = append(fixtures, f)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("loading fixtures: %w", err)
	}
	slices.SortFunc(fixtures, func(a, b Fixture) int { return strings.Compare(a.Name, b.Name) })
	return fixtures, nil
}

func readFixtureFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// ResponseBody returns the body of a fixture response as bytes.
func (r *FixtureResponse) ResponseBody() []byte {
	if len(r.Body) > 0 {
		return r.Body
	}
	return []byte(r.BodyText)
}
