package selectors

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/canonical/lxd/shared/api"
)

// FuzzValueRule: whatever LXD holds in a listed key, every emitted value
// keeps the value rule and every other value is reported as omitted.
func FuzzValueRule(f *testing.F) {
	f.Add("payments")
	f.Add("a\nb")
	f.Add("\xff")
	f.Add(strings.Repeat("x", 300))
	f.Fuzz(func(t *testing.T, value string) {
		inst := &api.Instance{Name: "c1", ExpandedConfig: map[string]string{"user.k": value}}
		got, omitted, err := Build(Config{Enable: []string{"name"}, UserKeys: []string{"k"}}, Input{Instance: inst})
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range got {
			if !utf8.ValidString(v) || strings.ContainsFunc(v, unicode.IsControl) || len(v) > MaxValue {
				t.Fatalf("emitted %q", v)
			}
		}
		if value != "" && len(got)+len(omitted) != 2 {
			t.Fatalf("value %q: emitted %q, omitted %v", value, got, omitted)
		}
	})
}
