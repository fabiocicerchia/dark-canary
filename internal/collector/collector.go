// Package collector buffers request/response pairs captured from the primary and
// shadow paths and hands matched pairs to the diff engine.
package collector

import (
	"sync"
	"time"
)

// Capture is one request/response observed on one path.
type Capture struct {
	Path       string              `json:"path"`      // "primary" or "shadow"
	CorrelID   string              `json:"correl_id"` // ties the two paths together (injected by the Lua mirror hook)
	Method     string              `json:"method"`
	URL        string              `json:"uri"`
	ReqHeaders map[string][]string `json:"req_headers"`
	ReqBody    []byte              `json:"req_body"` // subject to PII scrubbing BEFORE it ever reaches here
	Status     int                 `json:"status"`
	ResHeaders map[string][]string `json:"res_headers"`
	ResBody    []byte              `json:"res_body"`
	Latency    time.Duration       `json:"-"`
	At         time.Time           `json:"-"`
}

// Pair is a correlated primary+shadow observation ready to diff.
type Pair struct {
	CorrelID string
	Primary  Capture
	Shadow   Capture
}

// Collector correlates captures into pairs.
type Collector interface {
	Ingest(c Capture)
	// Pairs emits correlated pairs once both paths have reported (or a pair times out).
	Pairs() <-chan Pair
}

const (
	PathPrimary = "primary"
	PathShadow  = "shadow"
)

// Stats is the honest account of what happened to every capture. Half the
// operational questions about a shadow deployment ("why is nothing being
// diffed?") are answered here, so nothing is allowed to vanish silently.
type Stats struct {
	Received  int64 `json:"received"`
	Paired    int64 `json:"paired"`
	Pending   int64 `json:"pending"`
	Expired   int64 `json:"expired"`   // one side never arrived within the timeout
	Dropped   int64 `json:"dropped"`   // buffer or backlog was full
	Discarded int64 `json:"discarded"` // malformed: no correlation id, unknown path
	Backlog   int64 `json:"backlog"`   // pairs the diff engine has not consumed yet
}

type Options struct {
	// Timeout is how long a lone capture waits for its partner. The shadow can
	// be slower than the primary — that is often the finding — so this wants to
	// be generous relative to the shadow's p99, not the primary's.
	Timeout time.Duration
	// MaxPending bounds memory. A shadow that stops responding must not turn
	// into an unbounded buffer of primary captures.
	MaxPending int
	// Buffer is the depth of the Pairs channel.
	Buffer int
	// Now is injectable so the expiry logic is testable without sleeping.
	Now func() time.Time
}

const (
	defaultTimeout    = 30 * time.Second
	defaultMaxPending = 10_000
	defaultBuffer     = 256
)

type pending struct {
	capture Capture
	at      time.Time
}

// Buffer is an in-memory correlating collector. One process, one buffer: two
// captures only pair if they reach the same instance, which is worth knowing
// before putting two replicas behind a load balancer.
type Buffer struct {
	mu      sync.Mutex
	pending map[string]*pending
	order   []string // correlation ids in insertion order
	stats   Stats

	opts  Options
	pairs chan Pair
}

var _ Collector = (*Buffer)(nil)

func New(opts Options) *Buffer {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.MaxPending <= 0 {
		opts.MaxPending = defaultMaxPending
	}
	if opts.Buffer <= 0 {
		opts.Buffer = defaultBuffer
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Buffer{
		pending: make(map[string]*pending),
		opts:    opts,
		pairs:   make(chan Pair, opts.Buffer),
	}
}

func (b *Buffer) Pairs() <-chan Pair { return b.pairs }

func (b *Buffer) Ingest(c Capture) {
	now := b.opts.Now()

	b.mu.Lock()
	b.stats.Received++

	if c.CorrelID == "" || (c.Path != PathPrimary && c.Path != PathShadow) {
		b.stats.Discarded++
		b.mu.Unlock()
		return
	}
	if c.At.IsZero() {
		c.At = now
	}

	b.expire(now)

	if held, ok := b.pending[c.CorrelID]; ok {
		if held.capture.Path == c.Path {
			// The same side twice: a retry, or two requests sharing a
			// correlation id. Keep the newer one rather than pairing a capture
			// with itself and reporting a perfect match.
			held.capture = c
			held.at = now
			b.mu.Unlock()
			return
		}
		b.remove(c.CorrelID)
		pair := Pair{CorrelID: c.CorrelID}
		if c.Path == PathPrimary {
			pair.Primary, pair.Shadow = c, held.capture
		} else {
			pair.Primary, pair.Shadow = held.capture, c
		}
		b.stats.Paired++
		b.mu.Unlock()

		// Non-blocking: if the diff engine is behind, drop the pair and say so.
		// Blocking here would push back onto the capture path, which is the one
		// place this tool must never affect.
		select {
		case b.pairs <- pair:
		default:
			b.mu.Lock()
			b.stats.Dropped++
			b.stats.Paired--
			b.mu.Unlock()
		}
		return
	}

	if len(b.pending) >= b.opts.MaxPending {
		b.evictOldest()
	}
	b.pending[c.CorrelID] = &pending{capture: c, at: now}
	b.order = append(b.order, c.CorrelID)
	b.mu.Unlock()
}

// Sweep drops captures whose partner never arrived. Ingest also sweeps, so a
// busy collector needs no ticker; an idle one does, or its last few captures sit
// in memory until traffic resumes.
func (b *Buffer) Sweep(now time.Time) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.expire(now)
}

// caller holds the lock
func (b *Buffer) expire(now time.Time) int {
	cutoff := now.Add(-b.opts.Timeout)
	expired := 0
	// order is insertion-ordered, so anything expired is a prefix of it.
	for _, id := range b.order {
		p, ok := b.pending[id]
		if !ok {
			continue
		}
		if p.at.After(cutoff) {
			break
		}
		delete(b.pending, id)
		b.stats.Expired++
		expired++
	}
	if expired > 0 {
		b.compactOrder()
	}
	return expired
}

// caller holds the lock
func (b *Buffer) evictOldest() {
	for _, id := range b.order {
		if _, ok := b.pending[id]; ok {
			delete(b.pending, id)
			b.stats.Dropped++
			b.compactOrder()
			return
		}
	}
}

// caller holds the lock
func (b *Buffer) remove(id string) {
	delete(b.pending, id)
	b.compactOrder()
}

// caller holds the lock
//
// ponytail: O(n) compaction of the order slice, run only when something is
// actually removed. A ring buffer or an intrusive list would make it O(1);
// revisit if a profile ever shows it, which it will not at 1% sampling.
func (b *Buffer) compactOrder() {
	kept := b.order[:0]
	for _, id := range b.order {
		if _, ok := b.pending[id]; ok {
			kept = append(kept, id)
		}
	}
	b.order = kept
}

func (b *Buffer) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.stats
	s.Pending = int64(len(b.pending))
	s.Backlog = int64(len(b.pairs))
	return s
}
