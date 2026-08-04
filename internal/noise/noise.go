// Package noise is declarative suppression of differences that are expected and
// meaningless: timestamps, request IDs, ordering, float precision. This is the
// whole product — a diff tool that reports 100% divergence is worthless.
//
// Rules are data, loaded from a file, so an operator tunes them without a
// redeploy. Every suppression is counted and reported: suppression must never be
// mistakable for agreement.
package noise

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/fabiocicerchia/dark-canary/internal/diff"
)

// Rule suppresses a class of difference at a location.
type Rule struct {
	// Path is a glob over the diff's path namespace: /status,
	// /headers/<name>, /body/<json pointer>. "*" matches one segment, "**"
	// matches any number, so /body/**/updatedAt covers a timestamp wherever it
	// is nested.
	Path string `json:"path"`
	// Ignore drops differences here entirely (timestamps, request IDs).
	Ignore bool `json:"ignore"`
	// Normalise makes a narrower claim than Ignore: the values still have to
	// agree, just not exactly.
	//
	//   round:N   numbers agreeing to N decimal places
	//   sort      arrays with the same elements in a different order
	//   trim      strings differing only in surrounding whitespace
	//   lower     strings differing only in case
	//   len       strings/arrays of the same length (opaque ids, tokens)
	Normalise string `json:"normalise,omitempty"`
	// Reason is carried into the report. A rule nobody can explain is a rule
	// nobody dares delete.
	Reason string `json:"reason,omitempty"`
}

// Ruleset is loaded declaratively so operators tune it without redeploys.
type Ruleset struct {
	Rules []Rule `json:"rules"`
}

var _ diff.Suppressor = Ruleset{}

// Default is the ruleset a fresh install starts from: the differences every
// pair of servers has, that no operator ever wants to read about. Deliberately
// short — a long default ruleset hides real findings on day one.
func Default() Ruleset {
	return Ruleset{Rules: []Rule{
		{Path: "/headers/date", Ignore: true, Reason: "clock, not behaviour"},
		{Path: "/headers/server", Ignore: true, Reason: "identifies the build, not the response"},
		{Path: "/headers/x-request-id", Ignore: true, Reason: "unique per request by design"},
		{Path: "/headers/x-dark-canary-id", Ignore: true, Reason: "our own correlation header"},
		{Path: "/headers/set-cookie", Ignore: true, Reason: "session ids differ by construction"},
		{Path: "/headers/age", Ignore: true, Reason: "cache age, not content"},
		{Path: "/headers/etag", Ignore: true, Reason: "derived from bytes we compare directly"},
	}}
}

// Load reads a ruleset from a file.
//
// JSON, not YAML: it costs no dependency, and the rules are short enough that
// the difference is a `reason` field instead of a comment. ponytail: swap in
// gopkg.in/yaml.v3 if operators actually ask for comments and anchors.
func Load(path string) (Ruleset, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Ruleset{}, err
	}
	var rs Ruleset
	if err := json.Unmarshal(b, &rs); err != nil {
		return Ruleset{}, fmt.Errorf("%s: %w", path, err)
	}
	for i, r := range rs.Rules {
		if r.Path == "" {
			return Ruleset{}, fmt.Errorf("%s: rule %d has no path", path, i)
		}
		if !r.Ignore && r.Normalise == "" {
			return Ruleset{}, fmt.Errorf("%s: rule %d (%s) neither ignores nor normalises", path, i, r.Path)
		}
		if r.Normalise != "" {
			if _, err := parseNormalise(r.Normalise); err != nil {
				return Ruleset{}, fmt.Errorf("%s: rule %d (%s): %w", path, i, r.Path, err)
			}
		}
	}
	return rs, nil
}

// Merge appends rules to the defaults. Later rules win, so an operator can
// re-enable something the defaults hid.
func (rs Ruleset) Merge(other Ruleset) Ruleset {
	return Ruleset{Rules: append(append([]Rule{}, rs.Rules...), other.Rules...)}
}

// Match returns the last rule whose path matches, so later rules override
// earlier ones.
func (rs Ruleset) Match(path string) (Rule, bool) {
	var found Rule
	var ok bool
	for _, r := range rs.Rules {
		if matchGlob(r.Path, path) {
			found, ok = r, true
		}
	}
	return found, ok
}

// Suppress implements diff.Suppressor. It returns true only when the values
// genuinely differ and a rule says the difference does not matter.
func (rs Ruleset) Suppress(path string, primary, shadow any) bool {
	rule, ok := rs.Match(path)
	if !ok {
		return false
	}
	if rule.Ignore {
		return true
	}
	equalAlready, equalNormalised := compare(rule.Normalise, primary, shadow)
	return !equalAlready && equalNormalised
}

// Apply removes differences per the ruleset and returns how many were
// suppressed (report this, so nobody mistakes suppression for agreement).
//
// The engine already consults the ruleset while it compares, which is the only
// place value-level normalisation can happen. This is the post-hoc pass, for
// filtering a result produced without a ruleset — replaying stored diffs against
// a new rule, for instance, to see what it would have hidden.
func (rs Ruleset) Apply(r diff.Result) diff.Result {
	out := r
	out.Differences = nil
	for _, d := range r.Differences {
		rule, ok := rs.Match(d.Path)
		if ok && (rule.Ignore || suppressRendered(rule.Normalise, d.Primary, d.Shadow)) {
			out.Suppressed++
			continue
		}
		out.Differences = append(out.Differences, d)
	}
	return out
}

// --- normalisation -----------------------------------------------------------

type normaliser struct {
	kind      string
	precision int
}

func parseNormalise(spec string) (normaliser, error) {
	switch {
	case spec == "sort", spec == "trim", spec == "lower", spec == "len":
		return normaliser{kind: spec}, nil
	case strings.HasPrefix(spec, "round:"):
		n, err := strconv.Atoi(strings.TrimPrefix(spec, "round:"))
		if err != nil || n < 0 || n > 15 {
			return normaliser{}, fmt.Errorf("round: needs 0..15 decimal places, got %q", spec)
		}
		return normaliser{kind: "round", precision: n}, nil
	default:
		return normaliser{}, fmt.Errorf("unknown normalisation %q", spec)
	}
}

// compare reports whether the values were already equal, and whether they are
// equal once normalised. The caller needs both to avoid counting a suppression
// where there was never a difference.
func compare(spec string, a, b any) (equalAlready, equalNormalised bool) {
	n, err := parseNormalise(spec)
	if err != nil {
		return false, false
	}
	if canonical(a) == canonical(b) {
		return true, true
	}
	return false, n.equal(a, b)
}

func (n normaliser) equal(a, b any) bool {
	switch n.kind {
	case "round":
		af, aok := toFloat(a)
		bf, bok := toFloat(b)
		if !aok || !bok {
			return false
		}
		shift := math.Pow(10, float64(n.precision))
		return math.Round(af*shift) == math.Round(bf*shift)
	case "sort":
		as, aok := toSortedStrings(a)
		bs, bok := toSortedStrings(b)
		if !aok || !bok || len(as) != len(bs) {
			return false
		}
		for i := range as {
			if as[i] != bs[i] {
				return false
			}
		}
		return true
	case "trim":
		as, aok := a.(string)
		bs, bok := b.(string)
		return aok && bok && strings.TrimSpace(as) == strings.TrimSpace(bs)
	case "lower":
		as, aok := a.(string)
		bs, bok := b.(string)
		return aok && bok && strings.EqualFold(as, bs)
	case "len":
		al, aok := lengthOf(a)
		bl, bok := lengthOf(b)
		return aok && bok && al == bl
	default:
		return false
	}
}

// Post-hoc variant: the engine renders values to strings, so only the
// string-shaped normalisations survive. Anything needing the original value
// (sort, and round on a number that rendered oddly) has to happen during the
// diff, which is why the engine consults the ruleset directly.
func suppressRendered(spec, a, b string) bool {
	if spec == "" || a == b {
		return false
	}
	n, err := parseNormalise(spec)
	if err != nil {
		return false
	}
	switch n.kind {
	case "round":
		return n.equal(json.Number(a), json.Number(b))
	default:
		return n.equal(a, b)
	}
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case float64:
		return t, true
	case int:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func toSortedStrings(v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		out = append(out, canonical(item))
	}
	sort.Strings(out)
	return out, true
}

func lengthOf(v any) (int, bool) {
	switch t := v.(type) {
	case string:
		return len(t), true
	case []any:
		return len(t), true
	default:
		return 0, false
	}
}

// A stable rendering for equality: map key order must not decide whether two
// values match.
func canonical(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// --- path matching -----------------------------------------------------------

// matchGlob matches segment by segment: "*" is one segment, "**" is any number.
// Case-insensitive, because header names are.
func matchGlob(pattern, path string) bool {
	return matchSegments(split(strings.ToLower(pattern)), split(strings.ToLower(path)))
}

func split(s string) []string {
	parts := strings.Split(strings.TrimPrefix(s, "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		return nil
	}
	return parts
}

func matchSegments(pattern, path []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	if pattern[0] == "**" {
		// Match zero or more segments here, then the rest of the pattern.
		for i := 0; i <= len(path); i++ {
			if matchSegments(pattern[1:], path[i:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	if pattern[0] != "*" && pattern[0] != path[0] {
		return false
	}
	return matchSegments(pattern[1:], path[1:])
}
