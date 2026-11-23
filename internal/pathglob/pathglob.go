// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package pathglob is the glob grammar of repository patterns: "*"
// matches within one path segment, "**" spans segments, and the whole
// path must match. Trust scopes (trust.scopes[].repositories, FR-033) and
// the repository scope of scan exceptions (scan.exceptions[].scope,
// FR-031 amendment R-15) share it, so one pattern means one thing on
// every surface. It lives apart from both consumers because the engine
// imports the scan package: the matcher cannot sit in either without a
// cycle, and two copies would drift.
package pathglob

import "strings"

// Match matches a slash-separated path against a pattern. Comparison is
// exact and case-sensitive: both sides are expected in the canonical form
// of ADR-0013.
func Match(pattern, path string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

func matchSegments(pat, parts []string) bool {
	if len(pat) == 0 {
		return len(parts) == 0
	}
	if pat[0] == "**" {
		// "**" absorbs zero or more segments.
		for skip := 0; skip <= len(parts); skip++ {
			if matchSegments(pat[1:], parts[skip:]) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	if !matchSegment(pat[0], parts[0]) {
		return false
	}
	return matchSegments(pat[1:], parts[1:])
}

// matchSegment matches one segment with "*" wildcards (any run of
// characters within the segment).
func matchSegment(pat, s string) bool {
	parts := strings.Split(pat, "*")
	if len(parts) == 1 {
		return pat == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}
