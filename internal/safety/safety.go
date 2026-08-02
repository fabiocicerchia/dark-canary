// Package safety holds the non-negotiable controls. These are designed in from
// day one, not bolted on later — a shadow-traffic tool that does real writes or
// leaks PII is a liability, not a product.
package safety

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Config gathers every guard rail in one place so none can be forgotten.
type Config struct {
	// MirrorReadsOnly defaults true: only idempotent (GET/HEAD) requests are
	// mirrored. Write mirroring is an explicit, loudly-documented opt-in because
	// non-idempotent requests hitting the shadow do real writes.
	MirrorReadsOnly bool `json:"mirrorReadsOnly"`

	// SampleRate is a first-class control, defaulting LOW, to bound load
	// amplification on shared datastores.
	SampleRate float64 `json:"sampleRate"`

	// ScrubFields lists request/response fields scrubbed BEFORE anything is
	// stored, so PII never lands in the collector or the UI.
	ScrubFields []string `json:"scrubFields"`

	// KillFile is checked on every request by the edge hook and on every
	// capture by the collector. A path on local disk, on purpose: it has to work
	// when the control plane is the thing that is broken.
	KillFile string `json:"killFile"`

	// MaxBodyBytes caps what a capture may carry. The edge truncates too; this
	// is the collector refusing to be the place a 200MB response is buffered.
	MaxBodyBytes int `json:"maxBodyBytes"`
}

// Default is the safe posture: reads only, 1% sampling, nothing stored raw.
func Default() Config {
	return Config{
		MirrorReadsOnly: true,
		SampleRate:      0.01,
		ScrubFields:     nil,
		KillFile:        "/etc/dark-canary/kill",
		MaxBodyBytes:    1 << 20,
	}
}

// Validate refuses a configuration that quietly disables a guard rail. Rejecting
// at startup beats discovering it in an incident review.
func (c Config) Validate() error {
	if c.SampleRate < 0 || c.SampleRate > 1 {
		return fmt.Errorf("sampleRate must be between 0 and 1, got %v", c.SampleRate)
	}
	if c.MaxBodyBytes <= 0 {
		return fmt.Errorf("maxBodyBytes must be positive, got %d", c.MaxBodyBytes)
	}
	if !c.MirrorReadsOnly {
		// Not an error — it is a supported, deliberate choice — but it must
		// never happen silently.
		return ErrWriteMirroringEnabled
	}
	return nil
}

// ErrWriteMirroringEnabled is returned by Validate as a warning, not a failure:
// callers log it loudly and continue.
var ErrWriteMirroringEnabled = fmt.Errorf(
	"write mirroring is enabled: non-idempotent requests will do REAL WRITES on the shadow")

// KillSwitch must work even when the control plane is down.
type KillSwitch interface {
	Engaged() bool
}

// FileKillSwitch is the whole implementation: a path that exists means stop.
// No API call, no config reload, no dependency on anything that can also be
// broken — `touch` it over SSH and everything stops within TTL.
type FileKillSwitch struct {
	Path string
	// TTL caches the answer, so a hot path costs one clock read rather than one
	// stat per request. Zero means the default below.
	TTL time.Duration
	Now func() time.Time

	mu      sync.Mutex
	checked time.Time
	engaged bool
}

const defaultKillTTL = time.Second

func (k *FileKillSwitch) Engaged() bool {
	if k.Path == "" {
		return false
	}
	now := time.Now
	if k.Now != nil {
		now = k.Now
	}
	ttl := k.TTL
	if ttl == 0 {
		ttl = defaultKillTTL
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	t := now()
	if !k.checked.IsZero() && t.Sub(k.checked) < ttl {
		return k.engaged
	}
	_, err := os.Stat(k.Path)
	k.engaged = err == nil
	k.checked = t
	return k.engaged
}

// --- scrubbing ---------------------------------------------------------------

// Scrubber redacts configured fields from a captured body. The edge scrubs
// first; this is the second line, because a capture can also arrive from a hand-
// rolled client, a replay, or an older edge that predates a new field.
type Scrubber struct {
	patterns []*regexp.Regexp
	fields   []string
}

const Redacted = "[redacted]"

func NewScrubber(fields []string) *Scrubber {
	s := &Scrubber{fields: fields}
	for _, f := range fields {
		if f == "" {
			continue
		}
		// "field": <string|number|null|bool>, matched shallowly. Nested
		// structures are matched wherever they appear, since JSON keys are
		// unique per object but not per document.
		s.patterns = append(s.patterns, regexp.MustCompile(
			`("`+regexp.QuoteMeta(f)+`"\s*:\s*)("(?:[^"\\]|\\.)*"|-?[0-9.eE+]+|true|false|null)`))
	}
	return s
}

func (s *Scrubber) Body(body []byte) []byte {
	if s == nil || len(s.patterns) == 0 || len(body) == 0 {
		return body
	}
	out := body
	for _, re := range s.patterns {
		out = re.ReplaceAll(out, []byte(`${1}"`+Redacted+`"`))
	}
	return out
}

// Header redacts a header value when its name is configured for scrubbing, or
// when it is one of the credentials nobody should ever have to configure.
var alwaysScrubbed = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
	"x-api-key":           true,
}

func (s *Scrubber) Header(name string, values []string) []string {
	lower := strings.ToLower(name)
	if alwaysScrubbed[lower] {
		return []string{Redacted}
	}
	if s != nil {
		for _, f := range s.fields {
			if strings.EqualFold(f, name) {
				return []string{Redacted}
			}
		}
	}
	return values
}
