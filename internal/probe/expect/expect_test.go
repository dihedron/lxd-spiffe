package expect

import (
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
)

func keys(obs []evidence.Observation) []string {
	out := []string{}
	for _, o := range obs {
		out = append(out, o.Key)
	}
	return out
}

func TestCheck(t *testing.T) {
	exp := Expectation{
		Name: "test", Status: []int{200}, Headers: []string{"ETag"}, Envelope: Sync,
		Fields: []string{"name", "environment.server_version", "location"},
	}
	etag := http.Header{"Etag": {"abc"}}
	tests := []struct {
		name   string
		status int
		header http.Header
		body   string
		want   []string
	}{
		{"conforms", 200, etag, `{"type":"sync","metadata":{"name":"c1","environment":{"server_version":"6.5"},"location":null}}`, []string{}},
		{"status", 404, etag, `{"type":"sync","metadata":{"name":"c1","environment":{"server_version":"6.5"},"location":""}}`, []string{"expectation.status"}},
		{"header", 200, http.Header{}, `{"type":"sync","metadata":{"name":"c1","environment":{"server_version":"6.5"},"location":""}}`, []string{"expectation.header.ETag"}},
		{"envelope", 200, etag, `{"type":"async","metadata":{"name":"c1","environment":{"server_version":"6.5"},"location":""}}`, []string{"expectation.body.type"}},
		{"fields", 200, etag, `{"type":"sync","metadata":{"environment":{}}}`, []string{"expectation.body.name", "expectation.body.environment.server_version", "expectation.body.location"}},
		{"nested not an object", 200, etag, `{"type":"sync","metadata":{"name":"c1","environment":"x","location":""}}`, []string{"expectation.body.environment.server_version"}},
		{"not JSON", 502, http.Header{}, `<html>bad gateway</html>`, []string{"expectation.status", "expectation.header.ETag", "expectation.body"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exp.Check(tt.status, tt.header, []byte(tt.body))
			if !reflect.DeepEqual(keys(got), tt.want) {
				t.Errorf("deviations = %v; want %v", keys(got), tt.want)
			}
			for _, o := range got {
				if d, ok := o.Value.(Deviation); !ok || d.Expectation != "test" {
					t.Errorf("%s: value %#v does not name the expectation", o.Key, o.Value)
				}
			}
		})
	}
}

func TestCheckStatusValue(t *testing.T) {
	got := Server.Check(403, http.Header{}, []byte(`{"type":"error","error":"not authorized","error_code":403}`))
	want := evidence.Observation{Key: "expectation.status", Value: Deviation{Expectation: "server", Want: []int{200}, Got: 403}}
	if len(got) == 0 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("first deviation = %#v; want %#v", got, want)
	}
}

func TestDenied(t *testing.T) {
	denied := Instance.Denied()
	if denied.Name != "instance+denied" || !slices.Equal(denied.Status, []int{403}) || denied.Envelope != Error || len(denied.Headers) != 0 || len(denied.Fields) != 0 {
		t.Fatalf("Denied() = %+v", denied)
	}
	if got := denied.Check(403, http.Header{}, []byte(`{"type":"error","error":"not authorized","error_code":403}`)); len(got) != 0 {
		t.Errorf("403 deviates: %v", keys(got))
	}
	if got := denied.Check(200, http.Header{"Etag": {"x"}}, []byte(`{"type":"sync","metadata":{}}`)); !slices.Equal(keys(got), []string{"expectation.status", "expectation.body.type"}) {
		t.Errorf("200 under --expect-denied: %v", keys(got))
	}
}

func TestHidden(t *testing.T) {
	hidden := Instance.Hidden()
	if hidden.Name != "instance+hidden" || !slices.Equal(hidden.Status, []int{404}) || hidden.Envelope != Error || len(hidden.Headers) != 0 || len(hidden.Fields) != 0 {
		t.Fatalf("Hidden() = %+v", hidden)
	}
	if got := hidden.Check(404, http.Header{}, []byte(`{"type":"error","error":"Not Found","error_code":404}`)); len(got) != 0 {
		t.Errorf("404 deviates: %v", keys(got))
	}
	if got := hidden.Check(403, http.Header{}, []byte(`{"type":"error","error":"Forbidden","error_code":403}`)); !slices.Equal(keys(got), []string{"expectation.status"}) {
		t.Errorf("403 under --expect-hidden: %v", keys(got))
	}
	if got := hidden.Check(200, http.Header{"Etag": {"x"}}, []byte(`{"type":"sync","metadata":{}}`)); !slices.Equal(keys(got), []string{"expectation.status", "expectation.body.type"}) {
		t.Errorf("200 under --expect-hidden: %v", keys(got))
	}
}

// TestTable checks that every expectation is complete and named once.
func TestTable(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Table {
		if e.Name == "" || e.Method == "" || e.Path == "" || len(e.Status) == 0 || e.Envelope == "" {
			t.Errorf("incomplete expectation %+v", e)
		}
		if seen[e.Name] {
			t.Errorf("duplicate expectation %q", e.Name)
		}
		seen[e.Name] = true
	}
	if !slices.Contains(InstanceFull.Fields, "state") || slices.Contains(Instance.Fields, "state") {
		t.Errorf("only instance-full expects the state")
	}
}

// TestDevLXD checks the devLXD expectations: either format of PRF-10, the
// fields at the top level of a plain body or in an envelope's metadata.
func TestDevLXD(t *testing.T) {
	const plain = `{"api_version":"1.0","instance_type":"container","location":"lxd6","state":"Started","auth":"untrusted"}`
	const envelope = `{"type":"sync","status":"Success","status_code":200,"metadata":` + plain + `}`
	for name, tt := range map[string]struct {
		exp    Expectation
		status int
		body   string
		want   []string
	}{
		"plain":               {DevLXDServer, 200, plain, []string{}},
		"envelope":            {DevLXDServer, 200, envelope, []string{}},
		"missing field":       {DevLXDServer, 200, `{"api_version":"1.0","instance_type":"vm","state":"Started"}`, []string{"expectation.body.location"}},
		"missing in envelope": {DevLXDServer, 200, `{"type":"sync","status_code":200,"metadata":{"api_version":"1.0"}}`, []string{"expectation.body.instance_type", "expectation.body.location", "expectation.body.state"}},
		"error envelope":      {DevLXDServer, 200, `{"type":"error","error":"x","error_code":500,"metadata":null}`, []string{"expectation.body.type", "expectation.body.api_version", "expectation.body.instance_type", "expectation.body.location", "expectation.body.state"}},
		"text where JSON":     {DevLXDConfig, 200, `forbidden`, []string{"expectation.body"}},
		"JSON list":           {DevLXDConfig, 200, `["/1.0/config/user.a"]`, []string{}},
		"text value":          {DevLXDConfigKey, 200, `some value`, []string{}},
		"text meta-data":      {DevLXDMetadata, 200, "#cloud-config\ninstance-id: x\n", []string{}},
		"status":              {DevLXDConfigKey, 403, `Forbidden`, []string{"expectation.status"}},
	} {
		if got := keys(tt.exp.Check(tt.status, http.Header{}, []byte(tt.body))); !slices.Equal(got, tt.want) {
			t.Errorf("%s: deviations %v; want %v", name, got, tt.want)
		}
	}
	for _, e := range []Expectation{DevLXDRoot, DevLXDServer, DevLXDConfig, DevLXDConfigKey, DevLXDMetadata, DevLXDDevices} {
		inTable := slices.ContainsFunc(Table, func(x Expectation) bool { return x.Name == e.Name })
		if e.Envelope != DevLXD || !inTable {
			t.Errorf("%s: envelope %q, in Table %v", e.Name, e.Envelope, inTable)
		}
	}
}
