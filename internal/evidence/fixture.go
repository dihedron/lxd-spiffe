package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
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
	req.Body, req.BodyText = sanitizeBody(w.Sanitizer, reqBody)
	body := RedactConfigBody(path, status, respBody)
	if filePath, ok := FilesAPIPath(path, query); ok {
		body = FileBody(filePath, respHeader, body, true)
	}
	resp := sanitizeResponse(w.Sanitizer, status, respHeader, body)

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

// labDirPrefix is the guest directory whose files the lab makes; their
// content may stay in fixtures (PRS-35).
const labDirPrefix = "/run/lxd-probe/"

// FilesAPIPath returns the file path of a files-API request
// (/1.0/instances/<name>/files?path=...), and whether it is one.
func FilesAPIPath(apiPath, query string) (string, bool) {
	rest, ok := strings.CutPrefix(apiPath, "/1.0/instances/")
	if !ok || !strings.HasSuffix(rest, "/files") || strings.Count(rest, "/") != 1 {
		return "", false
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return "", true // a files-API request all the same: its body is content
	}
	return q.Get("path"), true
}

// FileBody returns a files-API response body as evidence may hold it
// (probe spec PRS-35): a file's content is replaced by its size and
// SHA-256, but for a directory listing or a symlink (metadata, by their
// X-LXD-type header) and, when keepLab is set (fixtures), a file under the
// lab directory, given as a clean path. An unknown type counts as content.
func FileBody(filePath string, header http.Header, body []byte, keepLab bool) []byte {
	switch header.Get("X-LXD-type") {
	case "directory", "symlink":
		return body
	}
	if keepLab && path.Clean(filePath) == filePath && strings.HasPrefix(filePath, labDirPrefix) {
		return body
	}
	sum := sha256.Sum256(body)
	return []byte(fmt.Sprintf("[content omitted: %d bytes, sha256 %s]", len(body), hex.EncodeToString(sum[:])))
}

// sanitizeResponse returns a response as stored in fixtures and artifacts.
func sanitizeResponse(s *Sanitizer, status int, header http.Header, body []byte) FixtureResponse {
	resp := FixtureResponse{Status: status, Header: s.Headers(header)}
	resp.Body, resp.BodyText = sanitizeBody(s, body)
	return resp
}

// sanitizeBody returns a sanitized JSON body, or a sanitized text body when
// data is not JSON.
func sanitizeBody(s *Sanitizer, data []byte) (json.RawMessage, string) {
	if len(data) == 0 {
		return nil, ""
	}
	if v, err := decodeJSON(data); err == nil {
		if out, err := json.Marshal(s.Value(v)); err == nil {
			return out, ""
		}
	}
	return nil, s.String(string(data))
}

// LoadFixtures reads every fixture pair under dir, sorted by file name.
// devLXD fixtures, filed under a devlxd/ directory below dir (the lab's
// S0-2), are not the server's: they load when that directory is given.
func LoadFixtures(dir string) ([]Fixture, error) {
	var fixtures []Fixture
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "devlxd" && path != dir {
			return filepath.SkipDir
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
	data, err := os.ReadFile(path) //nolint:gosec // G304: a fixture under the directory the caller loads
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
