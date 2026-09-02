// Command dark-canary compares production traffic mirrored to a shadow
// deployment against the primary response, and reports genuine divergence.
//
// It is a collector, a diff engine and a report — no UI. The report is text and
// JSON on purpose: if the grouping is not useful in a terminal, a web UI will
// not rescue it, and the roadmap says get one real service diffed end to end
// before building any UI.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fabiocicerchia/dark-canary/internal/collector"
	"github.com/fabiocicerchia/dark-canary/internal/diff"
	"github.com/fabiocicerchia/dark-canary/internal/noise"
	"github.com/fabiocicerchia/dark-canary/internal/report"
	"github.com/fabiocicerchia/dark-canary/internal/safety"
)

const (
	// tokenHeader carries the shared secret on every guarded endpoint. The
	// dashboard and the Helm chart's NOTES.txt spell it out too, so it is a
	// protocol value, not a detail of the check.
	tokenHeader = "X-Dark-Canary-Token"

	// readHeaderTimeout bounds a client that opens a connection and dawdles over
	// the request line. Both listeners get it; the proxy one is in the request
	// path, so a slowloris there is a production outage rather than a nuisance.
	readHeaderTimeout = 5 * time.Second

	// shutdownGrace is how long in-flight requests have to finish after SIGTERM
	// before the listeners are cut. Shorter than a typical pod terminationGrace
	// so the process is gone before the kill.
	shutdownGrace = 5 * time.Second
)

// Only idempotent methods are mirrored. Shared by the proxy's mirror decision
// and the collector's refusal to accept a capture of anything else.
var idempotent = map[string]bool{http.MethodGet: true, http.MethodHead: true, http.MethodOptions: true}

func main() {
	if err := run(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "dark-canary:", err)
		os.Exit(1)
	}
}

// shutdownOn drains both listeners once the signal context is cancelled, giving
// in-flight requests shutdownGrace to finish before they are cut.
func shutdownOn(ctx context.Context, servers ...*http.Server) {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(shutdownCtx)
	}
}

func routes(srv *server) {
	http.Handle("/captures", http.HandlerFunc(srv.handleCapture))
	http.Handle("/report", http.HandlerFunc(srv.handleReport))
	http.Handle("/stats", http.HandlerFunc(srv.handleStats))
	http.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Nothing to do if the probe hung up mid-write; the status is already sent.
		_, _ = fmt.Fprintln(w, "ok")
	}))
	http.Handle("/", http.HandlerFunc(srv.handleDashboard))
}

func run() error {
	o := parseFlags()

	cfg, warn, err := o.safetyConfig()
	if warn != nil {
		// Supported, but never silent.
		fmt.Fprintln(os.Stderr, "dark-canary: WARNING:", warn)
	}
	if err != nil {
		return err
	}

	rules, err := o.rules()
	if err != nil {
		return err
	}
	primaryURL, shadowURL, err := o.upstreams()
	if err != nil {
		return err
	}

	srv := newServer(cfg, rules, collector.Options{Timeout: o.timeout, MaxPending: o.maxPending})
	srv.token = o.token

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go srv.consume(ctx)
	go srv.sweep(ctx, o.timeout)
	if o.interval > 0 {
		go srv.periodic(ctx, o.interval)
	}
	routes(srv)

	httpSrv := &http.Server{Addr: o.listen, ReadHeaderTimeout: readHeaderTimeout}
	proxySrv := &http.Server{Addr: o.proxyListen, ReadHeaderTimeout: readHeaderTimeout}
	// Buffered, so whichever listener dies first records why without waiting for
	// anyone to be reading.
	fatal := make(chan error, 2)
	go shutdownOn(ctx, httpSrv, proxySrv)

	// Proxy mode is opt-in: without -primary this is the collector it has always
	// been, fed by something else at the edge.
	if primaryURL != nil {
		proxySrv.Handler = newProxy(srv, primaryURL, shadowURL, cfg.SampleRate, o.shadowTO, o.inflight)
		go func() {
			fmt.Fprintf(os.Stderr, "dark-canary proxying %s \u2192 %s (shadow %s) \u2014 sample=%g\n",
				o.proxyListen, primaryURL, shadowURL, cfg.SampleRate)
			if err := proxySrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				// The proxy is the request path: failing to bind it is fatal,
				// and must exit non-zero. Reporting it only on stderr would
				// leave a supervisor believing the process stopped cleanly.
				fatal <- fmt.Errorf("proxy: %w", err)
				stop()
			}
		}()
	}

	fmt.Fprintf(os.Stderr, "dark-canary listening on %s — reads-only=%v kill-file=%s\n",
		o.listen, cfg.MirrorReadsOnly, cfg.KillFile)
	err = httpSrv.ListenAndServe()

	// A listener that never came up has no report to give, and printing an empty
	// one ahead of the real error reads as "nothing is being compared" when the
	// actual answer is "the port was taken".
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	select {
	case ferr := <-fatal:
		return ferr
	default:
	}

	// The report is the reason the process existed; print it on the way out.
	_ = report.Text(os.Stderr, srv.agg.Summary())
	return err
}

type server struct {
	cfg      safety.Config
	kill     safety.KillSwitch
	scrubber *safety.Scrubber
	buf      *collector.Buffer
	engine   diff.Engine
	agg      *report.Aggregator
	token    string // empty = no auth, only permitted on a loopback bind
}

// A bind with no host (":8099") listens on every interface, so only an explicit
// loopback host counts.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Constant-time so the endpoint cannot be used as an oracle to recover the
// token one byte at a time.
func (s *server) authorised(r *http.Request) bool {
	if s.token == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(r.Header.Get(tokenHeader)), []byte(s.token)) == 1
}

func newServer(cfg safety.Config, rules noise.Ruleset, opts collector.Options) *server {
	return &server{
		cfg:      cfg,
		kill:     &safety.FileKillSwitch{Path: cfg.KillFile},
		scrubber: safety.NewScrubber(cfg.ScrubFields),
		buf:      collector.New(opts),
		engine:   diff.NewEngine(diff.Options{Suppressor: rules}),
		agg:      report.New(nil),
	}
}

func (s *server) handleCapture(w http.ResponseWriter, r *http.Request) {
	if !s.authorised(r) {
		http.Error(w, "unauthorised", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	// The kill switch is checked here as well as at the edge: the edge may be an
	// older build, or someone may be replaying captures by hand.
	if s.kill.Engaged() {
		http.Error(w, "kill switch engaged", http.StatusServiceUnavailable)
		return
	}

	// Two caps, deliberately: the reader refuses to buffer more than the body
	// limit allows even for a lying Content-Length.
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(s.cfg.MaxBodyBytes)*2+1024))
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}

	var c collector.Capture
	if err := json.Unmarshal(body, &c); err != nil {
		http.Error(w, "malformed capture", http.StatusBadRequest)
		return
	}

	if s.cfg.MirrorReadsOnly && c.Method != "" && !idempotent[strings.ToUpper(c.Method)] {
		// The edge should never have mirrored this. Refusing it here means a
		// misconfigured edge cannot quietly turn into shadow writes.
		http.Error(w, "reads-only: refusing a capture of a non-idempotent request", http.StatusForbidden)
		return
	}

	s.ingest(c)
	w.WriteHeader(http.StatusAccepted)
}

// ingest is the one door into the buffer: scrub, cap, store. Proxy mode goes
// through it too, so a capture this process made itself gets the same treatment
// as one that arrived over the wire — there is no path that stores a raw body.
func (s *server) ingest(c collector.Capture) {
	c.ReqBody = s.limit(s.scrubber.Body(c.ReqBody))
	c.ResBody = s.limit(s.scrubber.Body(c.ResBody))
	for name, values := range c.ReqHeaders {
		c.ReqHeaders[name] = s.scrubber.Header(name, values)
	}
	for name, values := range c.ResHeaders {
		c.ResHeaders[name] = s.scrubber.Header(name, values)
	}
	s.buf.Ingest(c)
}

func (s *server) limit(b []byte) []byte {
	if len(b) > s.cfg.MaxBodyBytes {
		return b[:s.cfg.MaxBodyBytes]
	}
	return b
}

func (s *server) consume(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case pair := <-s.buf.Pairs():
			s.agg.Add(s.engine.Diff(pair))
		}
	}
}

// Ingest sweeps too, so this only matters when traffic stops — which is exactly
// when a pending capture would otherwise sit in memory indefinitely.
func (s *server) sweep(ctx context.Context, timeout time.Duration) {
	ticker := time.NewTicker(timeout)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			s.buf.Sweep(t)
		}
	}
}

func (s *server) periodic(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = report.Text(os.Stderr, s.agg.Summary())
		}
	}
}

func (s *server) handleReport(w http.ResponseWriter, r *http.Request) {
	// The report quotes production response bodies, so it is guarded exactly
	// like ingest.
	if !s.authorised(r) {
		http.Error(w, "unauthorised", http.StatusUnauthorized)
		return
	}
	summary := s.agg.Summary()
	if r.URL.Query().Get("format") == "json" || strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(summary)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_ = report.Text(w, summary)
}

// The first question of any shadow deployment is "why is nothing being
// compared", and this is the answer: what arrived, what paired, what expired.
func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	if !s.authorised(r) {
		http.Error(w, "unauthorised", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(struct {
		Collector collector.Stats `json:"collector"`
		Kill      bool            `json:"kill_switch_engaged"`
	}{s.buf.Stats(), s.kill.Engaged()})
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
