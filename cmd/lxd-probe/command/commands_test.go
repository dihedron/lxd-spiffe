package command

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/probe/guest"
	"github.com/dihedron/lxd-spiffe/internal/probe/server"
	"github.com/dihedron/lxd-spiffe/internal/probe/util"
	"github.com/jessevdk/go-flags"
)

// parse parses args into a fresh command tree and returns the leaf command
// that would run, without running it.
func parse(t *testing.T, args ...string) (flags.Commander, error) {
	t.Helper()
	options := Commands{}
	parser := flags.NewParser(&options, flags.HelpFlag|flags.PassDoubleDash)
	var leaf flags.Commander
	parser.CommandHandler = func(command flags.Commander, _ []string) error {
		leaf = command
		return nil
	}
	_, err := parser.ParseArgs(args)
	return leaf, err
}

func mustParse(t *testing.T, args ...string) flags.Commander {
	t.Helper()
	leaf, err := parse(t, args...)
	if err != nil {
		t.Fatalf("parse %q: %v", args, err)
	}
	return leaf
}

// TestCommandTree checks that every verb of the spec is reachable and maps
// to its leaf type.
func TestCommandTree(t *testing.T) {
	tests := []struct {
		args string
		want any
	}{
		{"guest preflight", &guest.Preflight{}},
		{"guest devlxd get /1.0", &guest.DevlxdGet{}},
		{"guest devlxd dump", &guest.DevlxdDump{}},
		{"guest devlxd watch user.lxd-probe.t1", &guest.DevlxdWatch{}},
		{"guest devlxd access --as-uid 0,1000", &guest.DevlxdAccess{}},
		{"guest devlxd access-child", &guest.DevlxdAccessChild{}},
		{"guest file put /run/lxd-probe/proof --content-file f", &guest.FilePut{}},
		{"guest file stat /run/lxd-probe/proof", &guest.FileStat{}},
		{"guest file rm /run/lxd-probe/proof", &guest.FileRm{}},
		{"guest file layout", &guest.FileLayout{}},
		{"guest idmap show", &guest.IdmapShow{}},
		{"server info", &server.Info{}},
		{"server whoami", &server.Whoami{}},
		{"server instance show c1", &server.InstanceShow{}},
		{"server instance watch c1", &server.InstanceWatch{}},
		{"server instance list", &server.InstanceList{}},
		{"server config get c1 user.lxd-probe.a", &server.ConfigGet{}},
		{"server config set c1 user.lxd-probe.a 1", &server.ConfigSet{}},
		{"server config unset c1 user.lxd-probe.a --method put", &server.ConfigUnset{}},
		{"server config race c1 user.lxd-probe.r", &server.ConfigRace{}},
		{"server file stat c1 /run/lxd-probe/proof", &server.FileStat{}},
		{"server file get c1 /run/lxd-probe/proof", &server.FileGet{}},
		{"server file head c1 /run/lxd-probe/proof", &server.FileHead{}},
		{"server file dir c1 /run/lxd-probe", &server.FileDir{}},
		{"server permissions c1", &server.Permissions{}},
		{"server bearer check", &server.BearerCheck{}},
		{"server rehearse file-pull c1", &server.RehearseFilePull{}},
		{"server rehearse config-push c1", &server.RehearseConfigPush{}},
		{"util diff a.json b.json", &util.Diff{}},
		{"util idmap translate --uid-map m --volatile-idmap v --uid 0", &util.IdmapTranslate{}},
		{"util idmap check i.json p.json", &util.IdmapCheck{}},
		{"util overlap r.json", &util.Overlap{}},
		{"util sanitize evidence", &util.Sanitize{}},
		{"util fixtures verify test/fixtures", &util.FixturesVerify{}},
		{"util note text", &util.Note{}},
		{"util report evidence/r1 evidence/r2", &util.Report{}},
	}
	for _, tt := range tests {
		t.Run(tt.args, func(t *testing.T) {
			leaf := mustParse(t, strings.Fields(tt.args)...)
			if reflect.TypeOf(leaf) != reflect.TypeOf(tt.want) {
				t.Errorf("leaf = %T; want %T", leaf, tt.want)
			}
		})
	}
}

// TestRunbookExamples parses command lines taken from the spec's runbooks
// and checks the values that reach the leaf.
func TestRunbookExamples(t *testing.T) {
	t.Run("config set", func(t *testing.T) {
		leaf := mustParse(t, "server", "config", "set", "NAME", "user.lxd-probe.a", "2",
			"--method", "patch", "--if-match", "stale", "--endpoint", "https://lab:8443",
			"--pin", "abcd", "--allow-write", "--lab", "--gate", "L14")
		cmd := leaf.(*server.ConfigSet)
		if cmd.Args.Name != "NAME" || cmd.Args.Key != "user.lxd-probe.a" || cmd.Args.Value != "2" {
			t.Errorf("args = %+v", cmd.Args)
		}
		if cmd.Method != "patch" || cmd.IfMatch != "stale" || cmd.Wait {
			t.Errorf("method = %q, if-match = %q, wait = %v", cmd.Method, cmd.IfMatch, cmd.Wait)
		}
		if cmd.Endpoint != "https://lab:8443" || cmd.Pin != "abcd" {
			t.Errorf("endpoint = %q, pin = %q", cmd.Endpoint, cmd.Pin)
		}
		if !cmd.AllowWrite || !cmd.Lab || cmd.Gate != "L14" {
			t.Errorf("allow-write = %v, lab = %v, gate = %q", cmd.AllowWrite, cmd.Lab, cmd.Gate)
		}
	})
	t.Run("config set defaults", func(t *testing.T) {
		cmd := mustParse(t, "server", "config", "set", "c1", "user.lxd-probe.a", "1").(*server.ConfigSet)
		if cmd.Method != "patch" || cmd.IfMatch != "auto" {
			t.Errorf("method = %q, if-match = %q; want patch, auto", cmd.Method, cmd.IfMatch)
		}
		if cmd.Project != "default" || cmd.Auth != "tls" {
			t.Errorf("project = %q, auth = %q; want default, tls", cmd.Project, cmd.Auth)
		}
		if cmd.EvidenceDir != "./evidence" || cmd.Timeout != 30*time.Second {
			t.Errorf("evidence-dir = %q, timeout = %v", cmd.EvidenceDir, cmd.Timeout)
		}
	})
	t.Run("config race", func(t *testing.T) {
		cmd := mustParse(t, "server", "config", "race", "NAME", "user.lxd-probe.r", "--writers", "8").(*server.ConfigRace)
		if cmd.Writers != 8 || cmd.Retries != 3 {
			t.Errorf("writers = %d, retries = %d", cmd.Writers, cmd.Retries)
		}
	})
	t.Run("devlxd watch", func(t *testing.T) {
		cmd := mustParse(t, "guest", "devlxd", "watch", "user.lxd-probe.t1", "--for", "60s").(*guest.DevlxdWatch)
		if cmd.Args.Key != "user.lxd-probe.t1" || cmd.For != 60*time.Second {
			t.Errorf("key = %q, for = %v", cmd.Args.Key, cmd.For)
		}
		if cmd.Socket != "/dev/lxd/sock" {
			t.Errorf("socket = %q", cmd.Socket)
		}
	})
	t.Run("devlxd access", func(t *testing.T) {
		cmd := mustParse(t, "guest", "devlxd", "access", "--as-uid", "0,1000,65534").(*guest.DevlxdAccess)
		if !reflect.DeepEqual([]uint32(cmd.AsUID), []uint32{0, 1000, 65534}) {
			t.Errorf("as-uid = %v", cmd.AsUID)
		}
	})
	t.Run("file put mode", func(t *testing.T) {
		cmd := mustParse(t, "guest", "file", "put", "/run/lxd-probe/proof", "--content-file", "-").(*guest.FilePut)
		if cmd.Mode != 0o600 {
			t.Errorf("default mode = %o; want 600", cmd.Mode)
		}
		cmd = mustParse(t, "guest", "file", "put", "/run/lxd-probe/proof", "--content-file", "-", "--mode", "0666").(*guest.FilePut)
		if cmd.Mode != 0o666 {
			t.Errorf("mode = %o; want 666", cmd.Mode)
		}
	})
	t.Run("instance watch", func(t *testing.T) {
		cmd := mustParse(t, "server", "instance", "watch", "NAME", "--for", "10m").(*server.InstanceWatch)
		if cmd.For != 10*time.Minute || cmd.Interval != time.Second {
			t.Errorf("for = %v, interval = %v", cmd.For, cmd.Interval)
		}
	})
	t.Run("bearer check", func(t *testing.T) {
		cmd := mustParse(t, "server", "bearer", "check", "--auth", "bearer", "--wait-expiry").(*server.BearerCheck)
		if cmd.Auth != "bearer" || !cmd.WaitExpiry || cmd.MaxWait != time.Hour {
			t.Errorf("auth = %q, wait-expiry = %v, max-wait = %v", cmd.Auth, cmd.WaitExpiry, cmd.MaxWait)
		}
	})
	t.Run("file get", func(t *testing.T) {
		cmd := mustParse(t, "server", "file", "get", "c1", "/run/lxd-probe/proof").(*server.FileGet)
		if cmd.MaxBytes != 1<<20 {
			t.Errorf("max-bytes = %d; want %d", cmd.MaxBytes, 1<<20)
		}
	})
	t.Run("util note", func(t *testing.T) {
		cmd := mustParse(t, "util", "note", "--gate", "L9", "--cell", "lxd6-unpriv-ubuntu-read", "file pull returned 403").(*util.Note)
		if cmd.Gate != "L9" || cmd.Cell != "lxd6-unpriv-ubuntu-read" || cmd.Args.Text != "file pull returned 403" {
			t.Errorf("gate = %q, cell = %q, text = %q", cmd.Gate, cmd.Cell, cmd.Args.Text)
		}
	})
	t.Run("util report", func(t *testing.T) {
		cmd := mustParse(t, "util", "report", "evidence/r1", "evidence/r2").(*util.Report)
		if !reflect.DeepEqual(cmd.Args.RunDirs, []string{"evidence/r1", "evidence/r2"}) {
			t.Errorf("run dirs = %v", cmd.Args.RunDirs)
		}
	})
	t.Run("util overlap default", func(t *testing.T) {
		cmd := mustParse(t, "util", "overlap", "r.json").(*util.Overlap)
		if cmd.ProofDir != "/run/spire/lxd" {
			t.Errorf("proof-dir = %q", cmd.ProofDir)
		}
	})
}

func TestUsageErrors(t *testing.T) {
	tests := []string{
		"server",                                     // command required
		"server config set c1 user.lxd-probe.a",      // missing positional argument
		"server config unset c1 user.lxd-probe.a",    // --method is required
		"server config set c1 k v --method post",     // invalid choice
		"server info --auth kerberos",                // invalid choice
		"guest devlxd access",                        // --as-uid is required
		"guest devlxd access --as-uid root",          // invalid uid
		"guest file put /run/lxd-probe/proof",        // --content-file is required
		"server instance watch c1 --for ten-minutes", // invalid duration
		"server info --no-such-flag",                 // unknown flag
		"util report",                                // at least one run directory
	}
	for _, args := range tests {
		t.Run(args, func(t *testing.T) {
			_, err := parse(t, strings.Fields(args)...)
			var flagsErr *flags.Error
			if !errors.As(err, &flagsErr) {
				t.Errorf("parse %q: err = %v (%T); want a *flags.Error", args, err, err)
			}
		})
	}
}

func TestHelp(t *testing.T) {
	_, err := parse(t, "server", "config", "set", "--help")
	var flagsErr *flags.Error
	if !errors.As(err, &flagsErr) || flagsErr.Type != flags.ErrHelp {
		t.Errorf("err = %v; want ErrHelp", err)
	}
}

// TestEnvironmentDefaults checks that session options read LXD_PROBE_*
// variables and that a flag overrides them (PRN-32).
func TestEnvironmentDefaults(t *testing.T) {
	t.Setenv("LXD_PROBE_ENDPOINT", "https://env:8443")
	t.Setenv("LXD_PROBE_PIN", "envpin")
	t.Setenv("LXD_PROBE_PROJECT", "restricted")
	t.Setenv("LXD_PROBE_RUN_ID", "run-env")
	t.Setenv("LXD_PROBE_CELL", "lxd6-unpriv-ubuntu")

	cmd := mustParse(t, "server", "info").(*server.Info)
	if cmd.Endpoint != "https://env:8443" || cmd.Pin != "envpin" || cmd.Project != "restricted" {
		t.Errorf("endpoint = %q, pin = %q, project = %q", cmd.Endpoint, cmd.Pin, cmd.Project)
	}
	if cmd.RunID != "run-env" || cmd.Cell != "lxd6-unpriv-ubuntu" {
		t.Errorf("run-id = %q, cell = %q", cmd.RunID, cmd.Cell)
	}

	cmd = mustParse(t, "server", "info", "--endpoint", "https://flag:8443").(*server.Info)
	if cmd.Endpoint != "https://flag:8443" {
		t.Errorf("endpoint = %q; want the flag to override the environment", cmd.Endpoint)
	}
}

// walk calls fn for every command of the tree, hidden ones included.
func walk(c *flags.Command, fn func(*flags.Command)) {
	fn(c)
	for _, sub := range c.Commands() {
		walk(sub, fn)
	}
}

// TestSafetyFlagsIgnoreEnvironment checks that the acknowledgement flags
// can only be given on the command line (PRN-32).
func TestSafetyFlagsIgnoreEnvironment(t *testing.T) {
	safety := map[string]bool{
		"allow-write": true, "lab": true, "allow-spire-keys": true,
		"allow-spire-paths": true, "allow-tls12": true,
	}
	parser := flags.NewParser(&Commands{}, flags.None)
	seen := map[string]bool{}
	walk(parser.Command, func(c *flags.Command) {
		for _, opt := range c.Options() {
			if safety[opt.LongName] {
				seen[opt.LongName] = true
				if opt.EnvKeyWithNamespace() != "" {
					t.Errorf("%s --%s reads %s; safety flags must not", c.Name, opt.LongName, opt.EnvKeyWithNamespace())
				}
			}
		}
	})
	for name := range safety {
		if !seen[name] {
			t.Errorf("no command declares --%s", name)
		}
	}
}

// TestDescriptions checks that every command and option is documented
// (PRN-31).
func TestDescriptions(t *testing.T) {
	parser := flags.NewParser(&Commands{}, flags.None)
	walk(parser.Command, func(c *flags.Command) {
		if c != parser.Command && c.ShortDescription == "" {
			t.Errorf("command %q has no description", c.Name)
		}
		for _, opt := range c.Options() {
			if opt.Description == "" {
				t.Errorf("command %q: option --%s has no description", c.Name, opt.LongName)
			}
		}
	})
}

func TestAccessChildHidden(t *testing.T) {
	parser := flags.NewParser(&Commands{}, flags.None)
	child := parser.Find("guest").Find("devlxd").Find("access-child")
	if child == nil || !child.Hidden {
		t.Errorf("guest devlxd access-child = %+v; want a hidden command", child)
	}
}
