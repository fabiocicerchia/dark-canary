package noise

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fabiocicerchia/dark-canary/internal/collector"
	"github.com/fabiocicerchia/dark-canary/internal/diff"
)

func TestGlobMatching(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/headers/date", "/headers/date", true},
		{"/headers/date", "/headers/Date", true}, // header names are case-insensitive
		{"/headers/*", "/headers/date", true},
		{"/headers/*", "/headers/date/extra", false},
		{"/body/items/*/updatedAt", "/body/items/0/updatedAt", true},
		{"/body/items/*/updatedAt", "/body/items/0/1/updatedAt", false},
		{"/body/**/updatedAt", "/body/a/b/c/updatedAt", true},
		{"/body/**/updatedAt", "/body/updatedAt", true}, // ** matches zero segments
		{"/body/**", "/body/anything/at/all", true},
		{"/status", "/body/status", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.path); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestLaterRulesOverrideEarlierOnes(t *testing.T) {
	rs := Ruleset{Rules: []Rule{
		{Path: "/body/**", Ignore: true},
		{Path: "/body/total", Normalise: "round:2"},
	}}
	if rs.Suppress("/body/name", "a", "b") != true {
		t.Error("the broad ignore should still apply where nothing overrides it")
	}
	// The later, narrower rule wins, so a real difference here is NOT ignored.
	if rs.Suppress("/body/total", json.Number("1.00"), json.Number("2.00")) {
		t.Error("the narrower rule must win, and these differ by more than rounding")
	}
}

func TestNormalisations(t *testing.T) {
	cases := []struct {
		name       string
		rule       Rule
		a, b       any
		suppressed bool
	}{
		{"round hides a float precision difference", Rule{Path: "/x", Normalise: "round:2"},
			json.Number("1.005"), json.Number("1.0049"), true},
		{"round does not hide a real change", Rule{Path: "/x", Normalise: "round:2"},
			json.Number("1.01"), json.Number("1.99"), false},
		{"sort hides array ordering", Rule{Path: "/x", Normalise: "sort"},
			[]any{"a", "b"}, []any{"b", "a"}, true},
		{"sort does not hide a different set", Rule{Path: "/x", Normalise: "sort"},
			[]any{"a", "b"}, []any{"a", "c"}, false},
		{"sort does not hide a different length", Rule{Path: "/x", Normalise: "sort"},
			[]any{"a", "b"}, []any{"a"}, false},
		{"trim hides surrounding whitespace", Rule{Path: "/x", Normalise: "trim"},
			" a ", "a", true},
		{"lower hides a case difference", Rule{Path: "/x", Normalise: "lower"},
			"Value", "value", true},
		{"len hides an opaque id of the same shape", Rule{Path: "/x", Normalise: "len"},
			"abc123", "xyz789", true},
		{"len does not hide a different shape", Rule{Path: "/x", Normalise: "len"},
			"abc", "abcd", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := Ruleset{Rules: []Rule{c.rule}}
			if got := rs.Suppress("/x", c.a, c.b); got != c.suppressed {
				t.Errorf("Suppress = %v, want %v", got, c.suppressed)
			}
		})
	}
}

// The contract the diff engine depends on: values that were never different
// must not be counted as suppressions, or the report understates the agreement
// it is claiming to measure.
func TestEqualValuesAreNeverCountedAsSuppressed(t *testing.T) {
	rs := Ruleset{Rules: []Rule{{Path: "/x", Normalise: "sort"}}}
	if rs.Suppress("/x", []any{"a", "b"}, []any{"a", "b"}) {
		t.Error("identical arrays are not a suppressed difference")
	}
	rs = Ruleset{Rules: []Rule{{Path: "/x", Normalise: "round:2"}}}
	if rs.Suppress("/x", json.Number("1"), json.Number("1")) {
		t.Error("identical numbers are not a suppressed difference")
	}
}

func TestUnmatchedPathsAreNeverSuppressed(t *testing.T) {
	rs := Default()
	if rs.Suppress("/body/total", json.Number("1"), json.Number("2")) {
		t.Error("the defaults must not touch response bodies")
	}
	if !rs.Suppress("/headers/date", "a", "b") {
		t.Error("the defaults must cover Date")
	}
}

func TestDefaultsAreShortEnoughToNotHideFindings(t *testing.T) {
	// A long default ruleset hides real divergence on day one. If this grows,
	// it should be a deliberate decision, not a drift.
	if n := len(Default().Rules); n > 10 {
		t.Errorf("default ruleset has grown to %d rules — justify each one", n)
	}
	for _, r := range Default().Rules {
		if r.Reason == "" {
			t.Errorf("default rule %s has no reason; a rule nobody can explain is one nobody dares delete", r.Path)
		}
	}
}

func TestMergePutsOperatorRulesLast(t *testing.T) {
	merged := Default().Merge(Ruleset{Rules: []Rule{{Path: "/headers/date", Normalise: "len"}}})
	// The operator re-enabled Date as a length comparison, so a genuinely
	// different length is no longer hidden.
	if merged.Suppress("/headers/date", "Mon, 01 Jan 2035 00:00:00 GMT", "nope") {
		t.Error("the operator rule should have overridden the default ignore")
	}
}

func TestLoadRejectsRulesThatDoNothing(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if _, err := Load(write("ok.yaml", "rules:\n  - path: /body/ts\n    ignore: true\n")); err != nil {
		t.Fatalf("valid ruleset rejected: %v", err)
	}
	if _, err := Load(write("noop.yaml", "rules:\n  - path: /body/ts\n")); err == nil {
		t.Error("a rule that neither ignores nor normalises must be rejected, not silently ignored")
	}
	if _, err := Load(write("nopath.yaml", "rules:\n  - ignore: true\n")); err == nil {
		t.Error("a rule with no path must be rejected")
	}
	if _, err := Load(write("bad.yaml", "rules:\n  - path: /x\n    normalise: round:banana\n")); err == nil {
		t.Error("an unparseable normalisation must be rejected at load, not at 3am")
	}
	if _, err := Load(write("unknown.yaml", "rules:\n  - path: /x\n    normalise: vibes\n")); err == nil {
		t.Error("an unknown normalisation must be rejected")
	}
	if _, err := Load(write("notyaml.yaml", "rules: [ unclosed\n")); err == nil {
		t.Error("unparseable YAML must be rejected")
	}
}

// End to end through the engine, which is where the rules actually run.
func TestRulesetSuppressesThroughTheEngine(t *testing.T) {
	rs := Default().Merge(Ruleset{Rules: []Rule{
		{Path: "/body/**/updatedAt", Ignore: true, Reason: "timestamps"},
		{Path: "/body/total", Normalise: "round:2", Reason: "float precision"},
		{Path: "/body/tags", Normalise: "sort", Reason: "ordering is not guaranteed"},
	}})
	engine := diff.NewEngine(diff.Options{Suppressor: rs})

	p := collector.Pair{
		CorrelID: "c1",
		Primary: collector.Capture{
			Status:     200,
			ResHeaders: map[string][]string{"Date": {"Mon, 01 Jan 2035 00:00:00 GMT"}},
			ResBody:    []byte(`{"items":[{"updatedAt":"2026-01-01","id":1}],"total":10.004,"tags":["a","b"],"name":"x"}`),
		},
		Shadow: collector.Capture{
			Status:     200,
			ResHeaders: map[string][]string{"Date": {"Tue, 02 Jan 2035 00:00:00 GMT"}},
			ResBody:    []byte(`{"items":[{"updatedAt":"2026-06-30","id":1}],"total":10.001,"tags":["b","a"],"name":"y"}`),
		},
	}

	r := engine.Diff(p)
	if len(r.Differences) != 1 || r.Differences[0].Path != "/body/name" {
		t.Fatalf("only the real change should survive, got %+v", r.Differences)
	}
	if r.Suppressed != 4 {
		t.Errorf("suppressed = %d, want 4 (date, updatedAt, total, tags)", r.Suppressed)
	}
}

func TestApplyFiltersAStoredResult(t *testing.T) {
	r := diff.Result{Differences: []diff.Difference{
		{Kind: diff.KindBodyVal, Path: "/body/ts", Primary: "1", Shadow: "2"},
		{Kind: diff.KindBodyVal, Path: "/body/name", Primary: "a", Shadow: "b"},
	}}
	out := Ruleset{Rules: []Rule{{Path: "/body/ts", Ignore: true}}}.Apply(r)
	if len(out.Differences) != 1 || out.Differences[0].Path != "/body/name" {
		t.Fatalf("got %+v", out.Differences)
	}
	if out.Suppressed != 1 {
		t.Errorf("suppressed = %d, want 1", out.Suppressed)
	}
}

// matchGlob is hand-rolled recursive matching, and "**" backtracks: it is the
// one algorithm here whose cost is not obvious by reading it. Paths come from
// production response bodies, so a pattern that blows up on an attacker-chosen
// key would stall the collector. Go's fuzzer fails a target that hangs, which
// is what makes this worth running.
func FuzzGlobMatching(f *testing.F) {
	seeds := [][2]string{
		{"/headers/date", "/headers/date"},
		{"/body/**", "/body/items/0/updatedAt"},
		{"/body/**/id", "/body/a/b/c/id"},
		{"**/**/**/**", "/a/b/c/d/e/f/g/h"}, // the backtracking case
		{"*", "/a"},
		{"", ""},
		{"//", "/a//b"}, // empty segments
	}
	for _, s := range seeds {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, pattern, path string) {
		matchGlob(pattern, path)
		// A pattern is always its own path, whatever the segments contain.
		if !matchGlob(path, path) {
			t.Fatalf("%q must match itself", path)
		}
	})
}
