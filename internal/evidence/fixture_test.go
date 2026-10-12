package evidence

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadFixturesSkipsDevLXD checks that the devLXD fixtures filed under a
// version's devlxd/ are not replayed as the server's (lab S0-2), and load
// when their own directory is given.
func TestLoadFixturesSkipsDevLXD(t *testing.T) {
	dir := t.TempDir()
	version := filepath.Join(dir, "6.9")
	server := &FixtureWriter{Dir: dir, Sanitizer: NewSanitizer(NewAliases())}
	if err := server.Write("6.9", 1, 1, http.MethodGet, "/1.0", "", http.Header{}, nil, 200, http.Header{}, []byte(`{"type":"sync"}`)); err != nil {
		t.Fatal(err)
	}
	guest := &FixtureWriter{Dir: filepath.Join(version, "devlxd"), Sanitizer: NewSanitizer(NewAliases())}
	if err := guest.Write("c1", 1, 1, http.MethodGet, "/1.0", "", http.Header{}, nil, 200, http.Header{}, []byte(`{"api_version":"1.0"}`)); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFixtures(version)
	if err != nil || len(got) != 1 || !strings.Contains(string(got[0].Response.Body), `"type": "sync"`) {
		t.Errorf("server fixtures: %+v, %v", got, err)
	}
	got, err = LoadFixtures(filepath.Join(version, "devlxd", "c1"))
	if err != nil || len(got) != 1 || !strings.Contains(string(got[0].Response.Body), `"api_version": "1.0"`) {
		t.Errorf("devLXD fixtures: %+v, %v", got, err)
	}
}

// TestFileBody checks that a file's content is replaced by its size and
// hash (probe spec PRS-35), but for directories, symlinks and, in
// fixtures, clean paths under the lab directory.
func TestFileBody(t *testing.T) {
	content := []byte("s3cret")
	header := func(typ string) http.Header { return http.Header{"X-Lxd-Type": {typ}} }
	placeholder := "[content omitted: 6 bytes, sha256 "
	for name, tt := range map[string]struct {
		path    string
		header  http.Header
		keepLab bool
		kept    bool
	}{
		"file":                      {"/etc/shadow", header("file"), true, false},
		"unknown type":              {"/etc/shadow", http.Header{}, true, false},
		"directory":                 {"/etc", header("directory"), false, true},
		"symlink":                   {"/etc/localtime", header("symlink"), false, true},
		"lab file, fixture":         {"/run/lxd-probe/proof", header("file"), true, true},
		"lab file, artifact":        {"/run/lxd-probe/proof", header("file"), false, false},
		"lab path escaping, dotdot": {"/run/lxd-probe/../../etc/shadow", header("file"), true, false},
		"lab directory itself":      {"/run/lxd-probe", header("file"), true, false},
	} {
		got := string(FileBody(tt.path, tt.header, content, tt.keepLab))
		if tt.kept && got != "s3cret" || !tt.kept && !strings.HasPrefix(got, placeholder) {
			t.Errorf("%s: %q", name, got)
		}
	}

	// fixtures of the files API go through it
	dir := t.TempDir()
	w := &FixtureWriter{Dir: dir, Sanitizer: NewSanitizer(NewAliases())}
	if err := w.Write("6.9", 1, 1, http.MethodGet, "/1.0/instances/c1/files", "path=%2Fetc%2Fshadow&project=default", http.Header{}, nil, 200, header("file"), content); err != nil {
		t.Fatal(err)
	}
	if err := w.Write("6.9", 1, 2, http.MethodGet, "/1.0/instances/c1/files", "path=%2Frun%2Flxd-probe%2Fproof&project=default", http.Header{}, nil, 200, header("file"), []byte("nonce")); err != nil {
		t.Fatal(err)
	}
	fixtures, err := LoadFixtures(dir)
	if err != nil || len(fixtures) != 2 {
		t.Fatalf("fixtures: %v, %v", fixtures, err)
	}
	if body := fixtures[0].Response.BodyText; !strings.HasPrefix(body, placeholder) {
		t.Errorf("/etc/shadow fixture body %q", body)
	}
	if body := fixtures[1].Response.BodyText; body != "nonce" {
		t.Errorf("lab proof fixture body %q", body)
	}
}
