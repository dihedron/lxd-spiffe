package evidence

import (
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const testPEM = `-----BEGIN CERTIFICATE-----
MIIBszCCATigAwIBAgIQXK
-----END CERTIFICATE-----`

const testJWT = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzcGlyZSJ9.c2lnbmF0dXJl"

func TestSanitizerString(t *testing.T) {
	s := NewSanitizer(NewAliases())
	s.AddSecret("s3cr3t-t0ken-value")
	s.RedactName(KindInstance, "c1")
	s.RedactName(KindProject, "spire-nodes")

	tests := []struct {
		in, want string
	}{
		{"plain text", "plain text"},
		{"token s3cr3t-t0ken-value here", "token " + Redacted + " here"},
		{"cert " + testPEM + " end", "cert " + Redacted + " end"},
		{"truncated -----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg", "truncated " + Redacted},
		{"two " + testPEM + " and " + testPEM, "two " + Redacted + " and " + Redacted},
		{"Bearer " + testJWT, "Bearer " + Redacted},
		{"eth0 00:16:3E:AA:BB:CC up", "eth0 mac-1 up"},
		{"same 00:16:3e:aa:bb:cc again", "same mac-1 again"},
		{"dashes 00-16-3e-aa-bb-cc", "dashes mac-1"},
		{"other 00:16:3e:aa:bb:cd", "other mac-2"},
		{"c1", "instance-1"},
		{"/1.0/instances/c1?project=spire-nodes", "/1.0/instances/instance-1?project=project-1"},
		{"/1.0/instances/c1/files?path=/run/c1", "/1.0/instances/instance-1/files?path=/run/instance-1"},
		{"c10 and ac1 and c1x", "c10 and ac1 and c1x"},
		{"instance c1 is running", "instance c1 is running"},
		{Redacted, Redacted},
	}
	for _, tt := range tests {
		if got := s.String(tt.in); got != tt.want {
			t.Errorf("String(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func TestSanitizerValue(t *testing.T) {
	s := NewSanitizer(NewAliases())
	in := decode(t, `{
		"type": "sync",
		"metadata": {
			"name": "c1",
			"config": {
				"cloud-init.user-data": "#cloud-config\npassword: x",
				"environment.SECRET": "v",
				"user.note": "private",
				"user.lxd-probe.a": "1",
				"user.spire.challenge.0123": "abcd",
				"security.privileged": "false",
				"volatile.eth0.hwaddr": "00:16:3e:aa:bb:cc"
			},
			"environment": {"server_name": "lab1", "certificate": "`+strings.ReplaceAll(testPEM, "\n", `\n`)+`"},
			"count": 3,
			"list": ["00:16:3e:aa:bb:cc", true, null]
		}
	}`)
	want := decode(t, `{
		"type": "sync",
		"metadata": {
			"name": "c1",
			"config": {
				"cloud-init.user-data": "[redacted]",
				"environment.SECRET": "[redacted]",
				"user.note": "[redacted]",
				"user.lxd-probe.a": "1",
				"user.spire.challenge.0123": "abcd",
				"security.privileged": "false",
				"volatile.eth0.hwaddr": "mac-1"
			},
			"environment": {"server_name": "lab1", "certificate": "[redacted]"},
			"count": 3,
			"list": ["mac-1", true, null]
		}
	}`)
	got := s.Value(in)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Value() =\n%s\nwant\n%s", encode(t, got), encode(t, want))
	}
	if !reflect.DeepEqual(s.Value(got), got) {
		t.Errorf("Value() is not idempotent")
	}
	// the input is not modified
	if in.(map[string]any)["metadata"].(map[string]any)["config"].(map[string]any)["user.note"] != "private" {
		t.Errorf("Value() modified its input")
	}
}

func TestSanitizerHeaders(t *testing.T) {
	s := NewSanitizer(NewAliases())
	h := http.Header{
		"Authorization":       {"Bearer " + testJWT},
		"Proxy-Authorization": {"Basic eA=="},
		"Cookie":              {"session=1"},
		"Set-Cookie":          {"session=1"},
		"Etag":                {`"abc"`},
		"X-Lxd-Uid":           {"0"},
		"Location":            {"/1.0/operations/00:16:3e:aa:bb:cc"},
	}
	got := s.Headers(h)
	for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie"} {
		if _, ok := got[name]; ok {
			t.Errorf("header %s kept", name)
		}
	}
	if got.Get("Etag") != `"abc"` || got.Get("X-Lxd-Uid") != "0" {
		t.Errorf("headers = %v", got)
	}
	if got.Get("Location") != "/1.0/operations/mac-1" {
		t.Errorf("Location = %q", got.Get("Location"))
	}
	if h.Get("Authorization") == "" {
		t.Errorf("Headers() modified its input")
	}
}

func TestSanitizerJSON(t *testing.T) {
	s := NewSanitizer(NewAliases())
	out, err := s.JSON([]byte(`{"config":{"user.x":"secret"},"n":12345678901234567890}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"user.x": "[redacted]"`) || !strings.Contains(string(out), "12345678901234567890") {
		t.Errorf("JSON() = %s", out)
	}
	if _, err := s.JSON([]byte("not json")); err == nil {
		t.Errorf("JSON(not json) succeeded")
	}
}

var macPattern = regexp.MustCompile(`(?i)[0-9a-f]{2}([:-][0-9a-f]{2}){5}`)

var pemHeader = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]+-----`)

// FuzzSanitizeJSON checks PRT-02: injected secrets, PEM blocks and MAC
// addresses never survive, the output is valid JSON, and a second pass
// changes nothing.
func FuzzSanitizeJSON(f *testing.F) {
	f.Add([]byte(`{"a":"b"}`), "s3cr3tvalue")
	f.Add([]byte(`["00:16:3e:aa:bb:cc",{"config":{"user.k":"v"}}]`), "AAAAAAAAAAAA")
	f.Add([]byte(`"`+strings.ReplaceAll(testPEM, "\n", `\n`)+`"`), "tokentoken")
	f.Fuzz(func(t *testing.T, data []byte, secret string) {
		if !json.Valid(data) || !regexp.MustCompile(`^[A-Za-z0-9]{8,64}$`).MatchString(secret) || strings.Contains(Redacted, secret) {
			t.Skip()
		}
		var v any
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			t.Skip()
		}
		doc := map[string]any{
			"input":  v,
			"secret": "before " + secret + " after",
			"pem":    testPEM,
			"cut":    "-----BEGIN RSA PRIVATE KEY-----\nMIIE",
			"mac":    "aa:bb:cc:dd:ee:ff",
		}
		s := NewSanitizer(NewAliases())
		s.AddSecret(secret)
		out := s.Value(doc)
		raw, err := json.Marshal(out)
		if err != nil || !json.Valid(raw) {
			t.Fatalf("output is not valid JSON: %v", err)
		}
		walkStrings(out, func(str string) {
			if strings.Contains(str, secret) {
				t.Errorf("secret survived in %q", str)
			}
			if pemHeader.MatchString(str) {
				t.Errorf("PEM survived in %q", str)
			}
			if macPattern.MatchString(str) {
				t.Errorf("MAC survived in %q", str)
			}
		})
		if !reflect.DeepEqual(s.Value(out), out) {
			t.Errorf("second pass changed the output")
		}
	})
}

func walkStrings(v any, fn func(string)) {
	switch v := v.(type) {
	case string:
		fn(v)
	case map[string]any:
		for _, e := range v {
			walkStrings(e, fn)
		}
	case []any:
		for _, e := range v {
			walkStrings(e, fn)
		}
	}
}

func decode(t *testing.T, s string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

func encode(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
