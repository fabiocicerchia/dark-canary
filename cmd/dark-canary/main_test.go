package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fabiocicerchia/local-ai-lab/dark-canary/internal/collector"
	"github.com/fabiocicerchia/local-ai-lab/dark-canary/internal/noise"
	"github.com/fabiocicerchia/local-ai-lab/dark-canary/internal/report"
	"github.com/fabiocicerchia/local-ai-lab/dark-canary/internal/safety"
)

func testServer(t *testing.T, mutate func(*safety.Config)) *server {
	t.Helper()
	cfg := safety.Default()
	cfg.KillFile = filepath.Join(t.TempDir(), "kill") // absent unless a test creates it
	if mutate != nil {
		mutate(&cfg)
	}
	return newServer(cfg, noise.Default(), collector.Options{Timeout: time.Minute})
}

func post(t *testing.T, s *server, capture map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/captures", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.handleCapture(rec, req)
	return rec
}

func capture(path, id, body string, status int) map[string]any {
	return map[string]any{
		"path": path, "correl_id": id, "method": "GET", "uri": "/orders",
		"status": status, "res_body": []byte(body),
		"res_headers": map[string][]string{"Content-Type": {"application/json"}},
	}
}

// The end-to-end path the roadmap gates on: two captures in, one grouped
// divergence out.
func TestCapturesInReportOut(t *testing.T) {
	s := testServer(t, nil)

	if rec := post(t, s, capture(collector.PathPrimary, "c1", `{"total":10,"ok":true}`, 200)); rec.Code != http.StatusAccepted {
		t.Fatalf("primary capture rejected: %d %s", rec.Code, rec.Body)
	}
	if rec := post(t, s, capture(collector.PathShadow, "c1", `{"total":11,"ok":true}`, 200)); rec.Code != http.StatusAccepted {
		t.Fatalf("shadow capture rejected: %d %s", rec.Code, rec.Body)
	}

	// consume() runs in a goroutine in main; drive it directly here.
	select {
	case pair := <-s.buf.Pairs():
		s.agg.Add(s.engine.Diff(pair))
	case <-time.After(2 * time.Second):
		t.Fatal("the two captures never paired")
	}

	summary := s.agg.Summary()
	if summary.Pairs != 1 || summary.Divergent != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(summary.Groups) != 1 || summary.Groups[0].Path != "/body/total" {
		t.Fatalf("groups = %+v", summary.Groups)
	}
	if summary.Groups[0].Example.Primary != "10" || summary.Groups[0].Example.Shadow != "11" {
		t.Errorf("both sides must reach the report: %+v", summary.Groups[0].Example)
	}
}

func TestReportRendersAsTextAndJSON(t *testing.T) {
	s := testServer(t, nil)
	s.agg.Add(s.engine.Diff(collector.Pair{
		CorrelID: "c1",
		Primary:  collector.Capture{Status: 200, ResBody: []byte(`{"a":1}`)},
		Shadow:   collector.Capture{Status: 500, ResBody: []byte(`{"a":1}`)},
	}))

	rec := httptest.NewRecorder()
	s.handleReport(rec, httptest.NewRequest(http.MethodGet, "/report", nil))
	if !strings.Contains(rec.Body.String(), "/status") {
		t.Errorf("text report missing the divergence:\n%s", rec.Body)
	}

	rec = httptest.NewRecorder()
	s.handleReport(rec, httptest.NewRequest(http.MethodGet, "/report?format=json", nil))
	var summary report.Summary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("JSON report is not valid JSON: %v", err)
	}
	if len(summary.Groups) != 1 || summary.Groups[0].Severity != "high" {
		t.Errorf("summary = %+v", summary)
	}
}

// A misconfigured edge must not be able to turn into shadow writes: the
// collector refuses the capture even though the mirroring already happened.
func TestNonIdempotentCapturesAreRefusedUnderReadsOnly(t *testing.T) {
	s := testServer(t, nil)
	c := capture(collector.PathPrimary, "c1", `{}`, 200)
	c["method"] = "POST"

	if rec := post(t, s, c); rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
	if s.buf.Stats().Received != 0 {
		t.Error("the capture must be refused before it reaches the buffer")
	}
}

func TestWriteMirroringCanBeEnabledDeliberately(t *testing.T) {
	s := testServer(t, func(c *safety.Config) { c.MirrorReadsOnly = false })
	c := capture(collector.PathPrimary, "c1", `{}`, 200)
	c["method"] = "POST"

	if rec := post(t, s, c); rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202 when write mirroring is explicitly enabled", rec.Code)
	}
}

func TestKillSwitchStopsIngestion(t *testing.T) {
	s := testServer(t, nil)
	killFile := s.cfg.KillFile
	if err := os.WriteFile(killFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if rec := post(t, s, capture(collector.PathPrimary, "c1", `{}`, 200)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503 while the kill switch is engaged", rec.Code)
	}
	if s.buf.Stats().Received != 0 {
		t.Error("nothing may be stored while the kill switch is engaged")
	}
}

func TestBodiesAreScrubbedAndCappedOnArrival(t *testing.T) {
	s := testServer(t, func(c *safety.Config) {
		c.ScrubFields = []string{"email"}
		c.MaxBodyBytes = 32
	})

	long := `{"email":"a@b.c","pad":"` + strings.Repeat("x", 200) + `"}`
	if rec := post(t, s, capture(collector.PathPrimary, "c1", long, 200)); rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d", rec.Code)
	}
	if rec := post(t, s, capture(collector.PathShadow, "c1", long, 200)); rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d", rec.Code)
	}

	pair := <-s.buf.Pairs()
	if strings.Contains(string(pair.Primary.ResBody), "a@b.c") {
		t.Error("the collector must scrub on arrival, not trust the edge to have done it")
	}
	if len(pair.Primary.ResBody) > 32 {
		t.Errorf("body length %d exceeds the configured cap", len(pair.Primary.ResBody))
	}
}

func TestMalformedRequestsAreRejectedCleanly(t *testing.T) {
	s := testServer(t, nil)

	rec := httptest.NewRecorder()
	s.handleCapture(rec, httptest.NewRequest(http.MethodGet, "/captures", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /captures = %d, want 405", rec.Code)
	}

	rec = httptest.NewRecorder()
	s.handleCapture(rec, httptest.NewRequest(http.MethodPost, "/captures", strings.NewReader("{not json")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body = %d, want 400", rec.Code)
	}
}

// "Why is nothing being compared" is the first question of any shadow
// deployment, and /stats is the answer.
func TestStatsAccountForEveryCapture(t *testing.T) {
	s := testServer(t, nil)
	post(t, s, capture(collector.PathPrimary, "c1", `{}`, 200))
	post(t, s, capture(collector.PathPrimary, "orphan", `{}`, 200))
	post(t, s, capture(collector.PathShadow, "c1", `{}`, 200))

	rec := httptest.NewRecorder()
	s.handleStats(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))

	var got struct {
		Collector collector.Stats `json:"collector"`
		Kill      bool            `json:"kill_switch_engaged"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Collector.Received != 3 || got.Collector.Paired != 1 || got.Collector.Pending != 1 {
		t.Fatalf("stats = %+v", got.Collector)
	}
	if got.Kill {
		t.Error("the kill switch should not read as engaged")
	}
}

// --- gandalf finding: the ingest endpoint was reachable and unauthenticated ---

func TestLoopbackDetection(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8099": true,
		"localhost:8099": true,
		"[::1]:8099":     true,
		":8099":          false, // every interface
		"0.0.0.0:8099":   false,
		"10.0.0.5:8099":  false,
		"garbage":        false,
	} {
		if got := isLoopback(addr); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

// Captures carry production response bodies and /report serves them back, so
// every endpoint is behind the same check.
func TestTokenGuardsEveryEndpoint(t *testing.T) {
	s := testServer(t, nil)
	s.token = "s3cret"

	endpoints := map[string]func(http.ResponseWriter, *http.Request){
		"/captures": s.handleCapture,
		"/report":   s.handleReport,
		"/stats":    s.handleStats,
	}
	for path, handler := range endpoints {
		method := http.MethodGet
		if path == "/captures" {
			method = http.MethodPost
		}

		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(method, path, strings.NewReader("{}")))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token = %d, want 401", path, rec.Code)
		}

		rec = httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("X-Dark-Canary-Token", "wrong")
		handler(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with the wrong token = %d, want 401", path, rec.Code)
		}

		rec = httptest.NewRecorder()
		req = httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("X-Dark-Canary-Token", "s3cret")
		handler(rec, req)
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s with the right token was rejected", path)
		}
	}
}

func TestNoTokenIsFineOnLoopback(t *testing.T) {
	s := testServer(t, nil) // token empty, as when bound to 127.0.0.1
	rec := httptest.NewRecorder()
	s.handleStats(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200 — a loopback bind needs no shared secret", rec.Code)
	}
}
