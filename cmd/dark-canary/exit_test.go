package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"testing"
)

// Every failure used to exit 1, so a supervisor could not tell a typo from a
// taken port. These are the classifications a caller now depends on; each case
// starts from an error shaped like the one run() really returns.
func TestExitCodeClassifiesEveryFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, exitOK},
		{"clean shutdown", http.ErrServerClosed, exitOK},
		{"bad flag value", usagef("%w", errors.New("sampleRate must be between 0 and 1, got 5")), exitUsage},
		{"reachable without a token", usagef("-listen %s is not loopback", "0.0.0.0:8099"), exitUsage},
		{"upstream typo", usagef("%w", errors.New(`-primary "ftp://x": need an http:// or https:// URL`)), exitUsage},
		{"ruleset missing", rulesetError{fmt.Errorf("open /nope: %w", fs.ErrNotExist)}, exitNoInput},
		{"ruleset unusable", rulesetError{errors.New("rule 0 (/body/x): round: needs 0..15 decimal places")}, exitDataErr},
		{"listener refused", fmt.Errorf("proxy: %w", &net.OpError{Op: "listen", Err: errors.New("address already in use")}), exitOSErr},
		{"unclassified", errors.New("something nobody planned for"), exitSoftware},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCode(tc.err); got != tc.want {
				t.Errorf("exitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// A wrapped error keeps the message it always had: the type carries the exit
// code, and changing what the operator reads is not part of the deal.
func TestUsageErrorKeepsItsMessage(t *testing.T) {
	inner := errors.New("maxBodyBytes must be positive, got 0")
	wrapped := usagef("%w", inner)
	if wrapped.Error() != inner.Error() {
		t.Errorf("message changed: %q != %q", wrapped.Error(), inner.Error())
	}
	if !errors.Is(wrapped, inner) {
		t.Error("wrapped error no longer unwraps to the original")
	}
}

// Codes a caller can act on have to be distinct, and none of them may collide
// with a shell's own reserved meanings.
func TestExitCodesAreDistinctAndInRange(t *testing.T) {
	seen := map[int]bool{}
	for _, code := range []int{exitOK, exitUsage, exitDataErr, exitNoInput, exitSoftware, exitOSErr} {
		if seen[code] {
			t.Errorf("exit code %d is used twice", code)
		}
		seen[code] = true
		if code < 0 || code > 125 {
			t.Errorf("exit code %d is outside the range a shell reports verbatim", code)
		}
	}
	if seen[1] {
		t.Error("1 is the check-failed code and must not be reused for an error")
	}
}
