package claim

import (
	"strings"
	"testing"
)

// FuzzParseMetaData: devLXD's meta-data is untrusted text (SEC-11, T18):
// parsing never panics, and accepted values are single trimmed lines.
func FuzzParseMetaData(f *testing.F) {
	// recorded from a v-alpine VM on LXD 6.9 (run 20261012T143252Z-s0-2-lxd6-v-alpine)
	f.Add("instance-id: 01405cc4-dd54-4c91-86ba-9771a1fe64a5\nlocal-hostname: v-alpine\n")
	f.Add("#cloud-config\r\nlocal-hostname: c1\r\n\n")
	f.Add("local-hostname: a\nlocal-hostname: b\n")
	f.Fuzz(func(t *testing.T, text string) {
		md, err := ParseMetaData(text)
		if err != nil {
			return
		}
		for _, v := range []string{md.InstanceID, md.LocalHostname} {
			if strings.ContainsAny(v, "\n\x00") || strings.TrimSpace(v) != v {
				t.Fatalf("value %q", v)
			}
		}
	})
}

// FuzzNames: every accepted name is printable ASCII within the bound, and
// every valid instance name is a valid project name too.
func FuzzNames(f *testing.F) {
	for _, s := range []string{"c1", "team.a", "-x-", "123", "a_b", "é", strings.Repeat("a", 64)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		instanceOK, projectOK := ValidInstanceName(s) == nil, ValidProject(s) == nil
		if instanceOK && !projectOK {
			t.Fatalf("%q is an instance name but not a project name", s)
		}
		if instanceOK || projectOK {
			for _, c := range []byte(s) {
				if c < '!' || c > '~' {
					t.Fatalf("accepted %q with byte %q", s, c)
				}
			}
			if len(s) > 63 {
				t.Fatalf("accepted %d bytes", len(s))
			}
		}
	})
}
