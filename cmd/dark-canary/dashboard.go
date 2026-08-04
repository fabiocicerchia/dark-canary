package main

import (
	_ "embed"
	"net/http"
)

// One file, embedded in the binary: a dashboard that needs a build step, a CDN
// or a second container is a dashboard nobody installs. It polls /report and
// /stats, which is all a stats view can honestly be.
//
//go:embed dashboard.html
var dashboardHTML []byte

// The shell is served unauthenticated on purpose: it contains no data, and a
// browser cannot put a token header on a navigation. Everything it renders comes
// from /report and /stats, which do check the token — so the guard sits on the
// data, where it belongs, and the page is useless without it.
func (s *server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page loads nothing it did not ship with; say so, so a stray injection
	// has nowhere to fetch from.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
	_, _ = w.Write(dashboardHTML)
}
