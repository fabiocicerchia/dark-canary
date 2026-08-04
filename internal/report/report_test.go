package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/fabiocicerchia/dark-canary/internal/diff"
)

func fixedClock() func() time.Time {
	t := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func result(correl string, diffs ...diff.Difference) diff.Result {
	return diff.Result{CorrelID: correl, Method: "GET", URL: "/x", Differences: diffs}
}

func TestAgreementRateAndCounts(t *testing.T) {
	a := New(fixedClock())
	a.Add(result("a"))
	a.Add(result("b"))
	a.Add(result("c", diff.Difference{Kind: diff.KindBodyVal, Path: "/body/x"}))

	s := a.Summary()
	if s.Pairs != 3 || s.Identical != 2 || s.Divergent != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if got := s.AgreementRate(); got < 0.66 || got > 0.67 {
		t.Errorf("agreement = %v, want ~0.667", got)
	}
}

// Grouping is the point: a list of individual diffs is unreadable at volume.
func TestIdenticalPathsCollapseIntoOneGroupWithARate(t *testing.T) {
	a := New(fixedClock())
	for i := 0; i < 9; i++ {
		a.Add(result("c", diff.Difference{Kind: diff.KindBodyVal, Path: "/body/ts", Primary: "1", Shadow: "2"}))
	}
	a.Add(result("clean"))

	s := a.Summary()
	if len(s.Groups) != 1 {
		t.Fatalf("want one group, got %d", len(s.Groups))
	}
	g := s.Groups[0]
	if g.Count != 9 {
		t.Errorf("count = %d, want 9", g.Count)
	}
	if g.Rate < 0.89 || g.Rate > 0.91 {
		t.Errorf("rate = %v, want ~0.9 of all pairs", g.Rate)
	}
	if g.Example.Primary != "1" || g.Example.Shadow != "2" {
		t.Errorf("the first occurrence must be kept as the example: %+v", g.Example)
	}
}

// A rare 500 on the shadow matters more than a timestamp that differs on every
// single request. Frequency alone would rank them the other way round.
func TestSeverityOutranksFrequency(t *testing.T) {
	a := New(fixedClock())
	for i := 0; i < 100; i++ {
		a.Add(result("c", diff.Difference{Kind: diff.KindBodyVal, Path: "/body/ts"}))
	}
	a.Add(result("rare", diff.Difference{Kind: diff.KindStatus, Path: "/status", Primary: "200", Shadow: "500"}))

	s := a.Summary()
	if s.Groups[0].Path != "/status" {
		t.Fatalf("a single status divergence must lead, got %+v", s.Groups[0])
	}
	if s.Groups[0].Severity != "high" || s.Groups[1].Severity != "low" {
		t.Errorf("severities = %s, %s", s.Groups[0].Severity, s.Groups[1].Severity)
	}
}

func TestMissingKeysRankAboveChangedValues(t *testing.T) {
	a := New(fixedClock())
	a.Add(result("a", diff.Difference{Kind: diff.KindBodyVal, Path: "/body/v"}))
	a.Add(result("b", diff.Difference{Kind: diff.KindBodyKey, Path: "/body/k"}))
	a.Add(result("c", diff.Difference{Kind: diff.KindShape, Path: "/body/s"}))

	s := a.Summary()
	if s.Groups[len(s.Groups)-1].Path != "/body/v" {
		t.Fatalf("a changed value is the least alarming; got order %+v", s.Groups)
	}
}

func TestSuppressionsAreReportedNotHidden(t *testing.T) {
	a := New(fixedClock())
	r := result("a")
	r.Suppressed = 7
	a.Add(r)

	var b bytes.Buffer
	if err := Text(&b, a.Summary()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "7 differences suppressed") {
		t.Errorf("the report must say how much it hid:\n%s", b.String())
	}
}

// "Nothing to report" and "nothing arrived" look identical on a dashboard and
// mean opposite things.
func TestNoPairsSaysSoRatherThanClaimingAgreement(t *testing.T) {
	var b bytes.Buffer
	if err := Text(&b, New(fixedClock()).Summary()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "No pairs yet") {
		t.Errorf("an empty report must not read as success:\n%s", b.String())
	}

	a := New(fixedClock())
	a.Add(result("a"))
	b.Reset()
	if err := Text(&b, a.Summary()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "No divergence.") {
		t.Errorf("a clean run must say so:\n%s", b.String())
	}
}

func TestTextRendersOneLinePerGroup(t *testing.T) {
	a := New(fixedClock())
	a.Add(result("a", diff.Difference{
		Kind: diff.KindBodyVal, Path: "/body/name",
		Primary: "first\nsecond", Shadow: strings.Repeat("y", 80),
	}))

	var b bytes.Buffer
	if err := Text(&b, a.Summary()); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if strings.Contains(line, "second") && strings.Contains(line, "first") {
			continue // same line: the newline was flattened, which is the point
		}
		if strings.HasPrefix(line, "second") {
			t.Errorf("a multi-line value broke the table:\n%s", b.String())
		}
	}
	if !strings.Contains(b.String(), "…") {
		t.Errorf("long example values must be truncated:\n%s", b.String())
	}
}
