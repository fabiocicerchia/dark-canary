// Package demo is a service with a shadow that is almost, but not quite, the
// same as its primary.
//
// WHY IT EXISTS: every component of dark-canary is tested in isolation, and
// nothing has ever mirrored one service against another end to end. That is
// the thing all the other tests exist to make possible, and it is the one
// nobody has watched happen.
//
// A synthetic load generator against a tidy demo service would prove very
// little — the issue this closes says so directly. Tidy payloads are exactly
// the ones noise suppression does not need tuning for. So this service is
// deliberately *untidy*, in the four ways a real pair of deployments differs:
//
//   - IDs that are unique per request by construction
//   - timestamps and durations that are never equal
//   - collections whose order is not stable between two processes
//   - floats computed in a different order and so differing in the last place
//
// and then, underneath all of that, ONE REAL BUG that a diff must still find:
// the shadow drops a field for a particular class of input. The whole design
// claim is that noise suppression makes that bug legible. If the harness
// reports the bug and nothing else, the claim holds; if it drowns, it does not.
package demo

import (
	"encoding/json"
	"fmt"
	// nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used
	// Deliberate, and crypto/rand would break it: the point of this service is
	// SEEDED, reproducible noise that differs between the two sides. Nothing
	// here is a token, an id anyone trusts, or a security decision — it is
	// synthetic payload jitter in a test fixture, and unpredictability is the
	// opposite of what it needs.
	"math/rand" //nolint:gosec // test fixture noise, seeded on purpose
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Options configures one side of the pair.
type Options struct {
	// Name is echoed in a header, so a capture can be traced back to the
	// process that produced it while debugging the harness itself.
	Name string
	// Bug turns on the deliberate divergence. The shadow runs with it; the
	// primary does not. It is scoped to one endpoint and one input class, the
	// way a real regression is — a shadow that differs on every response is
	// not a test of anything.
	Bug bool
	// Seed makes the noise reproducible per process while still differing
	// BETWEEN the two processes, which is what makes it noise rather than a
	// difference either side could avoid.
	Seed int64
	// Latency is added to every response, so the shadow can be made slower
	// than the primary — which is normal, and must not read as a divergence.
	Latency time.Duration
}

type service struct {
	opts  Options
	rng   *rand.Rand
	calls atomic.Int64
}

// Handler returns the service. Safe for concurrent use: the harness drives it
// with many goroutines and a service that raced would fail the run for the
// wrong reason.
func Handler(opts Options) http.Handler {
	s := &service{opts: opts, rng: rand.New(rand.NewSource(opts.Seed))}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/orders/", s.order)
	mux.HandleFunc("/api/catalogue", s.catalogue)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	return s.wrap(mux)
}

// wrap adds the per-response noise every server has and no two servers share.
func (s *service) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := s.calls.Add(1)
		if s.opts.Latency > 0 {
			time.Sleep(s.opts.Latency)
		}
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("X-Request-Id", fmt.Sprintf("%s-%d-%d", s.opts.Name, s.opts.Seed, n))
		h.Set("X-Served-By", s.opts.Name)
		h.Set("Date", time.Now().UTC().Format(http.TimeFormat))
		next.ServeHTTP(w, r)
	})
}

// order is where the bug lives.
//
// The response carries a total computed by summing line items. The primary
// sums them in the order given; the shadow sums them sorted, which for floats
// changes the last bits — that is NOISE, and `normalise: round:2` is the
// correct answer to it.
//
// The BUG is different in kind: for an order with a discount, the shadow omits
// the `discount` field entirely. No rounding rule can hide a missing field,
// and none should.
func (s *service) order(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/orders/")
	if id == "" {
		http.Error(w, `{"error":"no order id"}`, http.StatusBadRequest)
		return
	}
	// Deterministic from the id, so both sides compute the same order — the
	// only differences must be the ones this file introduces on purpose.
	n, _ := strconv.Atoi(strings.TrimLeft(id, "abcdefghijklmnopqrstuvwxyz-"))
	lines := make([]float64, 0, 4)
	for i := 0; i < 3+n%3; i++ {
		lines = append(lines, float64((n*7+i*13)%997)/3.0)
	}

	total := 0.0
	if s.opts.Bug {
		// Same numbers, added in a different order: float addition is not
		// associative, so this is the last-place difference every pair of
		// implementations has.
		for i := len(lines) - 1; i >= 0; i-- {
			total += lines[i]
		}
	} else {
		for _, v := range lines {
			total += v
		}
	}

	body := map[string]any{
		"id":          id,
		"lines":       lines,
		"total":       total,
		"currency":    "EUR",
		"served_at":   time.Now().UTC().Format(time.RFC3339Nano),
		"duration_ms": s.rng.Intn(40) + 5,
		"trace_id":    fmt.Sprintf("%016x", s.rng.Uint64()),
	}
	// THE BUG. Discounted orders lose the field on the shadow — the field a
	// finance team would notice a week later.
	discounted := n%4 == 0
	if discounted && !s.opts.Bug {
		body["discount"] = 5.0
	} else if discounted {
		// shadow: field absent
		_ = discounted
	}
	_ = json.NewEncoder(w).Encode(body)
}

// catalogue returns a collection whose order is not stable between processes.
// A diff that reports this is a diff nobody will read past the first page.
func (s *service) catalogue(w http.ResponseWriter, _ *http.Request) {
	items := []map[string]any{
		{"sku": "A-100", "stock": 4},
		{"sku": "B-200", "stock": 0},
		{"sku": "C-300", "stock": 17},
		{"sku": "D-400", "stock": 2},
	}
	s.rng.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items":       items,
		"generated":   time.Now().UTC().Format(time.RFC3339Nano),
		"cursor":      fmt.Sprintf("%016x", s.rng.Uint64()),
		"duration_ms": s.rng.Intn(20) + 2,
	})
}
