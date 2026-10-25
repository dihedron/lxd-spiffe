// Package docs holds the tests that keep the release documents complete
// (see .specs/lxd-spire-docs.md): the documents themselves are Markdown,
// built to PDF by build.sh.
package docs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	agent "github.com/dihedron/lxd-spiffe/internal/plugin/agent/lxdiid"
	"github.com/dihedron/lxd-spiffe/internal/plugin/config"
	server "github.com/dihedron/lxd-spiffe/internal/plugin/server/lxdiid"
	"github.com/dihedron/lxd-spiffe/internal/policy"
)

// configurationReference is the Setup Guide's configuration reference.
const configurationReference = "setup-guide/11-configuration-reference.md"

// sections splits a Markdown document by its level-2 headings.
func sections(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	heading := ""
	for _, line := range strings.Split(string(data), "\n") {
		if title, ok := strings.CutPrefix(line, "## "); ok {
			heading = strings.TrimSpace(title)
			continue
		}
		out[heading] += line + "\n"
	}
	return out
}

// TestConfigurationReferenceIsComplete fails when a configuration key of
// either plugin is missing from its section of the Setup Guide's
// configuration reference, in code font.
func TestConfigurationReferenceIsComplete(t *testing.T) {
	doc := sections(t, configurationReference)
	for _, component := range []struct {
		section string
		keys    []string
	}{
		{"Server plugin", server.ConfigKeys()},
		{"Agent plugin", agent.ConfigKeys()},
	} {
		text, ok := doc[component.section]
		if !ok {
			t.Errorf("%s: no \"## %s\" section", configurationReference, component.section)
			continue
		}
		if len(component.keys) == 0 {
			t.Errorf("%s: no keys found (the test is broken)", component.section)
		}
		for _, key := range component.keys {
			if !strings.Contains(text, "`"+key+"`") {
				t.Errorf("%s: the key `%s` is not documented in the %s section", configurationReference, key, component.section)
			}
		}
	}
}

// TestSamplesDecode fails when a packaged sample holds a key its plugin
// does not accept: SPIRE would refuse to start with it.
func TestSamplesDecode(t *testing.T) {
	for _, sample := range []struct {
		file, pluginType string
		keys             []string
		v                any
	}{
		{"../examples/server.conf", "NodeAttestor", server.ConfigKeys(), &server.Config{}},
		{"../examples/agent.conf", "NodeAttestor", agent.ConfigKeys(), &agent.Config{}},
	} {
		data, err := os.ReadFile(sample.file)
		if err != nil {
			t.Fatal(err)
		}
		pluginData, err := config.PluginData(data, sample.pluginType, "lxd_iid")
		if err != nil {
			t.Fatalf("%s: %v", sample.file, err)
		}
		if err := config.Decode(pluginData, sample.v, sample.keys); err != nil {
			t.Errorf("%s: %v", sample.file, err)
		}
	}
}

// separator matches a pipe table's header separator line.
var separator = regexp.MustCompile(`^\|(\s*:?-+:?\s*\|)+\s*$`)

// TestTablesSetColumnWidths fails on a pipe table whose separator line
// leaves the column widths to pandoc ("| --- | --- |"). With a cell wider
// than the line, pandoc then gives every column the same width. The dashes'
// relative lengths set the columns' relative widths.
func TestTablesSetColumnWidths(t *testing.T) {
	files, err := filepath.Glob("*/[0-9][0-9]-*.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Clean(file))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !separator.MatchString(strings.TrimSpace(line)) {
				continue
			}
			lengths := map[int]bool{}
			for _, cell := range strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|") {
				lengths[len(strings.Trim(strings.TrimSpace(cell), ":"))] = true
			}
			if len(lengths) == 1 {
				t.Errorf("%s:%d: the table's columns all have the same width: set their proportions with the dashes' lengths", file, i+1)
			}
		}
	}
}

// errorReference is the Operator's Guide's error reference.
const errorReference = "operators-guide/05-error-reference.md"

// messageArg maps a call that carries an error or log message to the index
// of its message argument: the standard constructors, the plugins' own
// (fail, invalid, deny), gRPC statuses and slog's warnings and errors.
var messageArg = map[string]int{
	"errors.New": 0, "fmt.Errorf": 0, "status.Error": 1, "status.Errorf": 1,
	"fail": 1, "invalid": 0, "deny": 1, "Warn": 0, "Error": 0,
}

// inspect walks the syntax trees of the Go files of dir, not their tests.
func inspect(t *testing.T, dir string, visit func(ast.Node)) {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			visit(n)
			return true
		})
	}
}

// stringLit returns the value of a string literal.
func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// callName names a call as messageArg does: "pkg.Func", a method's name,
// or a plain function's.
func callName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		if pkg, ok := fun.X.(*ast.Ident); ok && fun.Sel.Name != "Warn" && fun.Sel.Name != "Error" {
			return pkg.Name + "." + fun.Sel.Name
		}
		return fun.Sel.Name
	}
	return ""
}

// messageText returns the fixed text of a call's message: up to its first
// formatting verb, after a leading "%w: " (the wrapped error, documented on
// its own).
func messageText(call *ast.CallExpr) (string, bool) {
	arg, ok := messageArg[callName(call)]
	if !ok || len(call.Args) <= arg {
		return "", false
	}
	text, ok := stringLit(call.Args[arg])
	if !ok {
		return "", false
	}
	text = strings.TrimPrefix(text, "%w")
	if i := strings.Index(text, "%"); i >= 0 {
		text = text[:i]
	}
	return strings.Trim(text, " :;(,"), true
}

// messages returns the fixed text of every error and every warning or error
// log message the Go files of dir create. Texts shorter than 4 characters
// say nothing and are skipped.
func messages(t *testing.T, dir string) []string {
	t.Helper()
	var texts []string
	inspect(t, dir, func(n ast.Node) {
		if call, ok := n.(*ast.CallExpr); ok {
			if text, ok := messageText(call); ok && len(text) >= 4 {
				texts = append(texts, text)
			}
		}
	})
	return texts
}

// reasonCodes returns the values of the string constants of dir whose
// names start with "reason", and the literal reasons passed to fail.
func reasonCodes(t *testing.T, dir string) []string {
	t.Helper()
	var codes []string
	inspect(t, dir, func(n ast.Node) {
		var e ast.Expr
		switch n := n.(type) {
		case *ast.ValueSpec:
			if len(n.Names) == 1 && strings.HasPrefix(n.Names[0].Name, "reason") && len(n.Values) == 1 {
				e = n.Values[0]
			}
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "fail" && len(n.Args) > 0 {
				e = n.Args[0]
			}
		}
		if code, ok := stringLit(e); ok {
			codes = append(codes, code)
		}
	})
	return codes
}

// TestErrorReferenceIsComplete fails when something an operator can meet is
// missing from the Operator's Guide's error reference: a reason code of the
// server's log, or the text of an error or warning the plugins and the
// packages that check their input and configuration return or log.
func TestErrorReferenceIsComplete(t *testing.T) {
	data, err := os.ReadFile(errorReference)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	want := map[string]string{}
	const serverDir = "../internal/plugin/server/lxdiid"
	codes := reasonCodes(t, serverDir)
	for _, code := range []policy.Code{policy.NotRunning, policy.ScopeDenied, policy.PolicyDenied} {
		codes = append(codes, string(code))
	}
	if len(codes) < 10 {
		t.Fatalf("only %d reason codes found (the test is broken)", len(codes))
	}
	for _, code := range codes {
		want["`"+code+"`"] = "the reason code " + code
	}
	for _, dir := range []string{
		serverDir,
		"../internal/plugin/agent/lxdiid",
		"../internal/plugin/config",
		"../internal/policy",
		"../internal/claim",
		"../internal/proof",
		"../internal/agentproof",
		"../internal/selectors",
		"../internal/proto",
		"../internal/lxd",
	} {
		for _, text := range messages(t, dir) {
			want[text] = "a message of " + dir
		}
	}
	for text, source := range want {
		if !strings.Contains(doc, text) {
			t.Errorf("%s: %s is missing: %q", errorReference, source, text)
		}
	}
}
