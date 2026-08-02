// Package diff is THE product. Mirroring is not the value — nginx's `mirror`
// directive already does fire-and-forget subrequests. The value is a structural
// comparison of the two responses that survives real-world noise, so operators
// see genuine divergence and not a wall of false positives.
//
// Prior art to respect: Twitter's Diffy and GitHub's Scientist did this for
// services and library refactors respectively; both are long unmaintained, and
// nothing owns the edge-level version.
package diff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fabiocicerchia/local-ai-lab/dark-canary/internal/collector"
)

// Kind classifies a single difference.
type Kind string

const (
	KindStatus  Kind = "status"
	KindHeader  Kind = "header"
	KindBodyVal Kind = "body_value" // a JSON value differs
	KindBodyKey Kind = "body_key"   // a key present on one side only
	KindShape   Kind = "shape"      // array length / type mismatch
)

// Difference is one structural divergence between primary and shadow.
type Difference struct {
	Kind    Kind   `json:"kind"`
	Path    string `json:"path"`
	Primary string `json:"primary"`
	Shadow  string `json:"shadow"`
}

// Result is the diff of one pair, after noise suppression.
type Result struct {
	CorrelID    string       `json:"correl_id"`
	Method      string       `json:"method"`
	URL         string       `json:"url"`
	Differences []Difference `json:"differences"`
	Suppressed  int          `json:"suppressed"` // how many raw diffs the noise rules removed (report this!)
}

// Identical is the question an operator actually asks.
func (r Result) Identical() bool { return len(r.Differences) == 0 }

// Engine performs a structural (not textual) comparison. A textual diff that
// reports 100% divergence is worthless; this understands JSON structure, header
// semantics and ordering.
type Engine interface {
	Diff(p collector.Pair) Result
}

// Suppressor is the hook the noise rules plug into. It is consulted at the
// moment two values are compared, because that is the only place both values
// still exist — rounding a float or ignoring an array's order cannot be decided
// from a rendered difference after the fact.
//
// Declared here rather than imported from the noise package so diff stays the
// lower layer and there is no import cycle.
type Suppressor interface {
	// Suppress reports whether a difference at this path should be dropped. The
	// values are the decoded JSON values, or strings for headers and status.
	//
	// Contract: it must return true only when the two values genuinely differ
	// and the difference is noise. Arrays are offered whole (before their
	// elements are compared) so an ordering rule can act, so returning true for
	// values that were already equal would inflate the suppressed count with
	// suppressions that never happened.
	Suppress(path string, primary, shadow any) bool
}

// Path prefixes. One namespace, so one glob syntax covers every rule:
//
//	/status
//	/headers/content-type
//	/body/items/0/updatedAt
const (
	PathStatus  = "/status"
	PathHeaders = "/headers"
	PathBody    = "/body"
)

type structural struct {
	suppressor Suppressor
	maxValue   int
}

// Options tunes the engine. The defaults are the useful ones.
type Options struct {
	Suppressor Suppressor
	// MaxValueLen truncates the rendered values in a Difference. The diff is for
	// reading; a 2MB JSON blob in a report helps nobody, and storing it is a PII
	// risk the scrubbing at the edge should not have to carry alone.
	MaxValueLen int
}

const defaultMaxValueLen = 200

func NewEngine(opts Options) Engine {
	if opts.MaxValueLen <= 0 {
		opts.MaxValueLen = defaultMaxValueLen
	}
	return &structural{suppressor: opts.Suppressor, maxValue: opts.MaxValueLen}
}

func (e *structural) Diff(p collector.Pair) Result {
	s := &sink{e: e, r: Result{CorrelID: p.CorrelID, Method: p.Primary.Method, URL: p.Primary.URL}}

	if p.Primary.Status != p.Shadow.Status {
		s.add(KindStatus, PathStatus, p.Primary.Status, p.Shadow.Status)
	}
	e.diffHeaders(p.Primary.ResHeaders, p.Shadow.ResHeaders, s)
	e.diffBodies(p, s)
	return s.r
}

// sink collects differences and is the single place the noise rules are
// consulted, so "suppressed" can never quietly mean "never looked at".
type sink struct {
	e *structural
	r Result
}

func (s *sink) add(kind Kind, path string, primary, shadow any) {
	if s.suppressed(path, primary, shadow) {
		return
	}
	s.r.Differences = append(s.r.Differences, Difference{
		Kind:    kind,
		Path:    path,
		Primary: s.e.render(primary),
		Shadow:  s.e.render(shadow),
	})
}

func (s *sink) suppressed(path string, primary, shadow any) bool {
	if s.e.suppressor == nil || !s.e.suppressor.Suppress(path, primary, shadow) {
		return false
	}
	s.r.Suppressed++
	return true
}

// --- headers -----------------------------------------------------------------

// Hop-by-hop and transport headers say nothing about whether the shadow behaves
// like the primary, and every one of them would differ on every request. They
// are excluded here rather than left to the noise rules so that a fresh install
// is usable before anyone writes a single rule.
var transportHeaders = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"transfer-encoding":   true,
	"content-length":      true, // implied by the body, which is compared properly
	"te":                  true,
	"trailer":             true,
	"upgrade":             true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
}

func (e *structural) diffHeaders(primary, shadow map[string][]string, s *sink) {
	names := map[string]bool{}
	for k := range primary {
		names[strings.ToLower(k)] = true
	}
	for k := range shadow {
		names[strings.ToLower(k)] = true
	}

	sorted := make([]string, 0, len(names))
	for name := range names {
		if !transportHeaders[name] {
			sorted = append(sorted, name)
		}
	}
	sort.Strings(sorted) // deterministic output: a diff that reorders itself is unreadable

	for _, name := range sorted {
		a, b := headerValue(primary, name), headerValue(shadow, name)
		if a != b {
			s.add(KindHeader, PathHeaders+"/"+name, a, b)
		}
	}
}

func headerValue(h map[string][]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return strings.Join(v, ", ")
		}
	}
	return ""
}

// --- bodies ------------------------------------------------------------------

func (e *structural) diffBodies(p collector.Pair, s *sink) {
	primaryJSON, okA := decodeJSON(p.Primary.ResBody)
	shadowJSON, okB := decodeJSON(p.Shadow.ResBody)

	if okA && okB {
		e.walk(PathBody, primaryJSON, shadowJSON, s)
		return
	}
	if okA != okB {
		// One side stopped being JSON. That is a real finding, and comparing it
		// structurally is impossible, so say exactly that.
		s.add(KindShape, PathBody, jsonness(okA), jsonness(okB))
		return
	}
	// Neither is JSON: fall back to bytes. No pretence of structure.
	if !bytes.Equal(p.Primary.ResBody, p.Shadow.ResBody) {
		s.add(KindBodyVal, PathBody, string(p.Primary.ResBody), string(p.Shadow.ResBody))
	}
}

func jsonness(ok bool) string {
	if ok {
		return "valid JSON"
	}
	return "not JSON"
}

// UseNumber keeps 1.0 and 1 distinguishable and avoids float64 mangling large
// integers — which would otherwise show up as a divergence the services never
// had.
func decodeJSON(body []byte) (any, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

func (e *structural) walk(path string, a, b any, s *sink) {
	if typeName(a) != typeName(b) {
		s.add(KindShape, path, typeName(a), typeName(b))
		return
	}

	switch av := a.(type) {
	case map[string]any:
		bv := b.(map[string]any)
		for _, key := range sortedKeys(av, bv) {
			childPath := path + "/" + escapeToken(key)
			left, hasLeft := av[key]
			right, hasRight := bv[key]
			switch {
			case hasLeft && !hasRight:
				s.add(KindBodyKey, childPath, left, nil)
			case !hasLeft && hasRight:
				s.add(KindBodyKey, childPath, nil, right)
			default:
				e.walk(childPath, left, right, s)
			}
		}
	case []any:
		bv := b.([]any)
		// The whole array is offered to the noise rules first: "these two
		// arrays are the same set in a different order" is a judgement only the
		// rules can make, and it cannot be made from per-index differences
		// after the fact.
		if s.suppressed(path, a, b) {
			return
		}
		if len(av) != len(bv) {
			// Length first: without it, one inserted element reports every
			// subsequent index as different and buries the actual change.
			s.add(KindShape, path, fmt.Sprintf("%d items", len(av)), fmt.Sprintf("%d items", len(bv)))
		}
		for i := 0; i < min(len(av), len(bv)); i++ {
			e.walk(path+"/"+strconv.Itoa(i), av[i], bv[i], s)
		}
	default:
		if !scalarEqual(a, b) {
			s.add(KindBodyVal, path, a, b)
		}
	}
}

func sortedKeys(a, b map[string]any) []string {
	seen := map[string]bool{}
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		seen[k] = true
		keys = append(keys, k)
	}
	for k := range b {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// Numbers compare numerically: 1 and 1.0 are the same value however the two
// services chose to serialise it.
func scalarEqual(a, b any) bool {
	an, aok := numberOf(a)
	bn, bok := numberOf(b)
	if aok && bok {
		return an == bn
	}
	return a == b
}

func numberOf(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	default:
		return 0, false
	}
}

// RFC 6901 escaping, so a key containing "/" cannot forge a path.
func escapeToken(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

func (e *structural) render(v any) string {
	var s string
	switch t := v.(type) {
	case nil:
		s = "(absent)"
	case string:
		s = t
	case json.Number:
		s = t.String()
	default:
		if b, err := json.Marshal(v); err == nil {
			s = string(b)
		} else {
			s = fmt.Sprintf("%v", v)
		}
	}
	if len(s) > e.maxValue {
		return s[:e.maxValue] + fmt.Sprintf("… (%d bytes)", len(s))
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
