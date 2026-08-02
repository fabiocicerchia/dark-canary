package safety

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultsAreTheSafePosture(t *testing.T) {
	c := Default()
	if !c.MirrorReadsOnly {
		t.Error("write mirroring must never be the default: it does real writes on the shadow")
	}
	if c.SampleRate > 0.05 {
		t.Errorf("sample rate defaults to %v — it must default low to bound load amplification", c.SampleRate)
	}
	if c.KillFile == "" {
		t.Error("there must be a kill switch path out of the box")
	}
}

func TestValidateRejectsNonsenseAndWarnsAboutWriteMirroring(t *testing.T) {
	c := Default()
	c.SampleRate = 2
	if err := c.Validate(); err == nil {
		t.Error("a sample rate above 1 must be rejected")
	}

	c = Default()
	c.MaxBodyBytes = 0
	if err := c.Validate(); err == nil {
		t.Error("an unbounded body size must be rejected")
	}

	c = Default()
	c.MirrorReadsOnly = false
	err := c.Validate()
	if !errors.Is(err, ErrWriteMirroringEnabled) {
		t.Fatalf("enabling write mirroring must produce the warning, got %v", err)
	}
	if !strings.Contains(err.Error(), "REAL WRITES") {
		t.Errorf("the warning must be impossible to skim past: %v", err)
	}
}

// The kill switch has to work when the control plane is the broken thing, so it
// is a file and nothing else.
func TestKillSwitchIsJustAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kill")
	now := time.Now()
	k := &FileKillSwitch{Path: path, TTL: time.Second, Now: func() time.Time { return now }}

	if k.Engaged() {
		t.Fatal("no file means running")
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if k.Engaged() {
		t.Error("the previous answer is cached for TTL")
	}
	now = now.Add(2 * time.Second)
	if !k.Engaged() {
		t.Error("past the TTL, touching the file must stop everything")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if k.Engaged() {
		t.Error("removing the file must resume")
	}
}

func TestNoKillFileConfiguredIsNotEngaged(t *testing.T) {
	if (&FileKillSwitch{}).Engaged() {
		t.Error("an unconfigured kill switch must not stop the world")
	}
}

func TestScrubberRedactsConfiguredFields(t *testing.T) {
	s := NewScrubber([]string{"email", "ssn", "amount"})
	got := string(s.Body([]byte(`{"email":"a@b.c","id":7,"nested":{"ssn":"123-45-6789"},"amount":12.5,"keep":"yes"}`)))

	for _, secret := range []string{"a@b.c", "123-45-6789", "12.5"} {
		if strings.Contains(got, secret) {
			t.Errorf("%q survived scrubbing: %s", secret, got)
		}
	}
	if !strings.Contains(got, `"keep":"yes"`) || !strings.Contains(got, `"id":7`) {
		t.Errorf("unconfigured fields must be untouched: %s", got)
	}
	if strings.Count(got, Redacted) != 3 {
		t.Errorf("want three redactions, got %s", got)
	}
}

func TestScrubberHandlesEscapedQuotesWithoutRunningOn(t *testing.T) {
	s := NewScrubber([]string{"note"})
	got := string(s.Body([]byte(`{"note":"say \"hi\"","after":"visible"}`)))
	if !strings.Contains(got, `"after":"visible"`) {
		t.Errorf("an escaped quote must not swallow the rest of the document: %s", got)
	}
}

// Credentials are redacted whether or not anyone remembered to configure them.
func TestAuthorizationHeadersAreAlwaysRedacted(t *testing.T) {
	s := NewScrubber(nil)
	for _, name := range []string{"Authorization", "cookie", "Set-Cookie", "X-API-Key"} {
		if got := s.Header(name, []string{"secret"}); got[0] != Redacted {
			t.Errorf("%s was not redacted: %v", name, got)
		}
	}
	if got := s.Header("Content-Type", []string{"application/json"}); got[0] != "application/json" {
		t.Errorf("ordinary headers must survive: %v", got)
	}
}

func TestNilScrubberIsSafe(t *testing.T) {
	var s *Scrubber
	if got := string(s.Body([]byte(`{"a":1}`))); got != `{"a":1}` {
		t.Errorf("got %s", got)
	}
	if got := s.Header("Authorization", []string{"secret"}); got[0] != Redacted {
		t.Errorf("even a nil scrubber must redact credentials: %v", got)
	}
}
