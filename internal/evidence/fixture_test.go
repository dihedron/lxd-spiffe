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
