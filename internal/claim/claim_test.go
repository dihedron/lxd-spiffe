package claim

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFRS2InstanceName(t *testing.T) {
	for name, want := range map[string]bool{
		"c1":                    true,
		"v-alpine":              true,
		"C1":                    true,
		"a":                     true,
		strings.Repeat("a", 63): true,
		strings.Repeat("a", 64): false,
		"":                      false,
		"-c1":                   false,
		"c1-":                   false,
		"123":                   false,
		"1a":                    true,
		"c_1":                   false,
		"c.1":                   false,
		"c 1":                   false,
		"cé":                    false,
	} {
		if got := ValidInstanceName(name) == nil; got != want {
			t.Errorf("ValidInstanceName(%q) valid=%v, want %v", name, got, want)
		}
	}
}

func TestFRS2Project(t *testing.T) {
	for name, want := range map[string]bool{
		"default":               true,
		"team.a":                true,
		"Prod+EU":               true,
		"a:b":                   true,
		"-x-":                   true,
		"123":                   true,
		strings.Repeat("p", 63): true,
		strings.Repeat("p", 64): false,
		"":                      false,
		"*":                     false,
		"a*":                    true,
		".":                     false,
		"..":                    false,
		"...":                   true,
		"a/b":                   false,
		`a\b`:                   false,
		"a b":                   false,
		"a_b":                   false,
		"a'b":                   false,
		`a"b`:                   false,
		"a\tb":                  false,
		"a\x7fb":                false,
		"é":                     false,
	} {
		if got := ValidProject(name) == nil; got != want {
			t.Errorf("ValidProject(%q) valid=%v, want %v", name, got, want)
		}
	}
}

func TestCloudInitID(t *testing.T) {
	for id, want := range map[string]bool{
		"01405cc4-dd54-4c91-86ba-9771a1fe64a5":  true,
		"01405CC4-DD54-4C91-86BA-9771A1FE64A5":  false,
		"01405cc4dd544c9186ba9771a1fe64a5":      false,
		"01405cc4-dd54-4c91-86ba-9771a1fe64a":   false,
		"01405cc4-dd54-4c91-86ba-9771a1fe64a5x": false,
		"01405cc4-dd54-4c91-86ba_9771a1fe64a5":  false,
		"g1405cc4-dd54-4c91-86ba-9771a1fe64a5":  false,
		"":                                      false,
	} {
		if got := ValidCloudInitID(id) == nil; got != want {
			t.Errorf("ValidCloudInitID(%q) valid=%v, want %v", id, got, want)
		}
	}
}

func TestParseMetaData(t *testing.T) {
	// recorded from a v-alpine VM on LXD 6.9 (run 20261012T143252Z-s0-2-lxd6-v-alpine)
	md, err := ParseMetaData("instance-id: 01405cc4-dd54-4c91-86ba-9771a1fe64a5\nlocal-hostname: v-alpine\n")
	if err != nil || md.InstanceID != "01405cc4-dd54-4c91-86ba-9771a1fe64a5" || md.LocalHostname != "v-alpine" {
		t.Fatalf("%+v %v", md, err)
	}
	if md, err := ParseMetaData("#cloud-config\nlocal-hostname: c1\r\nother: x\n\n"); err != nil || md.LocalHostname != "c1" || md.InstanceID != "" {
		t.Errorf("unknown keys, comments, CRLF and blank lines: %+v %v", md, err)
	}
	for name, text := range map[string]string{
		"duplicate key": "local-hostname: a\nlocal-hostname: b\n",
		"no separator":  "local-hostname\n",
		"NUL byte":      "local-hostname: c\x001\n",
	} {
		if _, err := ParseMetaData(text); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// fakeMetaData returns a fixed meta-data or error, counting the calls.
type fakeMetaData struct {
	md    MetaData
	err   error
	calls int
}

func (f *fakeMetaData) MetaData(context.Context) (MetaData, error) {
	f.calls++
	return f.md, f.err
}

func hostname(name string, err error) func() (string, error) {
	return func() (string, error) { return name, err }
}

// TestFRA2Precedence: per field, configuration, then devLXD meta-data, then
// the OS hostname (name only); the project comes from configuration only.
func TestFRA2Precedence(t *testing.T) {
	const uuid = "01405cc4-dd54-4c91-86ba-9771a1fe64a5"
	devlxd := &fakeMetaData{md: MetaData{InstanceID: uuid, LocalHostname: "from-devlxd"}}
	for name, tt := range map[string]struct {
		cfg     Config
		src     Sources
		want    Claim
		wantErr bool
	}{
		"configuration first": {
			cfg:  Config{Project: "team.a", InstanceName: "from-config"},
			src:  Sources{DevLXD: devlxd, Hostname: hostname("from-host", nil)},
			want: Claim{Project: "team.a", InstanceName: "from-config", CloudInitInstanceID: uuid, NameSource: SourceConfigured},
		},
		"devLXD second": {
			src:  Sources{DevLXD: devlxd, Hostname: hostname("from-host", nil)},
			want: Claim{InstanceName: "from-devlxd", CloudInitInstanceID: uuid, NameSource: SourceDevLXDMetaData},
		},
		"hostname last": {
			src:  Sources{Hostname: hostname("from-host", nil)},
			want: Claim{InstanceName: "from-host", NameSource: SourceHostname},
		},
		"devLXD failing under auto falls through": {
			src:  Sources{DevLXD: &fakeMetaData{err: errors.New("no socket")}, Hostname: hostname("from-host", nil)},
			want: Claim{InstanceName: "from-host", NameSource: SourceHostname},
		},
		"devLXD failing when required": {
			cfg:     Config{DevLXDRequired: true},
			src:     Sources{DevLXD: &fakeMetaData{err: errors.New("no socket")}, Hostname: hostname("from-host", nil)},
			wantErr: true,
		},
		"devLXD required but absent": {
			cfg:     Config{DevLXDRequired: true},
			src:     Sources{Hostname: hostname("from-host", nil)},
			wantErr: true,
		},
		"devLXD without a hostname": {
			src:  Sources{DevLXD: &fakeMetaData{md: MetaData{InstanceID: uuid}}, Hostname: hostname("from-host", nil)},
			want: Claim{InstanceName: "from-host", CloudInitInstanceID: uuid, NameSource: SourceHostname},
		},
		"invalid devLXD name is refused, not skipped": {
			src:     Sources{DevLXD: &fakeMetaData{md: MetaData{LocalHostname: "bad_name"}}, Hostname: hostname("from-host", nil)},
			wantErr: true,
		},
		"invalid configured project": {
			cfg:     Config{Project: "a b", InstanceName: "c1"},
			wantErr: true,
		},
		"invalid cloud-init id": {
			src:     Sources{DevLXD: &fakeMetaData{md: MetaData{InstanceID: "not-a-uuid", LocalHostname: "c1"}}},
			wantErr: true,
		},
		"no source at all": {
			wantErr: true,
		},
		"hostname failing": {
			src:     Sources{Hostname: hostname("", errors.New("uname"))},
			wantErr: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Collect(context.Background(), tt.cfg, tt.src)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("accepted %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("claim %+v, want %+v", got, tt.want)
			}
		})
	}
}
