// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media

import (
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The verification report. This is the contract the Media screen (FR-062
// amendment R-02), the REST API (FR-060/FR-061) and `--output json`
// (FR-066) all read: it serializes as it stands, it carries codes and
// parameters rather than sentences — so every surface re-renders it in the
// reader's language (FR-063) long after the fact — and it says, for every
// refusal, both WHY and WHICH FILE (the FR-054 acceptance).

// Verdict is the medium-wide outcome.
type Verdict string

// The three outcomes. Partial is not a degraded form of Blocked: it is the
// normal state of a medium carrying several deliveries, one of which did
// not survive the trip (R-19).
const (
	// VerdictPushable means every recipe on the medium may be pushed.
	VerdictPushable Verdict = "pushable"
	// VerdictPartial means some recipes are pushable and some are blocked.
	VerdictPartial Verdict = "partial"
	// VerdictBlocked means nothing may be pushed: either a global block
	// stands, or every recipe is blocked.
	VerdictBlocked Verdict = "blocked"
)

// Report is the result of verifying one transported store.
type Report struct {
	// Verdict is the medium-wide outcome.
	Verdict Verdict `json:"verdict"`
	// Blocks are the medium-wide refusals (R-19). A block that was
	// overridden stays in the list, with Overridden set: the report is
	// also the evidence of what an administrator waved through (FR-094).
	Blocks []Block `json:"blocks,omitempty"`
	// Media describes the medium as it presents itself. Absent when the
	// manifest could not be read at all.
	Media *Info `json:"media,omitempty"`
	// Zone states the identity comparison that decided block 3.
	Zone ZoneCheck `json:"zone"`
	// Freshness states the R-28 comparison, when a previous import is on
	// record for the zone.
	Freshness *FreshnessCheck `json:"freshness,omitempty"`
	// Recipes carries one verdict per delivery, in manifest order.
	Recipes []RecipeVerdict `json:"recipes,omitempty"`
	// Findings are the non-blocking observations: content the medium
	// carries that will never be pushed, and bookkeeping that does not
	// match its inventory entry.
	Findings []Finding `json:"findings,omitempty"`
	// Checked is what verification actually read and hashed — the honest
	// volumetry, which is smaller than the inventory's when a global block
	// stopped the walk.
	Checked Totals `json:"checked"`
	// StartedAt and FinishedAt bound the verification.
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

// Pushable lists the recipes cleared for the destination push, in report
// order. It is empty whenever a global block stands.
func (r *Report) Pushable() []RecipeVerdict {
	if r.Verdict == VerdictBlocked {
		return nil
	}
	out := make([]RecipeVerdict, 0, len(r.Recipes))
	for i := range r.Recipes {
		if r.Recipes[i].Pushable {
			out = append(out, r.Recipes[i])
		}
	}
	return out
}

// Info is what the manifest says about the medium — claims, restated
// for display. Only the recipes and the inventory below them are checkable
// against bytes; the zone and the identity are anti-accident guards.
type Info struct {
	MediaID     string    `json:"mediaId"`
	Zone        string    `json:"zone"`
	MediaFormat int       `json:"mediaFormat"`
	StoreFormat int       `json:"storeFormat"`
	ProducedBy  Producer  `json:"producedBy"`
	ResolvedAt  time.Time `json:"resolvedAt"`
	WrittenAt   time.Time `json:"writtenAt"`
	// Totals is the inventory's own volumetry, as declared.
	Totals Totals `json:"totals"`
}

// ZoneCheck is the zone-identity comparison.
type ZoneCheck struct {
	// Expected is the zone this instance serves.
	Expected string `json:"expected"`
	// Found is the zone the medium is addressed to.
	Found string `json:"found"`
	// Match is true when the two agree.
	Match bool `json:"match"`
}

// FreshnessCheck is the R-28 comparison against the last completed import
// recorded for the zone. Both timestamps are always named, because a
// refusal an operator cannot date is a refusal they cannot act on.
type FreshnessCheck struct {
	// Resolved is the medium's resolution timestamp.
	Resolved time.Time `json:"resolved"`
	// Recorded is the resolution timestamp of the last import.
	Recorded time.Time `json:"recorded"`
	// RecordedMediaID is the medium that import came from.
	RecordedMediaID string `json:"recordedMediaId,omitempty"`
	// Stale is true when the medium is older than the record.
	Stale bool `json:"stale"`
}

// Block is one medium-wide refusal.
type Block struct {
	// Code is the stable taxonomy code (TBY-MED-…): every surface
	// re-renders what, cause and action from it (R-03).
	Code taxonomy.Code `json:"code"`
	// Params are the code's message parameters.
	Params map[string]string `json:"params,omitempty"`
	// Overridable says whether an administrator may proceed anyway. Only
	// the zone mismatch and the staleness guard ever are; an integrity
	// verdict never is (R-19).
	Overridable bool `json:"overridable"`
	// Overridden says the override was supplied and applied. The caller
	// is responsible for the FR-094 audit record: the actor and the
	// network origin belong to whoever authenticated them.
	Overridden bool `json:"overridden,omitempty"`
}

// RecipeVerdict is one delivery's outcome.
type RecipeVerdict struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// CookbookRepo is the nominal repository the trust decision was taken
	// on; ArtifactRepo is where the artifact sits on the medium.
	CookbookRepo string    `json:"cookbookRepo"`
	ArtifactRepo string    `json:"artifactRepo"`
	Digest       string    `json:"digest"`
	ResolvedAt   time.Time `json:"resolvedAt"`
	// Pushable is the decision. False means blocked whole, no override.
	Pushable bool `json:"pushable"`
	// Reason states why, when Pushable is false. Never nil in that case.
	Reason *Reason `json:"reason,omitempty"`
	// KeyFingerprint is the trust root that verified the signature.
	KeyFingerprint string `json:"keyFingerprint,omitempty"`
	// TrustScope names the declared scope that decided the verdict, when
	// one matched — the relaxation is always visible (FR-033).
	TrustScope string `json:"trustScope,omitempty"`
	// Unsigned records that the recipe carries no signature and was
	// admitted by an allowUnsigned scope. Never true outside one.
	Unsigned bool `json:"unsigned,omitempty"`
	// Files and Bytes are what this recipe reaches on the medium.
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// Reason is one refusal, with the file it is about.
type Reason struct {
	Code taxonomy.Code `json:"code"`
	// Path names the offending file, relative to the store root, in slash
	// form. Empty only for refusals about no file in particular (a
	// signature that does not verify).
	Path   string            `json:"path,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

// Finding is a non-blocking observation.
type Finding struct {
	Code   taxonomy.Code     `json:"code"`
	Path   string            `json:"path"`
	Params map[string]string `json:"params,omitempty"`
}

// Error renders a Block, a Reason or a Finding as the taxonomy error the
// UI, the API and the CLI already know how to display (R-03, FR-063).
func (b Block) Error() *taxonomy.Error   { return errorOf(b.Code, b.Params) }
func (r Reason) Error() *taxonomy.Error  { return errorOf(r.Code, r.Params) }
func (f Finding) Error() *taxonomy.Error { return errorOf(f.Code, f.Params) }

func errorOf(code taxonomy.Code, params map[string]string) *taxonomy.Error {
	if _, known := taxonomy.Lookup(code); !known {
		// A report can come back through JSON — from an older instance, or
		// from a hand-edited file. taxonomy.New panics on an unknown code,
		// which is right for a programming error and wrong for data: a
		// rendering surface must not take the process down over a string
		// it was handed.
		return taxonomy.New(taxonomy.CodeInternal, nil)
	}
	p := make(taxonomy.Params, len(params))
	for k, v := range params {
		p[k] = v
	}
	return taxonomy.New(code, p)
}

// Stage names the phase verification is in, for progress display.
type Stage string

// The stages, in the order FR-054 mandates: the manifest and its
// checksums, then the recipes' signatures and their ingredients, then the
// sweep for content nothing reaches.
const (
	// StageInventory is the writer's walk (Write reports on it too: the
	// same operator watches the same bytes go by).
	StageInventory Stage = "inventory"
	// StageManifest is reading and validating meta/media.json.
	StageManifest Stage = "manifest"
	// StageRecipes is the per-recipe walk: files, then signature.
	StageRecipes Stage = "recipes"
	// StageExtraneous is the final sweep for uncovered and unreachable
	// content.
	StageExtraneous Stage = "extraneous"
)

// Progress is one progress notification. It is a value, not a pointer:
// callbacks may keep it, ship it to a websocket, or drop it.
//
// The counters are cumulative over the whole verification. TotalFiles and
// TotalBytes are the inventory's declared volumetry — a claim from the
// medium, useful for a progress bar and for nothing else.
type Progress struct {
	Stage Stage `json:"stage"`
	// Recipe is "name@version" while Stage is StageRecipes.
	Recipe     string `json:"recipe,omitempty"`
	Files      int    `json:"files"`
	TotalFiles int    `json:"totalFiles,omitempty"`
	Bytes      int64  `json:"bytes"`
	TotalBytes int64  `json:"totalBytes,omitempty"`
}

// report calls a progress callback when there is one.
func report(f func(Progress), p Progress) {
	if f != nil {
		f(p)
	}
}
