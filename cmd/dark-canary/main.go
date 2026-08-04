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
	"flag"
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

func main() {
	if err := run(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "dark-canary:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		// Loopback by default. Captures carry response bodies from production
		// traffic, and /report serves them back — binding every interface with
		// no auth would make this the softest target on the network.
		listen     = flag.String("listen", "127.0.0.1:8099", "address to accept captures on")
		token      = flag.String("token", "", "shared secret required in X-Dark-Canary-Token; mandatory when -listen is not loopback")
		rulesPath  = flag.String("rules", "", "noise ruleset (YAML); merged on top of the built-in defaults")
		timeout    = flag.Duration("correlate-timeout", 30*time.Second, "how long a lone capture waits for its partner")
		maxPending = flag.Int("max-pending", 10_000, "bound on unpaired captures held in memory")
		killFile   = flag.String("kill-file", safety.Default().KillFile, "path whose existence stops all processing")
		maxBody    = flag.Int("max-body", safety.Default().MaxBodyBytes, "largest capture body accepted, in bytes")
		writes     = flag.Bool("allow-write-mirroring", false, "accept captures of non-idempotent requests (REAL WRITES on the shadow)")
		scrub      = flag.String("scrub", "", "comma-separated body fields to redact on arrival")
		interval   = flag.Duration("report-every", 0, "print the report to stderr on this interval (0 = only on request)")

		// Proxy mode: dark-canary does the routing itself, no nginx, no Lua.
		primary     = flag.String("primary", "", "upstream that answers the client, e.g. http://127.0.0.1:9001 (enables proxy mode)")
		shadow      = flag.String("shadow", "", "upstream mirrored to and discarded, e.g. http://127.0.0.1:9002")
		proxyListen = flag.String("proxy-listen", "127.0.0.1:8080", "address to serve proxied traffic on")
		sample      = flag.Float64("sample", safety.Default().SampleRate, "fraction of eligible requests mirrored to the shadow")
		shadowTO    = flag.Duration("shadow-timeout", 10*time.Second, "how long a mirrored request may take before it is abandoned")
		inflight    = flag.Int("max-inflight", 64, "bound on mirrored requests in flight; over this, requests are served but not mirrored")
	)
	flag.Parse()

	cfg := safety.Default()
	cfg.KillFile = *killFile
	cfg.MaxBodyBytes = *maxBody
	cfg.MirrorReadsOnly = !*writes
	cfg.ScrubFields = splitList(*scrub)
	cfg.SampleRate = *sample

	if err := cfg.Validate(); err != nil {
		if !errors.Is(err, safety.ErrWriteMirroringEnabled) {
			return err
		}
		// Supported, but never silent.
		fmt.Fprintln(os.Stderr, "dark-canary: WARNING:", err)
	}

	// Refuse to be reachable and unauthenticated at the same time. Failing at
	// startup is the only place this can be caught before the data is exposed.
	if !isLoopback(*listen) && *token == "" {
		return fmt.Errorf("-listen %s is not loopback: set -token, or bind to 127.0.0.1", *listen)
	}

	rules := noise.Default()
	if *rulesPath != "" {
		loaded, err := noise.Load(*rulesPath)
		if err != nil {
			return err
		}
		rules = rules.Merge(loaded)
	}

	srv := newServer(cfg, rules, collector.Options{Timeout: *timeout, MaxPending: *maxPending})
	srv.token = *token

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go srv.consume(ctx)
	go srv.sweep(ctx, *timeout)
	if *interval > 0 {
		go srv.periodic(ctx, *interval)
	}

	http.Handle("/captures", http.HandlerFunc(srv.handleCapture))
	http.Handle("/report", http.HandlerFunc(srv.handleReport))
	http.Handle("/stats", http.HandlerFunc(srv.handleStats))
	http.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	}))

	httpSrv := &http.Server{Addr: *listen, ReadHeaderTimeout: 5 * time.Second}
	proxySrv := &http.Server{Addr: *proxyListen, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		_ = proxySrv.Shutdown(shutdownCtx)
	}()

	// Proxy mode is opt-in: without -primary this is the collector it has always
	// been, fed by something else at the edge.
	if *primary != "" {
		if *shadow == "" {
			return fmt.Errorf("-primary needs -shadow: with nothing to mirror to there is nothing to compare")
		}
		primaryURL, err := parseUpstream("primary", *primary)
		if err != nil {
			return err
		}
		shadowURL, err := parseUpstream("shadow", *shadow)
		if err != nil {
			return err
		}
		proxySrv.Handler = newProxy(srv, primaryURL, shadowURL, cfg.SampleRate, *shadowTO, *inflight)
		go func() {
			fmt.Fprintf(os.Stderr, "dark-canary proxying %s → %s (shadow %s) — sample=%g\n",
				*proxyListen, primaryURL, shadowURL, cfg.SampleRate)
			if err := proxySrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintln(os.Stderr, "dark-canary: proxy:", err)
				stop()
			}
		}()
	} else if *shadow != "" {
		return fmt.Errorf("-shadow needs -primary: proxy mode routes to both or neither")
	}

	fmt.Fprintf(os.Stderr, "dark-canary listening on %s — reads-only=%v kill-file=%s\n",
		*listen, cfg.MirrorReadsOnly, cfg.KillFile)
	err := httpSrv.ListenAndServe()

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
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Dark-Canary-Token")), []byte(s.token)) == 1
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

var idempotent = map[string]bool{http.MethodGet: true, http.MethodHead: true, http.MethodOptions: true}

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
