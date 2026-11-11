// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package integrity is the vocabulary of the on-demand store verification
// (FR-057, roadmap R-31, milestone 6): the timestamped report every
// surface renders — the command line, the REST API and the interface —
// and the defects it is made of.
//
// It is a leaf on purpose. The report travels inside a task document
// (internal/tasks), which is why this package knows nothing of the store,
// the signature verifier or the queue; the walk that fills it lives in
// internal/integrity/check.
//
// A defect is a taxonomy code and its parameters, never a sentence
// (ADR-0015 §4): the report is re-rendered in the reader's language long
// after it was written, and the code is what a script keys on. Every
// defect names the file or the object it is about.
//
// What the verification does NOT do is as normative as what it does: it
// repairs nothing, deletes nothing and writes nothing into the store. A
// report is the answer; reclaiming an orphan or re-fetching a damaged blob
// is the operator's decision, taken with the means the product already has
// (prune, re-synchronization).
package integrity

import (
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/assurance"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Verdict is the outcome of a completed verification. The set is closed
// and part of the documented surface (it is also the label of the
// tobby_integrity_verifications_total metric).
type Verdict string

// The two verdicts. Orphaned content never makes a store "defective": it
// is listed as a finding, and listing it is the whole of what it asks.
const (
	// VerdictClean means no defect was found.
	VerdictClean Verdict = "clean"
	// VerdictDefects means at least one defect was found: a blob that does
	// not match its digest, an incomplete manifest, an unreadable
	// manifest, or a recipe signature that no longer verifies.
	VerdictDefects Verdict = "defects"
)

// Stage names a phase of the walk, for progress and for the per-stage
// items of the task.
type Stage string

// The stages, in the order the walk takes them.
const (
	// StageBlobs re-hashes every blob against the digest it is stored
	// under.
	StageBlobs Stage = "blobs"
	// StageManifests walks every manifest reachable from a recorded recipe
	// and checks that what it references is present.
	StageManifests Stage = "manifests"
	// StageSignatures re-verifies every recorded recipe's signature
	// against the trust roots configured on this instance.
	StageSignatures Stage = "signatures"
	// StageOrphans lists the content no recipe and no protected root
	// reaches.
	StageOrphans Stage = "orphans"
)

// Stages lists the stages in walk order.
func Stages() []Stage { return []Stage{StageBlobs, StageManifests, StageSignatures, StageOrphans} }

// Defect is one finding: a taxonomy code, the file or object it is about,
// and the parameters its message needs.
type Defect struct {
	Code taxonomy.Code `json:"code"`
	// Path is the file or object concerned: a blob path relative to the
	// store root, in slash form, a "repository:tag" or "repository@digest"
	// reference, or a recipe ("name@version"). Never empty.
	Path   string            `json:"path"`
	Params map[string]string `json:"params,omitempty"`
}

// Error rebuilds the taxonomy error a surface renders. A code this build
// does not know — a report written by a newer one — degrades to the
// internal error rather than panicking, like every other persisted code.
func (d *Defect) Error() *taxonomy.Error {
	if _, known := taxonomy.Lookup(d.Code); !known {
		return taxonomy.New(taxonomy.CodeInternal, nil)
	}
	p := make(taxonomy.Params, len(d.Params))
	for k, v := range d.Params {
		p[k] = v
	}
	return taxonomy.New(d.Code, p)
}

// Blocking reports whether the defect makes the verdict "defects": every
// code of the verification class does, and an orphan — an operational
// note — does not.
func (d *Defect) Blocking() bool {
	e, ok := taxonomy.Lookup(d.Code)
	return ok && e.Class == taxonomy.ClassVerification
}

// Signature states of a recorded recipe, as the verification found them.
type Signature string

// The signature states.
const (
	// SignatureVerified: the signature verifies against a trust root of
	// this instance.
	SignatureVerified Signature = "verified"
	// SignatureUnsignedAdmitted: no signature, admitted by a declared
	// scope that allows it — reduced assurance (FR-037).
	SignatureUnsignedAdmitted Signature = "unsigned-admitted"
	// SignatureRefused: the signature does not verify (or is absent where
	// a signature is required) under the trust configuration of this
	// instance now.
	SignatureRefused Signature = "refused"
	// SignatureNotChecked: the recipe's artifact is missing or unreadable,
	// so there was nothing to verify — the manifest defect says why.
	SignatureNotChecked Signature = "not-checked"
)

// Recipe is the verification's row for one recorded recipe: whether its
// content is complete, and how its signature fares now.
type Recipe struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	CookbookRepo string `json:"cookbook_repo,omitempty"`
	// Complete is true when every manifest reachable from the recipe is
	// complete in the store.
	Complete  bool      `json:"complete"`
	Signature Signature `json:"signature"`
	// KeyFingerprint is the trust root that verified the signature.
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	// TrustScope names the declared scope that decided the verdict, when
	// one matched.
	TrustScope string `json:"trust_scope,omitempty"`
	// Assurance re-reports the reduced-assurance marker of the recipe as
	// THIS instance's trust configuration sees it now (FR-037): a recipe
	// admitted unsigned when it was synchronized carries it only while a
	// declared scope still admits it.
	Assurance *assurance.Marker `json:"assurance,omitempty"`
}

// Store describes what was verified.
type Store struct {
	// Root is the store directory.
	Root string `json:"root"`
	// MediaID is the identity of the medium this store is (R-28).
	MediaID string `json:"media_id,omitempty"`
	// Transportable is true when the store carries a media manifest
	// (meta/media.json): it is the delivery unit of a mirror
	// synchronization, or a medium that arrived. The verification is the
	// same in both modes; this says which one it was.
	Transportable bool `json:"transportable"`
}

// Totals counts what the walk covered.
type Totals struct {
	Blobs     int   `json:"blobs"`
	Bytes     int64 `json:"bytes"`
	Manifests int   `json:"manifests"`
	Recipes   int   `json:"recipes"`
}

// Progress is the state of a walk in progress, published while it runs
// (FR-057: a verification is a tracked task whose progress is visible).
type Progress struct {
	Stage Stage `json:"stage"`
	// Done and Total count the stage's units: blobs for StageBlobs,
	// recipes for the two recipe stages. Total is zero while unknown.
	Done  int   `json:"done"`
	Total int   `json:"total"`
	Bytes int64 `json:"bytes,omitempty"`
}

// MaxListed bounds each list of a report. A store with a million damaged
// blobs has one problem, not a million lines; the counts below stay exact
// and the first MaxListed of each list are named.
const MaxListed = 500

// Report is the timestamped result of one verification, the same document
// on every surface (FR-061).
type Report struct {
	// RunID correlates the report with the task's log records (FR-090).
	RunID string `json:"run_id,omitempty"`
	// StartedAt and FinishedAt timestamp the verification. FinishedAt is
	// zero while it runs.
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	// Verdict is empty while the verification runs.
	Verdict Verdict `json:"verdict,omitempty"`
	Store   Store   `json:"store"`
	Checked Totals  `json:"checked"`
	// Recipes has one row per recorded recipe.
	Recipes []Recipe `json:"recipes,omitempty"`
	// Defects are the blocking findings; Findings the non-blocking ones
	// (orphaned content). Each is bounded by MaxListed, and the Omitted
	// counters say how many more there were.
	Defects         []Defect `json:"defects,omitempty"`
	DefectsOmitted  int      `json:"defects_omitted,omitempty"`
	Findings        []Defect `json:"findings,omitempty"`
	FindingsOmitted int      `json:"findings_omitted,omitempty"`
	// DefectCount and FindingCount are the exact totals, listed or not.
	DefectCount  int `json:"defect_count"`
	FindingCount int `json:"finding_count"`
	// Progress is set while the verification runs and absent from a
	// finished report.
	Progress *Progress `json:"progress,omitempty"`
}

// Running reports whether the report is a snapshot of a walk in progress.
func (r *Report) Running() bool { return r != nil && r.FinishedAt.IsZero() }

// Principal is the defect the exit code and the task-level failure are
// taken from: the principal error of the blocking defects by the single
// rule of taxonomy.Principal (verification outranks operational, then the
// most frequent code). Nil for a clean report. It is computed from the
// LISTED defects, which carry every code the walk found first.
func (r *Report) Principal() *taxonomy.Error {
	var errs []*taxonomy.Error
	for i := range r.Defects {
		if r.Defects[i].Blocking() {
			errs = append(errs, r.Defects[i].Error())
		}
	}
	return taxonomy.Principal(errs)
}
