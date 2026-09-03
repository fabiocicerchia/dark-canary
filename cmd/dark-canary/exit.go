package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
)

// Exit codes, following sysexits(3). One table for the whole tool, so a
// supervisor or an operator can tell "you typed the flag wrong" apart from "the
// port was taken" without parsing stderr. Everything here is still a refusal to
// start: the codes classify the refusal, they never soften it.
const (
	exitOK       = 0
	exitUsage    = 64 // EX_USAGE: a flag, or a combination of flags, this tool refuses
	exitDataErr  = 65 // EX_DATAERR: the ruleset was read but does not hold up
	exitNoInput  = 66 // EX_NOINPUT: the ruleset file is not there
	exitSoftware = 70 // EX_SOFTWARE: a failure with no better classification
	exitOSErr    = 71 // EX_OSERR: the OS refused — a listener that could not bind
)

// usageError marks a failure the operator caused with a flag. It carries no
// message of its own: the wrapped error already says what is wrong, and the
// point of the type is the exit code, not the text.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// usagef builds a usage error. Wrap an existing one with usagef("%w", err) so
// its message survives unchanged.
func usagef(format string, a ...any) error { return usageError{fmt.Errorf(format, a...)} }

// rulesetError marks a failure to load -rules. Whether the file is missing or
// merely wrong decides the code, and only errors.Is can tell those apart.
type rulesetError struct{ err error }

func (e rulesetError) Error() string { return e.err.Error() }
func (e rulesetError) Unwrap() error { return e.err }

// exitCode classifies a run() error. Unrecognised failures are EX_SOFTWARE
// rather than 1, because reaching here with something unclassified means the
// table is missing a case — which is a bug in this file, not in the operator.
func exitCode(err error) int {
	switch {
	case err == nil, errors.Is(err, http.ErrServerClosed):
		return exitOK
	case errors.As(err, new(usageError)):
		return exitUsage
	case errors.As(err, new(rulesetError)):
		if errors.Is(err, fs.ErrNotExist) {
			return exitNoInput
		}
		return exitDataErr
	case errors.As(err, new(*net.OpError)):
		return exitOSErr
	default:
		return exitSoftware
	}
}
