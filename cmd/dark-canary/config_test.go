package main

import (
	"errors"
	"os"
	"testing"

	"github.com/fabiocicerchia/dark-canary/internal/safety"
)

// defaults mirrors what parseFlags produces with no flags set, so a test can
// change one thing and assert on that thing.
func defaults() *options {
	return &options{
		listen:      "127.0.0.1:8099",
		proxyListen: "127.0.0.1:8080",
		killFile:    safety.Default().KillFile,
		maxBody:     safety.Default().MaxBodyBytes,
		sample:      safety.Default().SampleRate,
	}
}

// The process must refuse to be reachable and unauthenticated at the same time:
// /report serves production response bodies back.
func TestReachableWithoutATokenIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		listen  string
		token   string
		refused bool
	}{
		{"loopback, no token", "127.0.0.1:8099", "", false},
		{"localhost, no token", "localhost:8099", "", false},
		{"every interface, no token", "0.0.0.0:8099", "", true},
		{"every interface, token", "0.0.0.0:8099", "s3cret", false},
		{"bare port is every interface", ":8099", "", true},
		{"routable address, no token", "10.1.2.3:8099", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := defaults()
			o.listen, o.token = tc.listen, tc.token
			_, _, err := o.safetyConfig()
			if tc.refused && err == nil {
				t.Fatalf("-listen %s with token %q was accepted; it must refuse", tc.listen, tc.token)
			}
			if !tc.refused && err != nil {
				t.Fatalf("-listen %s with token %q was refused: %v", tc.listen, tc.token, err)
			}
			if tc.refused && exitCode(err) != exitUsage {
				t.Errorf("refusal exited %d, want %d", exitCode(err), exitUsage)
			}
		})
	}
}

// Enabling write mirroring is a warning, never a licence: the loopback refusal
// still has to fire, and the warning still has to be announced alongside it.
func TestWriteMirroringWarnsWithoutExcusingTheLoopbackRefusal(t *testing.T) {
	o := defaults()
	o.writes = true
	o.listen = "0.0.0.0:8099"

	cfg, warn, err := o.safetyConfig()

	if warn == nil || !errors.Is(warn, safety.ErrWriteMirroringEnabled) {
		t.Errorf("warn = %v, want ErrWriteMirroringEnabled", warn)
	}
	if err == nil {
		t.Fatal("a non-loopback bind with no token was accepted because write mirroring was on")
	}
	if cfg.MirrorReadsOnly {
		t.Error("-allow-write-mirroring did not clear MirrorReadsOnly")
	}
}

// A guard rail that cannot hold is a startup failure, not a runtime surprise.
func TestUnsafeValuesAreRefusedAsUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*options)
	}{
		{"sample above 1", func(o *options) { o.sample = 5 }},
		{"sample below 0", func(o *options) { o.sample = -0.1 }},
		{"no body cap", func(o *options) { o.maxBody = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := defaults()
			tc.mutate(o)
			if _, _, err := o.safetyConfig(); err == nil {
				t.Fatal("accepted")
			} else if exitCode(err) != exitUsage {
				t.Errorf("exited %d, want %d", exitCode(err), exitUsage)
			}
		})
	}
}

// Proxy mode routes to both upstreams or to neither. Routing to one of the two
// would serve traffic with nothing to compare it against.
func TestUpstreamsAreBothOrNeither(t *testing.T) {
	for _, tc := range []struct {
		name          string
		primary       string
		shadow        string
		wantErr       bool
		wantProxyMode bool
	}{
		{"neither: collector mode", "", "", false, false},
		{"primary only", "http://127.0.0.1:9001", "", true, false},
		{"shadow only", "", "http://127.0.0.1:9002", true, false},
		{"both", "http://127.0.0.1:9001", "http://127.0.0.1:9002", false, true},
		{"unroutable scheme", "ftp://127.0.0.1:9001", "http://127.0.0.1:9002", true, false},
		{"no host", "http://", "http://127.0.0.1:9002", true, false},
		{"bad shadow", "http://127.0.0.1:9001", "gopher://x", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := defaults()
			o.primary, o.shadow = tc.primary, tc.shadow
			primary, shadow, err := o.upstreams()

			if tc.wantErr {
				if err == nil {
					t.Fatalf("primary=%q shadow=%q was accepted", tc.primary, tc.shadow)
				}
				if exitCode(err) != exitUsage {
					t.Errorf("exited %d, want %d", exitCode(err), exitUsage)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := primary != nil; got != tc.wantProxyMode {
				t.Errorf("proxy mode = %v, want %v", got, tc.wantProxyMode)
			}
			if tc.wantProxyMode && shadow.Host != "127.0.0.1:9002" {
				t.Errorf("shadow host = %q, want 127.0.0.1:9002", shadow.Host)
			}
		})
	}
}

// A missing ruleset and an unusable one are different failures, and -rules being
// unset is not a failure at all.
func TestRulesetLoadingClassifiesItsFailures(t *testing.T) {
	o := defaults()
	if rules, err := o.rules(); err != nil {
		t.Fatalf("no -rules should be fine: %v", err)
	} else if len(rules.Rules) == 0 {
		t.Error("the built-in defaults were not loaded")
	}

	o.rulesPath = "/nonexistent/rules.yaml"
	if _, err := o.rules(); exitCode(err) != exitNoInput {
		t.Errorf("missing file exited %d, want %d", exitCode(err), exitNoInput)
	}

	bad := t.TempDir() + "/bad.yaml"
	if err := os.WriteFile(bad, []byte("rules:\n  - path: /body/x\n    normalise: round:99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o.rulesPath = bad
	if _, err := o.rules(); exitCode(err) != exitDataErr {
		t.Errorf("unusable ruleset exited %d, want %d", exitCode(err), exitDataErr)
	}
}
