// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package vex is the subset of OpenVEX (https://openvex.dev, v0.2.0) that
// Tobby reads (FR-031 amendment 2026-11-16, roadmap R-15, milestone 7).
//
// The instance may declare local OpenVEX documents (scan.vex). Each
// not_affected statement carrying a justification becomes an exception
// bounded by its products (the scope) and by an expiry; other statuses add
// nothing. A document that cannot be read or validated refuses the start
// (TBY-SCN-027). Documents are local files only — never fetched
// (NFR-019) — and Tobby never produces or signs one: it signs nothing
// (ADR-0006).
//
// This package carries the document shape and its closed vocabularies,
// reserved at the opening of the milestone. Parsing (bounded and strict,
// NFR-023), its fuzz target (NFR-024), and the translation into
// scan.Exception are track C.
package vex

import "time"

// Document is an OpenVEX document, restricted to the fields Tobby reads.
type Document struct {
	Context    string      `json:"@context"`
	ID         string      `json:"@id"`
	Author     string      `json:"author"`
	Timestamp  time.Time   `json:"timestamp"`
	Version    int         `json:"version"`
	Statements []Statement `json:"statements"`
}

// Statement is one VEX statement.
type Statement struct {
	Vulnerability Vulnerability `json:"vulnerability"`
	Products      []Product     `json:"products"`
	Status        Status        `json:"status"`
	Justification Justification `json:"justification,omitempty"`
	// ImpactStatement is the free-text alternative to a justification the
	// format allows; Tobby requires a justification and reports this text.
	ImpactStatement string `json:"impact_statement,omitempty"`
	// Timestamp, when set, overrides the document's.
	Timestamp *time.Time `json:"timestamp,omitempty"`
}

// Vulnerability names the vulnerability a statement is about.
type Vulnerability struct {
	// Name is the identifier, "CVE-<year>-<number>" for Tobby.
	Name string `json:"name"`
}

// Product identifies what a statement covers: a package URL, an OCI
// reference or a digest, as the document writes it.
type Product struct {
	ID string `json:"@id"`
}

// Status is a statement's status. The set is closed by the format.
type Status string

// The four OpenVEX statuses. Only StatusNotAffected yields an exception.
const (
	StatusNotAffected        Status = "not_affected"
	StatusAffected           Status = "affected"
	StatusFixed              Status = "fixed"
	StatusUnderInvestigation Status = "under_investigation"
)

// Valid reports whether s is one of the four statuses.
func (s Status) Valid() bool {
	switch s {
	case StatusNotAffected, StatusAffected, StatusFixed, StatusUnderInvestigation:
		return true
	}
	return false
}

// Justification is the machine-readable reason of a not_affected
// statement. The set is closed by the format.
type Justification string

// The five OpenVEX justifications.
const (
	ComponentNotPresent                         Justification = "component_not_present"
	VulnerableCodeNotPresent                    Justification = "vulnerable_code_not_present"
	VulnerableCodeNotInExecutePath              Justification = "vulnerable_code_not_in_execute_path"
	VulnerableCodeCannotBeControlledByAdversary Justification = "vulnerable_code_cannot_be_controlled_by_adversary"
	InlineMitigationsAlreadyExist               Justification = "inline_mitigations_already_exist"
)

// Valid reports whether j is one of the five justifications.
func (j Justification) Valid() bool {
	switch j {
	case ComponentNotPresent, VulnerableCodeNotPresent, VulnerableCodeNotInExecutePath,
		VulnerableCodeCannotBeControlledByAdversary, InlineMitigationsAlreadyExist:
		return true
	}
	return false
}

// Admits reports whether a statement may become an exception: status
// not_affected with a valid justification. Every other statement adds
// nothing — in particular a not_affected statement with only an impact
// statement, which a reviewer can read but a policy cannot bound.
func (s *Statement) Admits() bool {
	return s.Status == StatusNotAffected && s.Justification.Valid()
}
