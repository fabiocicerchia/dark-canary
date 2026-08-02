// Package report groups diffs by frequency and severity.
//
// This is the part the eventual web UI renders, built first and deliberately
// headless: a list of individual diffs is unreadable at any real traffic volume,
// and the grouping — "this one path diverges on 94% of requests" — is the thing
// an operator acts on. If this is not useful as text, a UI will not save it.
package report

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/fabiocicerchia/local-ai-lab/dark-canary/internal/diff"
)

// Severity orders the kinds of divergence by how much they should worry
// somebody. A status change means the shadow behaves differently; a value
// change might be a rounding difference nobody has written a rule for yet.
type Severity int

const (
	SeverityLow Severity = iota + 1
	SeverityMedium
	SeverityHigh
)

func (s Severity) String() string {
	switch s {
	case SeverityHigh:
		return "high"
	case SeverityMedium:
		return "medium"
	default:
		return "low"
	}
}

func severityOf(kind diff.Kind) Severity {
	switch kind {
	case diff.KindStatus:
		return SeverityHigh // the shadow answered differently, full stop
	case diff.KindShape, diff.KindBodyKey:
		return SeverityMedium // a client that expects this field will break
	default:
		return SeverityLow
	}
}

// Group is one (kind, path) that diverged, with how often and an example.
type Group struct {
	Kind     diff.Kind `json:"kind"`
	Path     string    `json:"path"`
	Count    int       `json:"count"`
	Severity string    `json:"severity"`
	Rate     float64   `json:"rate"` // share of compared pairs showing this
	Example  Example   `json:"example"`
	FirstAt  time.Time `json:"first_at"`
	LastAt   time.Time `json:"last_at"`
}

type Example struct {
	CorrelID string `json:"correl_id"`
	Method   string `json:"method"`
	URL      string `json:"url"`
	Primary  string `json:"primary"`
	Shadow   string `json:"shadow"`
}

// Summary is the whole answer: how many pairs were compared, how many were
// identical, and what diverged.
type Summary struct {
	Pairs      int       `json:"pairs"`
	Identical  int       `json:"identical"`
	Divergent  int       `json:"divergent"`
	Suppressed int       `json:"suppressed"`
	Groups     []Group   `json:"groups"`
	Since      time.Time `json:"since"`
	Now        time.Time `json:"now"`
}

// AgreementRate is the number an operator quotes in the go/no-go meeting.
func (s Summary) AgreementRate() float64 {
	if s.Pairs == 0 {
		return 0
	}
	return float64(s.Identical) / float64(s.Pairs)
}

// Aggregator accumulates results. Safe for concurrent use: one diff worker
// writes while an HTTP handler reads the report.
type Aggregator struct {
	mu         sync.Mutex
	groups     map[string]*Group
	pairs      int
	identical  int
	suppressed int
	since      time.Time
	now        func() time.Time
}

func New(now func() time.Time) *Aggregator {
	if now == nil {
		now = time.Now
	}
	return &Aggregator{groups: map[string]*Group{}, since: now(), now: now}
}

func (a *Aggregator) Add(r diff.Result) {
	t := a.now()

	a.mu.Lock()
	defer a.mu.Unlock()

	a.pairs++
	a.suppressed += r.Suppressed
	if r.Identical() {
		a.identical++
		return
	}

	for _, d := range r.Differences {
		key := string(d.Kind) + " " + d.Path
		g, ok := a.groups[key]
		if !ok {
			g = &Group{
				Kind:     d.Kind,
				Path:     d.Path,
				Severity: severityOf(d.Kind).String(),
				FirstAt:  t,
				Example: Example{
					CorrelID: r.CorrelID, Method: r.Method, URL: r.URL,
					Primary: d.Primary, Shadow: d.Shadow,
				},
			}
			a.groups[key] = g
		}
		g.Count++
		g.LastAt = t
	}
}

// Summary ranks by severity first, then by how often it happens. A rare 500 on
// the shadow outranks a timestamp that differs on every single request.
func (a *Aggregator) Summary() Summary {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := Summary{
		Pairs:      a.pairs,
		Identical:  a.identical,
		Divergent:  a.pairs - a.identical,
		Suppressed: a.suppressed,
		Since:      a.since,
		Now:        a.now(),
		Groups:     make([]Group, 0, len(a.groups)),
	}
	for _, g := range a.groups {
		copied := *g
		if a.pairs > 0 {
			copied.Rate = float64(g.Count) / float64(a.pairs)
		}
		out.Groups = append(out.Groups, copied)
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		si, sj := rank(out.Groups[i].Severity), rank(out.Groups[j].Severity)
		if si != sj {
			return si > sj
		}
		if out.Groups[i].Count != out.Groups[j].Count {
			return out.Groups[i].Count > out.Groups[j].Count
		}
		return out.Groups[i].Path < out.Groups[j].Path
	})
	return out
}

func rank(s string) int {
	switch s {
	case "high":
		return 3
	case "medium":
		return 2
	default:
		return 1
	}
}

// Text renders the summary for a terminal. The UI comes last; until it does,
// this is the whole interface, and it has to be good enough to make the go/no-go
// call from.
func Text(w io.Writer, s Summary) error {
	fmt.Fprintf(w, "%d pairs compared over %s\n", s.Pairs, s.Now.Sub(s.Since).Round(time.Second))
	fmt.Fprintf(w, "%d identical (%.1f%% agreement), %d divergent, %d differences suppressed by noise rules\n\n",
		s.Identical, s.AgreementRate()*100, s.Divergent, s.Suppressed)

	if len(s.Groups) == 0 {
		if s.Pairs == 0 {
			fmt.Fprintln(w, "No pairs yet. Check that both paths are reporting: /stats shows what arrived.")
		} else {
			fmt.Fprintln(w, "No divergence.")
		}
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEVERITY\tCOUNT\tRATE\tKIND\tPATH\tPRIMARY → SHADOW")
	for _, g := range s.Groups {
		fmt.Fprintf(tw, "%s\t%d\t%.1f%%\t%s\t%s\t%s → %s\n",
			g.Severity, g.Count, g.Rate*100, g.Kind, g.Path,
			oneLine(g.Example.Primary), oneLine(g.Example.Shadow))
	}
	return tw.Flush()
}

func oneLine(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\t", " ")
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
