package diff

import (
	"strings"
	"testing"

	"github.com/fabiocicerchia/dark-canary/internal/collector"
)

func pair(primaryBody, shadowBody string) collector.Pair {
	return collector.Pair{
		CorrelID: "c1",
		Primary:  collector.Capture{Path: "primary", Status: 200, Method: "GET", URL: "/x", ResBody: []byte(primaryBody)},
		Shadow:   collector.Capture{Path: "shadow", Status: 200, ResBody: []byte(shadowBody)},
	}
}

func diffOf(t *testing.T, p collector.Pair, s Suppressor) Result {
	t.Helper()
	return NewEngine(Options{Suppressor: s}).Diff(p)
}

func onlyDiff(t *testing.T, r Result) Difference {
	t.Helper()
	if len(r.Differences) != 1 {
		t.Fatalf("want exactly one difference, got %d: %+v", len(r.Differences), r.Differences)
	}
	return r.Differences[0]
}

// The premise of the whole tool: a textual diff of two JSON responses is
// worthless, a structural one is the product.
func TestKeyOrderIsNotADifference(t *testing.T) {
	r := diffOf(t, pair(`{"a":1,"b":2}`, `{"b":2,"a":1}`), nil)
	if !r.Identical() {
		t.Fatalf("re-ordered keys must not diverge: %+v", r.Differences)
	}
}

func TestWhitespaceIsNotADifference(t *testing.T) {
	r := diffOf(t, pair(`{"a":[1,2]}`, "{\n  \"a\": [ 1, 2 ]\n}"), nil)
	if !r.Identical() {
		t.Fatalf("formatting must not diverge: %+v", r.Differences)
	}
}

func TestNumbersCompareNumerically(t *testing.T) {
	r := diffOf(t, pair(`{"a":1.0,"big":10000000000000001}`, `{"a":1,"big":10000000000000001}`), nil)
	if !r.Identical() {
		t.Fatalf("1.0 and 1 are the same value, and large ints must survive decoding: %+v", r.Differences)
	}
}

func TestValueDifferenceIsReportedWithAPointerPath(t *testing.T) {
	d := onlyDiff(t, diffOf(t, pair(`{"user":{"name":"a"}}`, `{"user":{"name":"b"}}`), nil))
	if d.Kind != KindBodyVal || d.Path != "/body/user/name" {
		t.Fatalf("got %+v", d)
	}
	if d.Primary != "a" || d.Shadow != "b" {
		t.Errorf("both sides must be shown: %+v", d)
	}
}

func TestMissingKeyIsItsOwnKind(t *testing.T) {
	d := onlyDiff(t, diffOf(t, pair(`{"a":1,"b":2}`, `{"a":1}`), nil))
	if d.Kind != KindBodyKey || d.Path != "/body/b" {
		t.Fatalf("got %+v", d)
	}
	if d.Shadow != "(absent)" {
		t.Errorf("an absent value must not render as empty: %q", d.Shadow)
	}
}

func TestTypeChangeIsAShapeDifference(t *testing.T) {
	d := onlyDiff(t, diffOf(t, pair(`{"id":"7"}`, `{"id":7}`), nil))
	if d.Kind != KindShape || d.Primary != "string" || d.Shadow != "number" {
		t.Fatalf("got %+v", d)
	}
}

// One inserted element must not report every following index as changed — that
// is the wall of false positives this engine exists to avoid.
func TestArrayLengthIsReportedOnceNotPerIndex(t *testing.T) {
	r := diffOf(t, pair(`{"xs":[1,2,3]}`, `{"xs":[1,2,3,4]}`), nil)
	if len(r.Differences) != 1 {
		t.Fatalf("want one shape difference, got %d: %+v", len(r.Differences), r.Differences)
	}
	if r.Differences[0].Kind != KindShape || !strings.Contains(r.Differences[0].Primary, "3 items") {
		t.Errorf("got %+v", r.Differences[0])
	}
}

func TestStatusDifference(t *testing.T) {
	p := pair(`{}`, `{}`)
	p.Shadow.Status = 500
	d := onlyDiff(t, diffOf(t, p, nil))
	if d.Kind != KindStatus || d.Path != PathStatus || d.Shadow != "500" {
		t.Fatalf("got %+v", d)
	}
}

func TestHeadersAreComparedByNameNotByCase(t *testing.T) {
	p := pair(`{}`, `{}`)
	p.Primary.ResHeaders = map[string][]string{"Content-Type": {"application/json"}}
	p.Shadow.ResHeaders = map[string][]string{"content-type": {"application/json"}}
	if r := diffOf(t, p, nil); !r.Identical() {
		t.Fatalf("header case must not diverge: %+v", r.Differences)
	}

	p.Shadow.ResHeaders = map[string][]string{"content-type": {"text/plain"}}
	d := onlyDiff(t, diffOf(t, p, nil))
	if d.Kind != KindHeader || d.Path != "/headers/content-type" {
		t.Fatalf("got %+v", d)
	}
}

// Transport headers differ on every single request and say nothing about
// behaviour, so a fresh install must be usable before anyone writes a rule.
func TestTransportHeadersAreIgnoredWithoutAnyRules(t *testing.T) {
	p := pair(`{}`, `{}`)
	p.Primary.ResHeaders = map[string][]string{"Connection": {"keep-alive"}, "Content-Length": {"2"}}
	p.Shadow.ResHeaders = map[string][]string{"Connection": {"close"}, "Content-Length": {"999"}}
	if r := diffOf(t, p, nil); !r.Identical() {
		t.Fatalf("transport headers must not diverge: %+v", r.Differences)
	}
}

func TestMissingHeaderShowsAnEmptySide(t *testing.T) {
	p := pair(`{}`, `{}`)
	p.Primary.ResHeaders = map[string][]string{"X-Cache": {"HIT"}}
	d := onlyDiff(t, diffOf(t, p, nil))
	if d.Path != "/headers/x-cache" || d.Shadow != "" {
		t.Fatalf("got %+v", d)
	}
}

func TestNonJSONBodiesFallBackToBytes(t *testing.T) {
	if r := diffOf(t, pair("hello", "hello"), nil); !r.Identical() {
		t.Fatalf("identical text bodies must not diverge: %+v", r.Differences)
	}
	d := onlyDiff(t, diffOf(t, pair("hello", "goodbye"), nil))
	if d.Kind != KindBodyVal || d.Path != PathBody {
		t.Fatalf("got %+v", d)
	}
}

func TestOneSideCeasingToBeJSONIsAFinding(t *testing.T) {
	d := onlyDiff(t, diffOf(t, pair(`{"a":1}`, `<html>500</html>`), nil))
	if d.Kind != KindShape || d.Primary != "valid JSON" || d.Shadow != "not JSON" {
		t.Fatalf("got %+v", d)
	}
}

func TestEmptyBodiesOnBothSidesAreIdentical(t *testing.T) {
	if r := diffOf(t, pair("", ""), nil); !r.Identical() {
		t.Fatalf("two empty bodies must not diverge: %+v", r.Differences)
	}
}

func TestRenderedValuesAreTruncated(t *testing.T) {
	long := strings.Repeat("x", 500)
	r := NewEngine(Options{MaxValueLen: 20}).Diff(pair(`{"a":"`+long+`"}`, `{"a":"short"}`))
	d := onlyDiff(t, r)
	if len(d.Primary) > 60 || !strings.Contains(d.Primary, "500 bytes") {
		t.Fatalf("long values must be truncated and their real size stated: %q", d.Primary)
	}
}

// A key containing "/" must not be able to forge a path that a noise rule then
// matches by accident.
func TestSlashesInKeysAreEscaped(t *testing.T) {
	d := onlyDiff(t, diffOf(t, pair(`{"a/b":1}`, `{"a/b":2}`), nil))
	if d.Path != "/body/a~1b" {
		t.Fatalf("path = %q, want the RFC 6901 escaped form", d.Path)
	}
}

// --- suppression --------------------------------------------------------------

type pathSuppressor struct{ path string }

func (p pathSuppressor) Suppress(path string, _, _ any) bool { return path == p.path }

func TestSuppressedDifferencesAreCountedNotHidden(t *testing.T) {
	r := diffOf(t, pair(`{"ts":1,"v":1}`, `{"ts":2,"v":2}`), pathSuppressor{path: "/body/ts"})
	if len(r.Differences) != 1 || r.Differences[0].Path != "/body/v" {
		t.Fatalf("got %+v", r.Differences)
	}
	if r.Suppressed != 1 {
		t.Fatalf("suppressed = %d, want 1 — suppression must never look like agreement", r.Suppressed)
	}
}

type arraySuppressor struct{ calls []string }

func (a *arraySuppressor) Suppress(path string, primary, shadow any) bool {
	a.calls = append(a.calls, path)
	_, isArray := primary.([]any)
	return isArray && path == "/body/tags"
}

// Arrays are offered whole before their elements are walked, because "same set,
// different order" cannot be decided from per-index differences.
func TestArraysAreOfferedToTheRulesBeforeBeingWalked(t *testing.T) {
	s := &arraySuppressor{}
	r := diffOf(t, pair(`{"tags":["a","b"]}`, `{"tags":["b","a"]}`), s)
	if !r.Identical() {
		t.Fatalf("the rule should have suppressed the whole array: %+v", r.Differences)
	}
	if r.Suppressed != 1 {
		t.Fatalf("suppressed = %d, want 1", r.Suppressed)
	}
	if len(s.calls) == 0 || s.calls[0] != "/body/tags" {
		t.Fatalf("the array itself must be offered first, got %v", s.calls)
	}
}

// Response bodies come off production traffic, so Diff is the one place this
// tool touches bytes nobody vetted. Decoding and the recursive walk must not
// panic on any of them — a crash here takes the canary down with the deploy
// it was meant to be watching.
func FuzzDiffBodies(f *testing.F) {
	seeds := [][2]string{
		{`{"a":1}`, `{"a":2}`},
		{`[1,[2,[3]]]`, `[1,[2,[4]]]`},
		{`{"a":{"b":null}}`, `{"a":{"b":[]}}`},
		{`1e400`, `-1e400`},          // number literals float64 cannot hold
		{"\ufeff{}", `{"\ud800":1}`}, // BOM, lone surrogate
		{`{"":1}`, `{"a/b~c":1}`},    // empty key, pointer-escaping key
		{"", "not json"},             // neither side decodes
		{`{"a":1}`, ""},              // one side stops being JSON
	}
	for _, s := range seeds {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, primary, shadow string) {
		r := NewEngine(Options{}).Diff(pair(primary, shadow))
		// Identical inputs must never diverge: the false positive that would
		// make every report untrustworthy.
		if primary == shadow && !r.Identical() {
			t.Fatalf("%q vs itself diverged: %+v", primary, r.Differences)
		}
		for _, d := range r.Differences {
			if d.Path == "" {
				t.Fatalf("difference with no path: %+v", d)
			}
		}
	})
}
