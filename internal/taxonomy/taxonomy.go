// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package taxonomy is Tobby's single error taxonomy (roadmap R-03).
//
// Every user-visible error carries a short stable code ("TBY-REG-003") and a
// structured bilingual message — what happened, probable cause, corrective
// action — rendered from the same catalog entry by the web UI (HTML
// partial), the CLI (text), and the REST API (RFC 9457 problem document).
// Codes are stable across a major series: they are part of the product
// contract, anchor the embedded troubleshooting guide (/help#<code>), and
// map onto the CLI exit-code classes (FR-066: success, policy refusal,
// verification failure, operational failure).
//
// Errors persist as code + parameters, never as a localized sentence: task
// history must re-render in the viewer's language (FR-063) long after the
// fact. Messages never carry secrets or stack traces (NFR-015); the wrapped
// cause is for logs only.
package taxonomy

import (
	"fmt"
	"sort"
	"strings"
)

// Code is a short stable error code, "TBY-<domain>-<nnn>".
type Code string

// Class buckets codes into the CLI exit-code classes of FR-066. The full
// published exit-code table ships with R-08; the classes are fixed here so
// every code created from milestone 2 onward already has its disposition.
type Class int

const (
	// ClassOperational is everything that failed without a policy or
	// verification verdict: network, storage, internal errors.
	ClassOperational Class = iota
	// ClassPolicy marks refusals by explicit policy or authorization
	// (allowlist, roles, secure-by-default startup refusals).
	ClassPolicy
	// ClassVerification marks failed integrity or authenticity checks
	// (signatures, pinned digests). The most severe class.
	ClassVerification
)

// Process exit codes (FR-066). 0 is success and 2 is a CLI usage error, by
// convention; taxonomy classes map onto the remaining values.
const (
	ExitOK           = 0
	ExitFailure      = 1
	ExitUsage        = 2
	ExitPolicy       = 3
	ExitVerification = 4
)

// ExitCode returns the process exit code for the class.
func (c Class) ExitCode() int {
	switch c {
	case ClassPolicy:
		return ExitPolicy
	case ClassVerification:
		return ExitVerification
	default:
		return ExitFailure
	}
}

// severity orders classes for principal-code aggregation: verification
// failures outrank policy refusals, which outrank operational failures.
func (c Class) severity() int { return int(c) }

// Entry is one catalog entry: the stable identity of an error kind.
type Entry struct {
	// Code is the stable identifier, "TBY-<domain>-<nnn>".
	Code Code
	// Class is the FR-066 exit-code class.
	Class Class
	// HTTPStatus is the status served when this error is the direct
	// response body (RFC 9457). Zero for conditions never served over HTTP
	// (startup refusals, client-side conditions).
	HTTPStatus int
	// Params names the parameters the message templates may reference.
	// Tests enforce that every {{.param}} in every language's templates is
	// declared here, and that constructors supply them.
	Params []string
}

// Params is the parameter set of one error instance. Values must be
// renderable with %v and must never contain secrets (NFR-015).
type Params map[string]any

// Error is one occurrence of a catalog entry. It implements error and
// carries everything needed to re-render in any language: code, parameters,
// optional correlation identifier, and an optional wrapped cause (logs
// only — never rendered to users).
type Error struct {
	code        Code
	params      Params
	correlation string
	cause       error
}

// New builds an error for code. Unknown codes panic: they are programming
// errors, caught by the tests that walk the catalog.
func New(code Code, params Params) *Error {
	if _, ok := catalog[code]; !ok {
		panic(fmt.Sprintf("taxonomy: unknown code %q", code))
	}
	return &Error{code: code, params: params}
}

// WithCause attaches the underlying technical error, for logs and
// errors.Is/As chains. Never rendered to users.
func (e *Error) WithCause(err error) *Error {
	e.cause = err
	return e
}

// WithCorrelation attaches the run or task identifier shown alongside the
// rendered error (FR-090) so operators can find the matching log records.
func (e *Error) WithCorrelation(id string) *Error {
	e.correlation = id
	return e
}

// Code returns the stable code.
func (e *Error) Code() Code { return e.code }

// ParamsMap returns the instance parameters.
func (e *Error) ParamsMap() Params { return e.params }

// Correlation returns the attached correlation identifier, if any.
func (e *Error) Correlation() string { return e.correlation }

// Entry returns the catalog entry for the error's code.
func (e *Error) Entry() Entry { return catalog[e.code] }

// Error renders the technical, non-localized form used in logs and wrapped
// error chains: code, sorted parameters, wrapped cause.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.code))
	if len(e.params) > 0 {
		keys := make([]string, 0, len(e.params))
		for k := range e.params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString(" [")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(" ")
			}
			fmt.Fprintf(&b, "%s=%v", k, e.params[k])
		}
		b.WriteString("]")
	}
	if e.cause != nil {
		b.WriteString(": ")
		b.WriteString(e.cause.Error())
	}
	return b.String()
}

// Unwrap exposes the wrapped cause to errors.Is/As.
func (e *Error) Unwrap() error { return e.cause }

// Lookup returns the catalog entry for code.
func Lookup(code Code) (Entry, bool) {
	en, ok := catalog[code]
	return en, ok
}

// All returns every catalog entry, sorted by code. The embedded
// troubleshooting stub (/help) and the completeness tests iterate on it.
func All() []Entry {
	out := make([]Entry, 0, len(catalog))
	for _, en := range catalog {
		out = append(out, en)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// Principal returns the principal error of a multi-item operation — the one
// whose code a task list, a report, or the CLI exit path surfaces when
// several items failed. The rule is fixed here so the UI and the API never
// compute it separately: highest class severity first (verification >
// policy > operational), then the most frequent code, then the smallest
// code lexicographically. Returns nil for an empty or all-nil slice.
func Principal(errs []*Error) *Error {
	counts := make(map[Code]int)
	first := make(map[Code]*Error)
	for _, e := range errs {
		if e == nil {
			continue
		}
		counts[e.code]++
		if _, ok := first[e.code]; !ok {
			first[e.code] = e
		}
	}
	var best Code
	for code := range counts {
		if best == "" {
			best = code
			continue
		}
		bc, cc := catalog[best].Class.severity(), catalog[code].Class.severity()
		switch {
		case cc > bc:
			best = code
		case cc == bc && counts[code] > counts[best]:
			best = code
		case cc == bc && counts[code] == counts[best] && code < best:
			best = code
		}
	}
	if best == "" {
		return nil
	}
	return first[best]
}
