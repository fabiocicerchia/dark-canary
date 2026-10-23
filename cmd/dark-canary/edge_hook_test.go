package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fabiocicerchia/dark-canary/internal/collector"
)

// The collector's half of the edge-hook seam. The payloads are what
// lua/dark_canary.lua ships, pinned by lua/dark_canary_test.lua; each side
// tested alone passed while every edge capture decoded with nothing to
// compare, so this test reads the other side's bytes.
func TestEdgeHookPayloadsPairAndDiff(t *testing.T) {
	s := testServer(t, nil)
	s.token = "s3cret"

	for _, name := range []string{"edge_primary.json", "edge_shadow.json"} {
		payload, err := os.ReadFile(filepath.Join("..", "..", "lua", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/captures", strings.NewReader(string(payload)))
		req.Header.Set(tokenHeader, "s3cret")
		rec := httptest.NewRecorder()
		s.handleCapture(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s rejected: %d %s", name, rec.Code, rec.Body)
		}
	}

	var pair collector.Pair
	select {
	case pair = <-s.buf.Pairs():
	case <-time.After(2 * time.Second):
		t.Fatal("the two edge captures never paired")
	}
	for side, c := range map[string]collector.Capture{"primary": pair.Primary, "shadow": pair.Shadow} {
		if string(c.ReqBody) != `{"id":7}` {
			t.Errorf("%s req_body = %q", side, c.ReqBody)
		}
		if len(c.ResBody) == 0 {
			t.Errorf("%s res_body is empty", side)
		}
		if got := c.ResHeaders["vary"]; len(got) != 2 {
			t.Errorf("%s repeated header = %v, want both values", side, got)
		}
	}

	s.agg.Add(s.engine.Diff(pair))
	summary := s.agg.Summary()
	if summary.Divergent != 1 || len(summary.Groups) != 1 || summary.Groups[0].Path != "/body/total" {
		t.Fatalf("the edge captures did not reach the diff: %+v", summary)
	}
}
