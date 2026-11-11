// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package scan

import (
	"context"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Severity is a scanner's qualitative severity, normalized to the closed
// set below. The values are Trivy's own spelling, which is also the
// spelling an operator reads in every other tool of a qualification
// pipeline (ADR-0008 context).
type Severity string

// The closed severity set.
const (
	SeverityUnknown  Severity = "UNKNOWN"
	SeverityLow      Severity = "LOW"
	SeverityMedium   Severity = "MEDIUM"
	SeverityHigh     Severity = "HIGH"
	SeverityCritical Severity = "CRITICAL"
)

// Severities lists the closed set, least severe first.
func Severities() []Severity {
	return []Severity{SeverityUnknown, SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical}
}

// Valid reports whether s belongs to the closed set.
func (s Severity) Valid() bool {
	switch s {
	case SeverityUnknown, SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical:
		return true
	}
	return false
}

// LowerBound is the score a finding WITHOUT a CVSS score is evaluated at:
// the lower bound of its CVSS v3 qualitative band (FR-031 amendment,
// RECIPE-SPEC §10.4). ok is false for UNKNOWN — and for anything outside
// the closed set — which has no place on the scale: such a finding is not
// compared with the threshold and is reported as unscored.
func (s Severity) LowerBound() (score float64, ok bool) {
	switch s {
	case SeverityLow:
		return 0.1, true
	case SeverityMedium:
		return 4.0, true
	case SeverityHigh:
		return 7.0, true
	case SeverityCritical:
		return 9.0, true
	}
	return 0, false
}

// Finding is one normalized vulnerability found in scanned content.
type Finding struct {
	// ID is the vulnerability identifier as the scanner reports it
	// ("CVE-2024-12345", or a GHSA/distribution identifier when no CVE
	// exists). Exceptions match it exactly.
	ID string `json:"id"`
	// Severity is the scanner's qualitative severity.
	Severity Severity `json:"severity"`
	// CVSS is the CVSS base score the policy compares with its threshold,
	// already reduced to one number: when several sources score the
	// finding, the highest. Nil when no source scores it.
	CVSS *float64 `json:"cvss,omitempty"`
	// Package is the affected package name.
	Package string `json:"package"`
	// InstalledVersion is the version found in the content.
	InstalledVersion string `json:"installed_version,omitempty"`
	// FixedVersion is the first version carrying the fix; empty when the
	// advisory names none.
	FixedVersion string `json:"fixed_version,omitempty"`
	// Source is the advisory data source the finding was matched from
	// (ADR-0008 decision 5: "identifier, severity, fixed-in, source").
	Source string `json:"source,omitempty"`
}

// Database describes the vulnerability database a scan ran against — the
// facts R-13 puts in every report.
type Database struct {
	// Version is the database's own schema version, as the scanner states
	// it.
	Version string `json:"version"`
	// Digest is the OCI digest of the database artifact, when it came
	// through the tobby-system channel (FR-032): what makes two reports
	// comparable across zones.
	Digest string `json:"digest,omitempty"`
	// BuiltAt is the build timestamp the database records about itself.
	// Its age is measured from here, not from when it was transferred.
	BuiltAt time.Time `json:"built_at"`
	// AgeSeconds is the database age at scan time, in whole seconds.
	// Stated rather than left to be derived: the report is read long after
	// the fact, when "now" has moved (R-13: the age is in EVERY report).
	AgeSeconds int64 `json:"age_seconds"`
	// Stale is true when the age exceeded scan.maxDbAge at scan time.
	Stale bool `json:"stale,omitempty"`
}

// Info identifies a scanner build.
type Info struct {
	// Name is the scanner ("trivy").
	Name string `json:"name"`
	// Version is the scanner's own version string.
	Version string `json:"version,omitempty"`
	// Digest is the digest of the executed binary, verified before every
	// execution when the binary came through the channel (FR-031).
	Digest string `json:"digest,omitempty"`
}

// Target is what one scan is asked about: content addressed by digest.
// How the content is handed to the scanner — an OCI layout view of the
// store, a loopback registry read — is the implementation's business;
// the digest is the identity, and results are cacheable by (digest,
// database) (ADR-0008 consequences).
type Target struct {
	// Digest is the content's manifest (or index) digest.
	Digest string
	// Repository is the local relocated repository holding it (FR-035).
	Repository string
	// Kind is the ingredient kind ("ContainerImage", "OCIArtifact",
	// "FileSet", "HelmChart").
	Kind string
	// Platform optionally narrows a multi-platform index to one child,
	// "os/arch[/variant]".
	Platform string
}

// Result is a scanner's normalized answer about one Target.
type Result struct {
	Scanner   Info      `json:"scanner"`
	ScannedAt time.Time `json:"scanned_at"`
	Database  Database  `json:"database"`
	Findings  []Finding `json:"findings"`
}

// Status is a scanner's readiness, reported at startup and on demand: the
// source of the degraded-mode banner, log record and metric (FR-031,
// FR-032 amendments). Never silent: an unavailable scanner says why.
type Status struct {
	// Available is false when the binary is missing, unverifiable, or not
	// executable — the degraded mode.
	Available bool
	// Reason is the taxonomy code explaining an unavailable scanner or an
	// absent database; empty when Available and a database is present.
	Reason taxonomy.Code
	// Scanner identifies the binary, when one was found.
	Scanner Info
	// Database describes the database, nil when there is none.
	Database *Database
}

// Scanner is the scanner-neutral seam (ADR-0008 decision 5). An
// implementation runs the component that parses hostile input OUT of the
// Tobby process (decision 4) and returns only normalized results.
//
// Scan returns an error only when no Result could be produced; that error
// is a *taxonomy.Error from the TBY-SCN family, so the per-item report can
// say "not scanned" with the reason.
type Scanner interface {
	Scan(ctx context.Context, t Target) (*Result, error)
	Status(ctx context.Context) Status
}
