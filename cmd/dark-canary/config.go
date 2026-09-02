package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"time"

	"github.com/fabiocicerchia/dark-canary/internal/noise"
	"github.com/fabiocicerchia/dark-canary/internal/safety"
)

// options is the flag surface, parsed once into one value. Holding it together
// is what lets run() hand a whole step to a function instead of eleven
// pointers, which is why it exists at all.
type options struct {
	listen      string
	token       string
	rulesPath   string
	timeout     time.Duration
	maxPending  int
	killFile    string
	maxBody     int
	writes      bool
	scrub       string
	interval    time.Duration
	primary     string
	shadow      string
	proxyListen string
	sample      float64
	shadowTO    time.Duration
	inflight    int
}

func parseFlags() *options {
	var o options
	// Loopback by default. Captures carry response bodies from production
	// traffic, and /report serves them back — binding every interface with
	// no auth would make this the softest target on the network.
	flag.StringVar(&o.listen, "listen", "127.0.0.1:8099", "address to accept captures on")
	flag.StringVar(&o.token, "token", "", "shared secret required in "+tokenHeader+"; mandatory when -listen is not loopback")
	flag.StringVar(&o.rulesPath, "rules", "", "noise ruleset (YAML); merged on top of the built-in defaults")
	flag.DurationVar(&o.timeout, "correlate-timeout", 30*time.Second, "how long a lone capture waits for its partner")
	flag.IntVar(&o.maxPending, "max-pending", 10_000, "bound on unpaired captures held in memory")
	flag.StringVar(&o.killFile, "kill-file", safety.Default().KillFile, "path whose existence stops all processing")
	flag.IntVar(&o.maxBody, "max-body", safety.Default().MaxBodyBytes, "largest capture body accepted, in bytes")
	flag.BoolVar(&o.writes, "allow-write-mirroring", false, "accept captures of non-idempotent requests (REAL WRITES on the shadow)")
	flag.StringVar(&o.scrub, "scrub", "", "comma-separated body fields to redact on arrival")
	flag.DurationVar(&o.interval, "report-every", 0, "print the report to stderr on this interval (0 = only on request)")

	// Proxy mode: dark-canary does the routing itself, no nginx, no Lua.
	flag.StringVar(&o.primary, "primary", "", "upstream that answers the client, e.g. http://127.0.0.1:9001 (enables proxy mode)")
	flag.StringVar(&o.shadow, "shadow", "", "upstream mirrored to and discarded, e.g. http://127.0.0.1:9002")
	flag.StringVar(&o.proxyListen, "proxy-listen", "127.0.0.1:8080", "address to serve proxied traffic on")
	flag.Float64Var(&o.sample, "sample", safety.Default().SampleRate, "fraction of eligible requests mirrored to the shadow")
	flag.DurationVar(&o.shadowTO, "shadow-timeout", 10*time.Second, "how long a mirrored request may take before it is abandoned")
	flag.IntVar(&o.inflight, "max-inflight", 64, "bound on mirrored requests in flight; over this, requests are served but not mirrored")
	flag.Parse()
	return &o
}

// safetyConfig folds the flags into the guard rails. warn is a posture that is
// supported but must never be silent; err is one the tool refuses outright. Both
// can be set at once, and the warning is still announced when they are — write
// mirroring being enabled does not excuse the tool from the loopback refusal.
func (o *options) safetyConfig() (cfg safety.Config, warn, err error) {
	cfg = safety.Default()
	cfg.KillFile = o.killFile
	cfg.MaxBodyBytes = o.maxBody
	cfg.MirrorReadsOnly = !o.writes
	cfg.ScrubFields = splitList(o.scrub)
	cfg.SampleRate = o.sample

	if err := cfg.Validate(); err != nil {
		if !errors.Is(err, safety.ErrWriteMirroringEnabled) {
			return cfg, nil, err
		}
		warn = err
	}

	// Refuse to be reachable and unauthenticated at the same time. Failing at
	// startup is the only place this can be caught before the data is exposed.
	if !isLoopback(o.listen) && o.token == "" {
		return cfg, warn, fmt.Errorf("-listen %s is not loopback: set -token, or bind to 127.0.0.1", o.listen)
	}
	return cfg, warn, nil
}

// rules is the built-in defaults with the operator's file merged on top.
func (o *options) rules() (noise.Ruleset, error) {
	rules := noise.Default()
	if o.rulesPath == "" {
		return rules, nil
	}
	loaded, err := noise.Load(o.rulesPath)
	if err != nil {
		return rules, err
	}
	return rules.Merge(loaded), nil
}

// upstreams parses -primary and -shadow, or reports that proxy mode is off.
// Neither or both: routing to one of the two is not a mode this tool has.
func (o *options) upstreams() (primary, shadow *url.URL, err error) {
	if o.primary == "" {
		if o.shadow != "" {
			return nil, nil, fmt.Errorf("-shadow needs -primary: proxy mode routes to both or neither")
		}
		return nil, nil, nil
	}
	if o.shadow == "" {
		return nil, nil, fmt.Errorf("-primary needs -shadow: with nothing to mirror to there is nothing to compare")
	}
	if primary, err = parseUpstream("primary", o.primary); err != nil {
		return nil, nil, err
	}
	if shadow, err = parseUpstream("shadow", o.shadow); err != nil {
		return nil, nil, err
	}
	return primary, shadow, nil
}
