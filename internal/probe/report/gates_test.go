package report

import (
	"flag"
	"os"
	"regexp"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update the runbooks copy and golden files")

const (
	probeSpec    = "../../../.specs/lxd-probe.md"
	attestorSpec = "../../../.specs/lxd-spire-plugins.md"
)

// section returns the spec text from the line starting with from up to the
// line starting with to.
func section(t *testing.T, path, from, to string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "\n"+from)
	if start < 0 {
		t.Fatalf("%s: no %q", path, from)
	}
	end := strings.Index(text[start+1:], "\n"+to)
	if end < 0 {
		t.Fatalf("%s: no %q after %q", path, to, from)
	}
	return text[start+1 : start+1+end+1]
}

// The gate texts are the spec's runbooks (PRU-08).
func TestRunbooksMatchSpec(t *testing.T) {
	want := section(t, probeSpec, "### Gate runbooks", "### Delivery plan")
	if *update {
		if err := os.WriteFile("runbooks.md", []byte(want), 0o644); err != nil { //nolint:gosec // G306: a source file
			t.Fatal(err)
		}
		return
	}
	if runbooks != want {
		t.Errorf("runbooks.md differs from the spec's Gate runbooks; run go test -run TestRunbooksMatchSpec -update ./internal/probe/report")
	}
}

func TestGates(t *testing.T) {
	gates, err := Gates()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	byID := map[string]Gate{}
	for _, g := range gates {
		ids = append(ids, g.ID)
		byID[g.ID] = g
	}
	if got := strings.Join(ids, " "); got != "L1 L2 L3 L4 L5 L6 L7 L8 L9 L10 L11 L12 L13 L14 L15 L16 L17" {
		t.Fatalf("gates %s", got)
	}
	l1 := byID["L1"]
	if l1.Title != "Files API metadata and owner representation" ||
		!strings.HasPrefix(l1.Question, "What does the files API return") || strings.Contains(l1.Question, "**Cells:**") {
		t.Errorf("L1 title %q question %q", l1.Title, l1.Question)
	}
	if !strings.HasPrefix(l1.Rules, "- Header names found") || !strings.HasSuffix(l1.Rules, "(or reject).") {
		t.Errorf("L1 rules %q", l1.Rules)
	}
	if l3 := byID["L3"]; l3.Question != "" || !strings.HasPrefix(l3.Rules, "identifies which fields") {
		t.Errorf("L3 question %q rules %q", l3.Question, l3.Rules)
	}
	if l14 := byID["L14"]; !strings.Contains(l14.Rules, "FR-S14 uses `PATCH`") || !strings.Contains(l14.Rules, "**Idle ETag (Q5):**") {
		t.Errorf("L14 rules %q: the decision list and the idle-ETag rate", l14.Rules)
	}
	if l5 := byID["L5"]; !l5.Withdrawn || l5.Rules != "" {
		t.Errorf("L5 %+v", l5)
	}
	for _, g := range gates {
		if g.Withdrawn || g.ID == "L10" {
			continue
		}
		if len(g.Axes) == 0 {
			t.Errorf("%s has no relevant axis values", g.ID)
		}
		if strings.Contains(g.Rules, "\n#### ") {
			t.Errorf("%s rules run into the next gate", g.ID)
		}
	}
}

func TestParseRunbooksErrors(t *testing.T) {
	for name, text := range map[string]string{
		"no gates":    "### Gate runbooks\n\nnothing\n",
		"twice":       "#### L1 — A\n\n**Decision:** x\n\n#### L1 — B\n\n**Decision:** y\n",
		"no decision": "#### L1 — A\n\n**Question:** why?\n",
	} {
		if _, err := parseRunbooks(text); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// APIUsage is the attestor spec's table.
func TestAPIUsageMatchesAttestorSpec(t *testing.T) {
	table := section(t, attestorSpec, "#### API usage and required extensions", "Required API extensions")
	row := regexp.MustCompile("(?m)^\\| ([^|]+) \\| `([A-Z]+) ([^`?]+)[^|]*\\|")
	var got []Call
	for _, m := range row.FindAllStringSubmatch(table, -1) {
		got = append(got, Call{Purpose: m[1], Method: m[2], Path: m[3]})
	}
	if len(got) != len(APIUsage) {
		t.Fatalf("attestor spec %d calls %v, APIUsage %d", len(got), got, len(APIUsage))
	}
	for i := range got {
		if got[i] != APIUsage[i] {
			t.Errorf("call %d: spec %+v, APIUsage %+v", i, got[i], APIUsage[i])
		}
	}
}
