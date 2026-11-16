// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package clock is the vocabulary of the clock-plausibility check (FR-096,
// roadmap R-32, milestone 7).
//
// An isolated zone has no time source to trust, and a wrong clock
// silently falsifies everything judged against it: database freshness
// (FR-032), the anti-rollback guard on media (FR-052, R-28), exception and
// token expiries (FR-031, FR-072), certificate validity. Tobby cannot know
// the right time, but it holds two dates it can prove have passed without
// any network: the build time of its own binary (SOURCE_DATE_EPOCH,
// ADR-0011) and the resolution time recorded in a medium's manifest
// (FR-054). A local clock earlier than either, beyond a tolerance for
// ordinary drift, is implausible.
//
// The check warns and records — banner, log record, CLI output, metric
// (tobby_clock_implausible), audit event (clock.implausible), codes
// TBY-CLK-001/002 — and changes no verdict by itself. This package holds
// the pure comparison (this file); the Monitor that keeps the latest verdict
// per anchor and the warn-and-record path are in monitor.go, wired at startup
// and at medium opening by internal/cli.
package clock

import "time"

// Anchor names the date a clock is checked against.
type Anchor string

const (
	// AnchorBuild is the build time of the running binary.
	AnchorBuild Anchor = "build"
	// AnchorMedium is the resolution time of the medium being opened.
	AnchorMedium Anchor = "medium"
)

// Verdict is the outcome of one check.
type Verdict struct {
	Anchor Anchor `json:"anchor"`
	// At is the anchor's date; Now the local clock when checked.
	At  time.Time `json:"at"`
	Now time.Time `json:"now"`
	// Gap is how far the local clock is behind the anchor; zero or
	// negative when it is not.
	Gap time.Duration `json:"gap"`
	// Implausible is true when Gap exceeds the tolerance.
	Implausible bool `json:"implausible"`
}

// Check compares now with the anchor date at. A zero at proves nothing
// (a development build carries no build time) and yields a plausible
// verdict. The comparison is strict: a gap equal to the tolerance is
// still plausible.
func Check(anchor Anchor, at, now time.Time, tolerance time.Duration) Verdict {
	v := Verdict{Anchor: anchor, At: at, Now: now}
	if at.IsZero() {
		return v
	}
	v.Gap = at.Sub(now)
	v.Implausible = v.Gap > tolerance
	return v
}
