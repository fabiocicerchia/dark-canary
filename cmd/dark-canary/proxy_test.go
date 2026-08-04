package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fabiocicerchia/dark-canary/internal/collector"
)

// upstream answers every request with body, and records what it was sent.
func upstream(t *testing.T, body string) (*httptest.Server, *[]string) {
	t.Helper()
	var got []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s, &got
}

func testProxy(t *testing.T, srv *server, primary, shadow *httptest.Server) *httptest.Server {
	t.Helper()
	pu, err := url.Parse(primary.URL)
	if err != nil {
		t.Fatal(err)
	}
	su, err := url.Parse(shadow.URL)
	if err != nil {
		t.Fatal(err)
	}
	p := httptest.NewServer(newProxy(srv, pu, su, 1.0, time.Second, 8))
	t.Cleanup(p.Close)
	return p
}

// The whole point of proxy mode: the client is served by the primary and never
// sees the shadow, and the pair reaches the diff engine without an edge.
func TestProxyServesPrimaryAndDiffsTheShadow(t *testing.T) {
	srv := testServer(t, nil)
	primary, _ := upstream(t, `{"total":10.004,"state":"paid"}`)
	shadow, shadowGot := upstream(t, `{"total":10.001,"state":"PAID"}`)
	front := testProxy(t, srv, primary, shadow)

	resp, err := http.Get(front.URL + "/orders/7")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	body, _ := io.ReadAll(resp.Body)

	if string(body) != `{"total":10.004,"state":"paid"}` {
		t.Fatalf("client got the wrong body: %s", body)
	}

	pair := waitForPair(t, srv)
	d := srv.engine.Diff(pair)
	if len(d.Differences) == 0 {
		t.Fatal("the primary/shadow difference never reached the diff engine")
	}
	if len(*shadowGot) != 1 || (*shadowGot)[0] != "GET /orders/7" {
		t.Fatalf("shadow saw %v, want one GET /orders/7", *shadowGot)
	}
}

// A shadow that is down, slow or wrong must be invisible to the client. This is
// the property that makes it safe to put in the request path at all.
func TestBrokenShadowNeverReachesTheClient(t *testing.T) {
	srv := testServer(t, nil)
	primary, _ := upstream(t, `{"ok":true}`)
	shadow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("the shadow is on fire")
	}))
	shadow.Config.ErrorLog = nil
	t.Cleanup(shadow.Close)
	front := testProxy(t, srv, primary, shadow)

	resp, err := http.Get(front.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"ok":true}` {
		t.Fatalf("the shadow leaked into the client's response: %d %s", resp.StatusCode, body)
	}
}

// Reads-only is enforced here, in the binary that does the routing — not in an
// edge script that may be an older build.
func TestReadsOnlyStopsTheShadowFromBeingWrittenTo(t *testing.T) {
	srv := testServer(t, nil)
	primary, _ := upstream(t, `{"ok":true}`)
	shadow, shadowGot := upstream(t, `{"ok":true}`)
	front := testProxy(t, srv, primary, shadow)

	resp, err := http.Post(front.URL+"/orders", "application/json", strings.NewReader(`{"buy":1}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	body, _ := io.ReadAll(resp.Body)

	// The client's write still succeeds — reads-only bounds the shadow, not
	// production.
	if string(body) != `{"ok":true}` {
		t.Fatalf("the client's POST was not served: %s", body)
	}
	time.Sleep(50 * time.Millisecond)
	if len(*shadowGot) != 0 {
		t.Fatalf("reads-only, but the shadow was sent %v — that is a real write", *shadowGot)
	}
}

// The kill switch stops mirroring without stopping traffic. A tool in the
// request path that fails closed at 3am is worse than the divergence it hunts.
func TestKillSwitchStopsMirroringNotServing(t *testing.T) {
	srv := testServer(t, nil)
	if f, err := os.Create(srv.cfg.KillFile); err == nil {
		_ = f.Close()
	} else {
		t.Fatal(err)
	}
	primary, _ := upstream(t, `{"ok":true}`)
	shadow, shadowGot := upstream(t, `{"ok":true}`)
	front := testProxy(t, srv, primary, shadow)

	resp, err := http.Get(front.URL + "/still-serving")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"ok":true}` {
		t.Fatalf("the kill switch took production down: %s", body)
	}
	time.Sleep(50 * time.Millisecond)
	if len(*shadowGot) != 0 {
		t.Fatalf("kill switch engaged, but the shadow was still mirrored: %v", *shadowGot)
	}
}

func waitForPair(t *testing.T, s *server) (p collector.Pair) {
	t.Helper()
	select {
	case pair := <-s.buf.Pairs():
		return pair
	case <-time.After(2 * time.Second):
		t.Fatalf("no pair after 2s; stats: %+v", s.buf.Stats())
	}
	return
}
