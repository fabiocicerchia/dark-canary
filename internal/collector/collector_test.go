package collector

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newTestBuffer(opts Options) (*Buffer, *clock) {
	c := &clock{t: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}
	opts.Now = c.now
	return New(opts), c
}

func capture(path, id string) Capture {
	return Capture{Path: path, CorrelID: id, Method: "GET", Status: 200}
}

func TestPairsBothWaysRound(t *testing.T) {
	for _, order := range [][2]string{{PathPrimary, PathShadow}, {PathShadow, PathPrimary}} {
		b, _ := newTestBuffer(Options{})
		b.Ingest(capture(order[0], "c1"))
		select {
		case <-b.Pairs():
			t.Fatal("a single side must not produce a pair")
		default:
		}
		b.Ingest(capture(order[1], "c1"))

		pair := <-b.Pairs()
		if pair.Primary.Path != PathPrimary || pair.Shadow.Path != PathShadow {
			t.Fatalf("sides swapped when ingested in order %v: %+v", order, pair)
		}
	}
}

func TestUnpairedCapturesExpireRatherThanAccumulate(t *testing.T) {
	b, c := newTestBuffer(Options{Timeout: time.Minute})
	b.Ingest(capture(PathPrimary, "c1"))

	c.add(30 * time.Second)
	if n := b.Sweep(c.now()); n != 0 {
		t.Fatalf("expired %d captures before the timeout", n)
	}
	c.add(31 * time.Second)
	if n := b.Sweep(c.now()); n != 1 {
		t.Fatalf("expired %d, want 1", n)
	}

	// The shadow finally arrives, far too late: it must not pair with a capture
	// that has already been written off.
	b.Ingest(capture(PathShadow, "c1"))
	select {
	case p := <-b.Pairs():
		t.Fatalf("paired with an expired capture: %+v", p)
	default:
	}
	if s := b.Stats(); s.Expired != 1 || s.Paired != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestBufferIsBoundedAndSaysWhatItDropped(t *testing.T) {
	b, _ := newTestBuffer(Options{MaxPending: 2, Timeout: time.Hour})
	b.Ingest(capture(PathPrimary, "a"))
	b.Ingest(capture(PathPrimary, "b"))
	b.Ingest(capture(PathPrimary, "c")) // evicts "a", the oldest

	if s := b.Stats(); s.Pending != 2 || s.Dropped != 1 {
		t.Fatalf("stats = %+v, want 2 pending and 1 dropped", s)
	}
	b.Ingest(capture(PathShadow, "a"))
	select {
	case p := <-b.Pairs():
		t.Fatalf("the evicted capture must not still pair: %+v", p)
	default:
	}
}

// A shadow that stops responding for an hour must not leave the process holding
// an hour of primary captures.
func TestASilentShadowDoesNotGrowMemory(t *testing.T) {
	b, c := newTestBuffer(Options{Timeout: time.Minute, MaxPending: 1000})
	for i := 0; i < 500; i++ {
		b.Ingest(Capture{Path: PathPrimary, CorrelID: string(rune('a'+i%26)) + time.Duration(i).String()})
		c.add(time.Second)
	}
	if s := b.Stats(); s.Pending > 61 {
		t.Fatalf("pending = %d after 500 captures over 500s with a 60s timeout", s.Pending)
	}
}

func TestMalformedCapturesAreDiscardedNotPaired(t *testing.T) {
	b, _ := newTestBuffer(Options{})
	b.Ingest(Capture{Path: PathPrimary})               // no correlation id
	b.Ingest(Capture{Path: "sideways", CorrelID: "c"}) // unknown path

	s := b.Stats()
	if s.Discarded != 2 || s.Pending != 0 {
		t.Fatalf("stats = %+v, want 2 discarded", s)
	}
	if s.Received != 2 {
		t.Errorf("received = %d — discarded captures must still be counted", s.Received)
	}
}

// Two captures from the same side share a correlation id when a client retries.
// Pairing one with itself would report a perfect match that never happened.
func TestTheSameSideTwiceDoesNotPairWithItself(t *testing.T) {
	b, _ := newTestBuffer(Options{})
	b.Ingest(capture(PathPrimary, "c1"))
	b.Ingest(capture(PathPrimary, "c1"))

	select {
	case p := <-b.Pairs():
		t.Fatalf("paired a capture with itself: %+v", p)
	default:
	}
	if s := b.Stats(); s.Pending != 1 {
		t.Fatalf("stats = %+v, want the newer capture kept", s)
	}
}

// Back-pressure must never reach the edge: if the diff engine stalls, pairs are
// dropped and counted, not queued forever.
func TestAFullBacklogDropsPairsInsteadOfBlocking(t *testing.T) {
	b, _ := newTestBuffer(Options{Buffer: 1})
	for _, id := range []string{"a", "b"} {
		b.Ingest(capture(PathPrimary, id))
		b.Ingest(capture(PathShadow, id))
	}

	done := make(chan struct{})
	go func() {
		b.Ingest(capture(PathPrimary, "c"))
		b.Ingest(capture(PathShadow, "c"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Ingest blocked on a full backlog")
	}

	s := b.Stats()
	if s.Dropped == 0 {
		t.Fatalf("stats = %+v, want the dropped pairs counted", s)
	}
	if s.Paired+s.Dropped != 3 {
		t.Errorf("stats = %+v: every pair must be either delivered or counted as dropped", s)
	}
}

func TestIngestSweepsSoABusyCollectorNeedsNoTicker(t *testing.T) {
	b, c := newTestBuffer(Options{Timeout: time.Minute})
	b.Ingest(capture(PathPrimary, "old"))
	c.add(2 * time.Minute)
	b.Ingest(capture(PathPrimary, "new"))

	if s := b.Stats(); s.Expired != 1 || s.Pending != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
