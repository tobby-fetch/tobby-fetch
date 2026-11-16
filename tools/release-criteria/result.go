// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"fmt"
	"io"
	"strings"
)

// status is the verdict of one check or of one criterion. aggregate()
// refuses to call an empty list of evidence verified.
type status int

const (
	statusOK status = iota
	statusKO
	statusUnverifiable
)

func (s status) String() string {
	switch s {
	case statusOK:
		return "OK"
	case statusKO:
		return "KO"
	default:
		return "NON VÉRIFIABLE ICI"
	}
}

// detail is one line of evidence under a criterion.
type detail struct {
	status status
	text   string
}

// result is the checklist entry of one release criterion.
type result struct {
	id      int
	title   string
	details []detail
	// expectedKO, when set, says why a KO is the expected answer for this
	// version (the recipe-spec v1 tag on a release candidate). The KO is
	// still printed as KO; it just does not fail the run.
	expectedKO string
}

func (r *result) add(s status, format string, args ...any) {
	r.details = append(r.details, detail{status: s, text: fmt.Sprintf(format, args...)})
}

func (r *result) ok(format string, args ...any) { r.add(statusOK, format, args...) }
func (r *result) ko(format string, args ...any) { r.add(statusKO, format, args...) }
func (r *result) unverifiable(format string, args ...any) {
	r.add(statusUnverifiable, format, args...)
}

// status aggregates the evidence: any KO makes the criterion KO; otherwise
// any unverifiable line makes it unverifiable (a criterion half proven is
// not proven); an OK needs at least one line of evidence and nothing else.
func (r *result) status() status {
	return aggregate(r.details)
}

func aggregate(ds []detail) status {
	if len(ds) == 0 {
		return statusUnverifiable
	}
	out := statusOK
	for _, d := range ds {
		switch d.status {
		case statusKO:
			return statusKO
		case statusUnverifiable:
			out = statusUnverifiable
		case statusOK:
		}
	}
	return out
}

// failing reports whether the result must turn the exit code non-zero.
func (r *result) failing() bool {
	return r.status() == statusKO && r.expectedKO == ""
}

// summary counts the verdicts of a run.
type summary struct {
	ok, ko, expectedKO, unverifiable int
}

func summarize(rs []*result) summary {
	var s summary
	for _, r := range rs {
		switch r.status() {
		case statusOK:
			s.ok++
		case statusKO:
			if r.expectedKO != "" {
				s.expectedKO++
			} else {
				s.ko++
			}
		case statusUnverifiable:
			s.unverifiable++
		}
	}
	return s
}

// render prints the checklist: one line per criterion, its evidence below.
func render(w io.Writer, version, commit string, rs []*result) {
	head := version
	if commit != "" {
		head += " (" + shortSHA(commit) + ")"
	}
	_, _ = fmt.Fprintf(w, "Release criteria — %s\n\n", head)
	for _, r := range rs {
		st := r.status()
		line := fmt.Sprintf("[%s] %d. %s", st, r.id, r.title)
		if st == statusKO && r.expectedKO != "" {
			line += " — " + r.expectedKO
		}
		_, _ = fmt.Fprintln(w, line)
		for _, d := range r.details {
			marker := map[status]string{statusOK: "ok", statusKO: "KO", statusUnverifiable: "??"}[d.status]
			lines := strings.Split(strings.TrimRight(d.text, "\n"), "\n")
			_, _ = fmt.Fprintf(w, "      %s  %s\n", marker, lines[0])
			for _, l := range lines[1:] {
				_, _ = fmt.Fprintf(w, "          %s\n", l)
			}
		}
		_, _ = fmt.Fprintln(w)
	}
	s := summarize(rs)
	_, _ = fmt.Fprintf(w, "%d OK, %d KO, %d KO expected, %d not verifiable here\n", s.ok, s.ko, s.expectedKO, s.unverifiable)
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// lastLine returns the last meaningful line of command output, bounded —
// slsa-verifier and cosign print their verdict last.
func lastLine(s string, limit int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > limit {
		l = l[:limit] + "…"
	}
	return l
}
