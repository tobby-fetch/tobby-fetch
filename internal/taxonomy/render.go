// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package taxonomy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Text renders e for the CLI and plain-text contexts in lang:
//
//	TBY-REG-003 — The source registry refused authentication.
//	  cause:  Credentials for docker.io are missing or expired.
//	  action: Set registries.credentials for docker.io …
//	  see:    /help#TBY-REG-003    correlation: run_01j8m2vx4q
func Text(lang string, e *Error) string {
	m := Localize(lang, e)
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s\n", m.Code, m.What)
	fmt.Fprintf(&b, "  cause:  %s\n", m.Cause)
	fmt.Fprintf(&b, "  action: %s\n", m.Action)
	fmt.Fprintf(&b, "  see:    %s", m.HelpAnchor)
	if m.Correlation != "" {
		fmt.Fprintf(&b, "    correlation: %s", m.Correlation)
	}
	b.WriteString("\n")
	return b.String()
}

// Problem is the RFC 9457 problem document served by /api/v1. The
// extension members (code, probable_cause, action, correlation_id) are part
// of the documented API schema (FR-060); "type" is the in-instance
// troubleshooting anchor, resolvable offline (NFR-019).
type Problem struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Code          Code   `json:"code"`
	ProbableCause string `json:"probable_cause"`
	Action        string `json:"action"`
	CorrelationID string `json:"correlation_id,omitempty"`
}

// NewProblem builds the RFC 9457 body for e in lang. Codes with no
// HTTPStatus (never served) default to 500 — reaching that path is a
// programming error surfaced by tests.
func NewProblem(lang string, e *Error) Problem {
	m := Localize(lang, e)
	status := e.Entry().HTTPStatus
	if status == 0 {
		status = http.StatusInternalServerError
	}
	return Problem{
		Type:          m.HelpAnchor,
		Title:         m.What,
		Status:        status,
		Code:          m.Code,
		ProbableCause: m.Cause,
		Action:        m.Action,
		CorrelationID: m.Correlation,
	}
}

// WriteProblem serves e as application/problem+json with its entry's HTTP
// status.
func WriteProblem(w http.ResponseWriter, lang string, e *Error) {
	p := NewProblem(lang, e)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
