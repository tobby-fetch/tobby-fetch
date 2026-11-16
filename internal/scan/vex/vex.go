// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package vex reads OpenVEX documents (https://openvex.dev, specification
// v0.2.0) for the scan policy (FR-031 amendment 2026-11-16, roadmap R-15).
//
// The instance may declare local OpenVEX documents (scan.vex). Each
// not_affected statement carrying a machine-readable justification becomes
// exceptions scoped by its products and bounded by the expiry declared
// beside the document; every other statement adds nothing — a statement
// with an impact statement only included. A document that cannot be read
// or validated refuses the start (TBY-SCN-027). Documents are local files
// only — never fetched (NFR-019) — and Tobby never produces or signs one:
// it signs nothing (ADR-0006).
//
// The parser is bounded and strict (NFR-023): the size is capped before a
// byte is decoded, the nesting depth and the counts of statements and
// products are capped, every field the specification does not define is
// refused (by its exact name: encoding/json's case-insensitive matching is
// closed), as are duplicate keys, invalid UTF-8 and trailing data. Parse
// is fuzzed (FuzzParse, NFR-024).
package vex

import "time"

// Context is the only @context this package reads: the OpenVEX
// specification v0.2.0. A document of another version has another shape
// (v0.0.x carries the vulnerability as a string) and is refused rather
// than read approximately.
const Context = "https://openvex.dev/ns/v0.2.0"

// Document is an OpenVEX v0.2.0 document: every field the specification
// defines, and none other.
type Document struct {
	Context     string      `json:"@context"`
	ID          string      `json:"@id"`
	Author      string      `json:"author"`
	Role        string      `json:"role,omitempty"`
	Timestamp   time.Time   `json:"timestamp"`
	LastUpdated *time.Time  `json:"last_updated,omitempty"`
	Version     int         `json:"version"`
	Tooling     string      `json:"tooling,omitempty"`
	Statements  []Statement `json:"statements"`
}

// Statement is one VEX statement.
type Statement struct {
	ID            string        `json:"@id,omitempty"`
	Version       int           `json:"version,omitempty"`
	Vulnerability Vulnerability `json:"vulnerability"`
	// Timestamp, when set, overrides the document's.
	Timestamp   *time.Time `json:"timestamp,omitempty"`
	LastUpdated *time.Time `json:"last_updated,omitempty"`
	Products    []Product  `json:"products"`
	Status      Status     `json:"status"`
	Supplier    string     `json:"supplier,omitempty"`
	StatusNotes string     `json:"status_notes,omitempty"`
	// Justification is the machine-readable reason of a not_affected
	// statement — the only thing that makes it an exception.
	Justification Justification `json:"justification,omitempty"`
	// ImpactStatement is the free-text alternative to a justification the
	// format allows; a reviewer can read it, a policy cannot bound it, so
	// it adds no exception (decided 2026-11-16).
	ImpactStatement          string     `json:"impact_statement,omitempty"`
	ActionStatement          string     `json:"action_statement,omitempty"`
	ActionStatementTimestamp *time.Time `json:"action_statement_timestamp,omitempty"`
}

// Vulnerability names the vulnerability a statement is about.
type Vulnerability struct {
	ID string `json:"@id,omitempty"`
	// Name is the identifier the scanner reports ("CVE-2024-12345").
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Aliases are other identifiers of the same vulnerability; each is
	// excepted alongside Name.
	Aliases []string `json:"aliases,omitempty"`
}

// Product identifies what a statement covers — for Tobby an image: a
// package URL of type oci, an OCI reference or a digest.
type Product struct {
	ID            string         `json:"@id,omitempty"`
	Identifiers   *Identifiers   `json:"identifiers,omitempty"`
	Hashes        *Hashes        `json:"hashes,omitempty"`
	Subcomponents []Subcomponent `json:"subcomponents,omitempty"`
}

// Subcomponent is a component of a product — for Tobby a package inside
// the image, named by its package URL.
type Subcomponent struct {
	ID          string       `json:"@id,omitempty"`
	Identifiers *Identifiers `json:"identifiers,omitempty"`
	Hashes      *Hashes      `json:"hashes,omitempty"`
}

// Identifiers are the software identifiers the specification defines.
type Identifiers struct {
	PURL  string `json:"purl,omitempty"`
	CPE22 string `json:"cpe22,omitempty"`
	CPE23 string `json:"cpe23,omitempty"`
}

// Hashes are the hash algorithms the specification defines. Read, never
// used to scope: a hash says which bytes, not which manifest a scan reads.
type Hashes struct {
	MD5        string `json:"md5,omitempty"`
	SHA1       string `json:"sha1,omitempty"`
	SHA256     string `json:"sha-256,omitempty"`
	SHA384     string `json:"sha-384,omitempty"`
	SHA512     string `json:"sha-512,omitempty"`
	SHA3224    string `json:"sha3-224,omitempty"`
	SHA3256    string `json:"sha3-256,omitempty"`
	SHA3384    string `json:"sha3-384,omitempty"`
	SHA3512    string `json:"sha3-512,omitempty"`
	BLAKE2s256 string `json:"blake2s-256,omitempty"`
	BLAKE2b256 string `json:"blake2b-256,omitempty"`
	BLAKE2b512 string `json:"blake2b-512,omitempty"`
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
