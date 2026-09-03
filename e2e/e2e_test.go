//go:build e2e

// End to end: a real primary, a real shadow, a real dark-canary process
// between them, and a load loop through the proxy.
//
// WHY THIS IS A SEPARATE BUILD TAG. It compiles and execs the binary, binds
// four ports and runs for tens of seconds. `make test` is what you run on
// every save; this is what you run before believing the product works.
// `make e2e` does both steps.
//
// WHAT IT ASSERTS, and why each one is here rather than in a unit test:
//
//   - the pair forms at all, across two processes and a proxy
//   - noise suppression makes ONE real divergence legible among four kinds of
//     synthetic noise — the entire design claim, and untestable on tidy data
//   - the user's response is byte-identical to the primary's, under load,
//     with the shadow slow and then deliberately broken
//   - the kill file stops mirroring within a request or two, and the user path
//     does not notice
//   - -sample actually samples, and unsampled requests are still served
//   - unpaired captures do not accumulate without bound
package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fabiocicerchia/dark-canary/e2e/demo"
)

const (
	// Long enough to form hundreds of pairs, short enough that nobody skips
	// the target. The sustained run the issue also asks for is the standalone
	// demo-service binary, not this.
	loadRequests = 400
	concurrency  = 8
)

// ---- plumbing --------------------------------------------------------------

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

type harness struct {
	proxy     string // where the "user" sends requests
	collector string // where /report and /stats live
	killFile  string
	primary   *httptest.Server
	shadow    *httptest.Server
	cmd       *exec.Cmd
	stderr    *strings.Builder
}

// start brings up both services and the binary, and waits for all three.
func start(t *testing.T, extra ...string) *harness {
	t.Helper()
	h := &harness{stderr: &strings.Builder{}}

	// Different seeds on purpose: identical seeds would make the noise
	// identical, and a harness that suppressed nothing would still pass.
	h.primary = httptest.NewServer(demo.Handler(demo.Options{Name: "primary", Seed: 1}))
	h.shadow = httptest.NewServer(demo.Handler(demo.Options{
		Name: "shadow", Seed: 2, Bug: true, Latency: 5 * time.Millisecond,
	}))
	t.Cleanup(h.primary.Close)
	t.Cleanup(h.shadow.Close)

	h.proxy = freePort(t)
	h.collector = freePort(t)
	h.killFile = filepath.Join(t.TempDir(), "STOP")

	bin, err := filepath.Abs(filepath.Join("..", "bin", "dark-canary"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("%s not built — run `make build` first (or use `make e2e`)", bin)
	}
	rules, err := filepath.Abs("noise.yaml")
	if err != nil {
		t.Fatal(err)
	}

	// -sample 1 unless a test overrides it. The DEFAULT is 0.01, which is the
	// right default for production and would make this harness assert almost
	// nothing — the first run of it mirrored four requests in four hundred.
	args := append([]string{
		"-primary", h.primary.URL,
		"-shadow", h.shadow.URL,
		"-proxy-listen", h.proxy,
		"-listen", h.collector,
		"-rules", rules,
		"-kill-file", h.killFile,
		"-correlate-timeout", "5s",
		"-sample", "1",
	}, extra...)
	h.cmd = exec.Command(bin, args...)
	h.cmd.Stderr = h.stderr
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = h.cmd.Process.Kill()
		_, _ = h.cmd.Process.Wait()
		if t.Failed() {
			t.Logf("dark-canary stderr:\n%s", h.stderr.String())
		}
	})

	h.waitFor(t, "http://"+h.collector+"/healthz")
	h.waitFor(t, "http://"+h.proxy+"/healthz")
	return h
}

func (h *harness) waitFor(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx // fixed loopback URL in a test
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never came up\nstderr:\n%s", url, h.stderr.String())
}

// get sends one request as the user would, and returns status and body.
func (h *harness) get(t *testing.T, path string) (int, string) {
	t.Helper()
	resp, err := http.Get("http://" + h.proxy + path) //nolint:noctx // loopback, in a test
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (h *harness) drive(t *testing.T, n int) {
	t.Helper()
	var wg sync.WaitGroup
	work := make(chan int, n)
	for i := 0; i < n; i++ {
		work <- i
	}
	close(work)
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				path := fmt.Sprintf("/api/orders/ord-%d", i)
				if i%5 == 0 {
					path = "/api/catalogue"
				}
				if code, _ := h.get(t, path); code != http.StatusOK {
					t.Errorf("GET %s returned %d", path, code)
					return
				}
			}
		}()
	}
	wg.Wait()
}

type summary struct {
	Pairs      int `json:"pairs"`
	Identical  int `json:"identical"`
	Divergent  int `json:"divergent"`
	Suppressed int `json:"suppressed"`
	Groups     []struct {
		Kind    string  `json:"kind"`
		Path    string  `json:"path"`
		Count   int     `json:"count"`
		Rate    float64 `json:"rate"`
		Example struct {
			URL     string `json:"url"`
			Primary string `json:"primary"`
			Shadow  string `json:"shadow"`
		} `json:"example"`
	} `json:"groups"`
}

// report polls until the collector has seen at least `want` pairs, so the
// assertions do not race the correlator's own goroutine.
func (h *harness) report(t *testing.T, want int) summary {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var s summary
	for time.Now().Before(deadline) {
		// ?format=json: /report renders text for humans by default, and a
		// harness that silently failed to parse it would report zero pairs and
		// look like a broken product.
		resp, err := http.Get("http://" + h.collector + "/report?format=json") //nolint:noctx // test
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if json.Unmarshal(b, &s) == nil && s.Pairs >= want {
				return s
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("only %d pair(s) formed, wanted %d\nstderr:\n%s", s.Pairs, want, h.stderr.String())
	return s
}

// stats returns the collector's own counters.
func (h *harness) stats(t *testing.T) map[string]float64 {
	t.Helper()
	resp, err := http.Get("http://" + h.collector + "/stats") //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw struct {
		Collector map[string]float64 `json:"collector"`
		Engaged   bool               `json:"kill_switch_engaged"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	return raw.Collector
}

func (h *harness) killEngaged(t *testing.T) bool {
	t.Helper()
	resp, err := http.Get("http://" + h.collector + "/stats") //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw struct {
		Engaged bool `json:"kill_switch_engaged"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	return raw.Engaged
}

// waitQuiet blocks until no new captures have arrived for two polls, so a
// "before" snapshot is not racing traffic that is still landing.
func (h *harness) waitQuiet(t *testing.T) map[string]float64 {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	last := h.stats(t)
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		now := h.stats(t)
		if now["received"] == last["received"] {
			return now
		}
		last = now
	}
	t.Fatal("captures never stopped arriving")
	return nil
}

// ---- the run ---------------------------------------------------------------

// TestTheRealDivergenceIsFoundAndTheNoiseIsNot is the whole product in one
// test. The shadow differs from the primary on every single response — ids,
// timestamps, durations, collection order and the last bits of a float — and
// underneath all of that it drops a field on one class of order.
//
// A diff tool that reports the noise is worthless; one that suppresses the bug
// along with it is worse. This asserts both directions.
func TestTheRealDivergenceIsFoundAndTheNoiseIsNot(t *testing.T) {
	h := start(t)
	h.drive(t, loadRequests)
	s := h.report(t, loadRequests/2)

	if s.Suppressed == 0 {
		t.Fatal("nothing was suppressed, so the noise rules did not fire at all")
	}

	// The finding, and only the finding.
	var found bool
	var extras []string
	for _, g := range s.Groups {
		if strings.Contains(g.Path, "discount") {
			found = true
			if g.Count == 0 {
				t.Errorf("group %s reported with a zero count", g.Path)
			}
			continue
		}
		extras = append(extras, fmt.Sprintf("%s %s (%d, %.0f%%)  %q vs %q",
			g.Kind, g.Path, g.Count, g.Rate*100, g.Example.Primary, g.Example.Shadow))
	}
	if !found {
		t.Fatalf("the shadow's dropped `discount` field was not reported.\n"+
			"pairs=%d identical=%d divergent=%d suppressed=%d\ngroups: %v",
			s.Pairs, s.Identical, s.Divergent, s.Suppressed, s.Groups)
	}
	if len(extras) > 0 {
		// Not fatal on its own — a new noise source is a rule to add, not a
		// broken product — but it has to be visible, with enough detail to
		// write the rule from.
		t.Errorf("noise reached the report as a finding; each of these needs a "+
			"rule in e2e/noise.yaml with a reason, or is a second real bug:\n  %s",
			strings.Join(extras, "\n  "))
	}

	// Only a quarter of the orders are discounted, and one request in five
	// goes to the catalogue. A divergence rate near 100% would mean the noise
	// rules had stopped working and this test was passing by accident.
	if s.Divergent >= s.Pairs {
		t.Errorf("every pair diverged (%d/%d) — suppression is not doing anything",
			s.Divergent, s.Pairs)
	}
	if s.Identical == 0 {
		t.Error("no pair was identical after suppression; the noise rules are too narrow")
	}
	t.Logf("pairs=%d identical=%d divergent=%d suppressed=%d",
		s.Pairs, s.Identical, s.Divergent, s.Suppressed)
}

// TestTheUserGetsThePrimarysAnswerWhateverTheShadowDoes is the safety claim.
// One path returns to the user; the other is a dead end. The shadow here is
// slow AND wrong, which is the state this whole tool exists to survive.
func TestTheUserGetsThePrimarysAnswerWhateverTheShadowDoes(t *testing.T) {
	h := start(t)

	// Same request through the proxy and straight to the primary. The bodies
	// carry a timestamp and a trace id, so compare the fields that are the
	// service's actual answer.
	for i := 0; i < 40; i++ {
		path := fmt.Sprintf("/api/orders/ord-%d", i)
		code, viaProxy := h.get(t, path)
		if code != http.StatusOK {
			t.Fatalf("proxy returned %d", code)
		}
		direct, err := http.Get(h.primary.URL + path) //nolint:noctx // loopback test
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(direct.Body)
		_ = direct.Body.Close()

		var a, c map[string]any
		if err := json.Unmarshal([]byte(viaProxy), &a); err != nil {
			t.Fatalf("proxied body is not JSON: %v\n%s", err, viaProxy)
		}
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"id", "total", "lines", "currency", "discount"} {
			if fmt.Sprint(a[k]) != fmt.Sprint(c[k]) {
				t.Fatalf("%s: proxied %s=%v, primary %s=%v — the user got the wrong answer",
					path, k, a[k], k, c[k])
			}
		}
	}
}

// TestTheKillFileStopsMirroringAndTheUserNeverNotices exercises the control an
// operator reaches for at 3am. It has to work while traffic is flowing, not
// only at startup, and it must not cost a single user request.
//
// Asserted on captures RECEIVED rather than on pairs formed. Pairing runs on
// its own goroutine and lags the traffic, so a pair count taken the moment the
// kill file lands keeps rising afterwards from work that was already in
// flight — the first version of this test failed on exactly that, reporting 84
// "new" pairs that were the backlog. Received is the number that answers the
// actual question: did anything get mirrored after the switch was thrown.
func TestTheKillFileStopsMirroringAndTheUserNeverNotices(t *testing.T) {
	h := start(t)
	h.drive(t, 80)
	h.report(t, 20)
	before := h.waitQuiet(t)

	if err := os.WriteFile(h.killFile, []byte("stop"), 0o600); err != nil {
		t.Fatal(err)
	}

	// FileKillSwitch caches its answer for a second so the hot path costs a
	// clock read rather than a stat per request — "everything stops within
	// TTL", as it says. The first version of this test asserted an INSTANT
	// stop, drove 60 requests inside that second, and duly found 120 captures
	// still arriving. That is the documented contract working, not a bug, so
	// the window is waited out rather than asserted away.
	time.Sleep(2 * time.Second)
	if !h.killEngaged(t) {
		t.Fatal("the kill file is on disk and /stats says the switch is not engaged")
	}
	before = h.waitQuiet(t)

	// Serving must continue. Every one of these is a user request, and a
	// non-200 here is the failure mode that makes people refuse to install a
	// shadow-traffic tool at all.
	for i := 0; i < 60; i++ {
		if code, body := h.get(t, fmt.Sprintf("/api/orders/kill-%d", i)); code != http.StatusOK {
			t.Fatalf("request %d after the kill file: %d %s", i, code, body)
		}
	}

	time.Sleep(500 * time.Millisecond) // longer than any in-flight mirror needs
	after := h.stats(t)
	if after["received"] > before["received"] {
		t.Errorf("%v capture(s) arrived past the kill switch's TTL, from 60 requests",
			after["received"]-before["received"])
	}
}

// TestSamplingMirrorsAFractionAndServesTheRest. -sample is the other control
// that gets used under pressure: turn the mirroring down, keep serving.
func TestSamplingMirrorsAFractionAndServesTheRest(t *testing.T) {
	h := start(t, "-sample", "0.25")
	const n = 200
	h.drive(t, n)
	s := h.report(t, 5)

	// Sampling is probabilistic, so this is a band, not an equality. The
	// assertion that matters is that it is neither 0 (mirroring off) nor n
	// (the flag ignored).
	if s.Pairs == 0 {
		t.Fatal("sample=0.25 mirrored nothing")
	}
	if float64(s.Pairs) > 0.75*float64(n) {
		t.Errorf("sample=0.25 mirrored %d of %d — the flag is not being applied", s.Pairs, n)
	}
	t.Logf("sample=0.25 → %d pair(s) from %d requests", s.Pairs, n)
}

// TestUnpairedCapturesDoNotAccumulate. A shadow that stops answering is the
// normal way a soak run ends up out of memory: captures arrive from the
// primary side, never pair, and are held forever.
func TestUnpairedCapturesDoNotAccumulate(t *testing.T) {
	h := start(t, "-correlate-timeout", "1s")
	h.drive(t, 60)
	h.report(t, 10)

	h.shadow.Close() // the shadow is gone; primary captures now have no partner
	for i := 0; i < 100; i++ {
		if code, _ := h.get(t, fmt.Sprintf("/api/orders/lonely-%d", i)); code != http.StatusOK {
			t.Fatalf("the user path broke when the shadow went away: %d", code)
		}
	}

	// One timeout plus a sweep interval. Then pending must have come back down.
	time.Sleep(4 * time.Second)
	stats := h.stats(t)
	t.Logf("stats after 100 unpairable requests: %v", stats)
	if stats["pending"] > 50 {
		t.Errorf("%v capture(s) still pending after the correlate timeout — "+
			"a soak run would grow without bound", stats["pending"])
	}
	if stats["expired"] == 0 {
		t.Error("nothing expired, so the sweep is not running and `pending` is only low by luck")
	}
}
