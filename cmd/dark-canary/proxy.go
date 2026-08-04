package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/fabiocicerchia/dark-canary/internal/collector"
)

// Proxy mode makes dark-canary the edge itself, with no nginx and no Lua: the
// request is forwarded to the primary and the primary's response is what the
// client gets. A copy is fired at the shadow and the shadow's answer is read,
// captured and thrown away. The shadow cannot slow the client down, cannot fail
// the request, and with reads-only on cannot be sent anything that writes.
//
// Both captures are handed straight to the in-process buffer — same binary, so
// there is no /captures round trip and no correlation header to propagate.
type proxy struct {
	srv    *server
	fwd    *httputil.ReverseProxy
	shadow *url.URL
	client *http.Client
	rate   float64
	// slots bounds shadow requests in flight. A shadow that has stopped
	// answering must not accumulate goroutines behind the primary path, so a
	// full channel means this request is simply not mirrored — counted, never
	// queued, exactly like a full pair buffer.
	slots   chan struct{}
	n       atomic.Uint64
	skipped atomic.Uint64
}

type correlKey struct{}

func newProxy(srv *server, primary, shadow *url.URL, rate float64, timeout time.Duration, inflight int) *proxy {
	p := &proxy{
		srv:    srv,
		shadow: shadow,
		client: &http.Client{
			Timeout: timeout,
			// The shadow is a dead end: its redirects are its own business and
			// following them would mirror traffic somewhere nobody configured.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		rate:  rate,
		slots: make(chan struct{}, inflight),
	}
	p.fwd = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(primary)
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
		},
		ModifyResponse: p.capturePrimary,
	}
	return p
}

// mirror decides once per request, before anything is forwarded, so the primary
// and the shadow capture can never disagree about whether this request is being
// compared — half a pair is worse than none, it expires and skews the stats.
func (p *proxy) mirror(r *http.Request) bool {
	// The kill switch stops mirroring; it must never stop serving. Whatever is
	// wrong at 3am, this proxy is in the request path and taking production down
	// is not an available response.
	if p.srv.kill.Engaged() {
		return false
	}
	if p.srv.cfg.MirrorReadsOnly && !idempotent[r.Method] {
		return false
	}
	if p.rate < 1 && rand.Float64() >= p.rate { //nolint:gosec // sampling, not security
		return false
	}
	select {
	case p.slots <- struct{}{}:
		return true
	default:
		p.skipped.Add(1)
		return false
	}
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.mirror(r) {
		p.fwd.ServeHTTP(w, r)
		return
	}

	// A slot is held from here until the shadow goroutine finishes or bails.
	correl := strconv.FormatUint(p.n.Add(1), 36)

	// The body has to serve two requests, so it is read once and replayed. The
	// cap is the capture cap: a body larger than we would ever store is a body
	// this request will not be compared on.
	var body []byte
	if r.Body != nil {
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, int64(p.srv.cfg.MaxBodyBytes)+1))
		if err != nil || len(body) > p.srv.cfg.MaxBodyBytes {
			<-p.slots
			p.skipped.Add(1)
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
			p.fwd.ServeHTTP(w, r)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	}

	// Cloned before the primary is served, because ReverseProxy owns r from the
	// next line on and reading it from another goroutine is a data race.
	sreq := r.Clone(context.WithoutCancel(r.Context()))
	go p.mirrorToShadow(sreq, body, correl)

	p.fwd.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlKey{}, correl)))
}

// capturePrimary tees the primary's response as it streams to the client rather
// than buffering it: this is the path the user is waiting on, and a proxy that
// holds whole responses in memory breaks streaming and large downloads.
func (p *proxy) capturePrimary(resp *http.Response) error {
	correl, ok := resp.Request.Context().Value(correlKey{}).(string)
	if !ok {
		return nil
	}
	c := collector.Capture{
		Path:       "primary",
		CorrelID:   correl,
		Method:     resp.Request.Method,
		URL:        resp.Request.URL.RequestURI(),
		ReqHeaders: resp.Request.Header,
		Status:     resp.StatusCode,
		ResHeaders: resp.Header,
	}
	resp.Body = &teeBody{
		ReadCloser: resp.Body,
		max:        p.srv.cfg.MaxBodyBytes,
		done: func(b []byte) {
			c.ResBody = b
			p.srv.ingest(c)
		},
	}
	return nil
}

func (p *proxy) mirrorToShadow(r *http.Request, body []byte, correl string) {
	defer func() { <-p.slots }()

	r.URL.Scheme, r.URL.Host = p.shadow.Scheme, p.shadow.Host
	r.RequestURI = ""
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))

	resp, err := p.client.Do(r)
	if err != nil {
		// A shadow that refuses the request is a finding, but not one this tool
		// can express as a diff — the pair will expire and show up in /stats.
		return
	}
	defer resp.Body.Close() //nolint:errcheck // response is discarded by design

	resBody, _ := io.ReadAll(io.LimitReader(resp.Body, int64(p.srv.cfg.MaxBodyBytes)))
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection is reusable

	p.srv.ingest(collector.Capture{
		Path:       "shadow",
		CorrelID:   correl,
		Method:     r.Method,
		URL:        r.URL.RequestURI(),
		ReqHeaders: r.Header,
		ReqBody:    body,
		Status:     resp.StatusCode,
		ResHeaders: resp.Header,
		ResBody:    resBody,
	})
}

// teeBody passes the body through untouched while keeping the first max bytes.
// The capture is handed over on Close, which ReverseProxy always calls, rather
// than on EOF, which a client that hangs up early never produces.
type teeBody struct {
	io.ReadCloser
	buf  bytes.Buffer
	max  int
	done func([]byte)
}

func (t *teeBody) Read(b []byte) (int, error) {
	n, err := t.ReadCloser.Read(b)
	if n > 0 && t.buf.Len() < t.max {
		t.buf.Write(b[:min(n, t.max-t.buf.Len())])
	}
	return n, err
}

func (t *teeBody) Close() error {
	if t.done != nil {
		t.done(t.buf.Bytes())
		t.done = nil
	}
	return t.ReadCloser.Close()
}

// parseUpstream keeps a typo out of the request path: an upstream with no scheme
// or no host silently proxies nowhere, and the first sign would be production
// 502s.
func parseUpstream(flagName, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("-%s %q: %w", flagName, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("-%s %q: need an http:// or https:// URL", flagName, raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("-%s %q: no host", flagName, raw)
	}
	return u, nil
}
