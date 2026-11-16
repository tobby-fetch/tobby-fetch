// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package scan

import (
	"slices"
	"strings"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/pathglob"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Mode is the enforcement mode of the policy (FR-031). Set at the
// instance only.
type Mode string

const (
	// ModeWarn is the advisory mode, and the default: violations are
	// reported on every surface and the transfer proceeds. Not a silent
	// removal of a control (FR-075): the scan still runs, its results are
	// still visible, and the UI says the policy is advisory.
	ModeWarn Mode = "warn"
	// ModeBlock refuses a violating item: it is never pushed to a
	// destination. An item that could not be scanned is refused too.
	ModeBlock Mode = "block"
)

// Valid reports whether m is one of the two modes.
func (m Mode) Valid() bool { return m == ModeWarn || m == ModeBlock }

// Level names where a policy setting came from, so a report can say which
// level decided (FR-031 amendment: "the most specific level wins").
type Level string

// The three levels, least specific first.
const (
	LevelInstance  Level = "instance"
	LevelRetriever Level = "retriever"
	LevelRecipe    Level = "recipe"
)

// Exception tolerates one vulnerability beyond the threshold, as declared
// at the instance or the Retriever (RECIPE-SPEC §10.4), or translated from
// a local OpenVEX document (FR-031 amendment 2026-11-16, R-15). A recipe
// carries none.
type Exception struct {
	// ID is the vulnerability identifier, matched exactly against
	// Finding.ID.
	ID string `json:"id"`
	// Justification says why the vulnerability is tolerated. Required at
	// the instance from R-15 on; for a VEX-born exception, the OpenVEX
	// justification and the document it came from.
	Justification string `json:"justification,omitempty"`
	// Expires bounds the exception. Required at the instance and for
	// VEX-born exceptions (R-15); zero only for a Retriever exception of
	// the v1alpha1 format that declares none — applied, and reported as
	// unbounded (TBY-SCN-028).
	Expires time.Time `json:"expires,omitzero"`
	// Level is where the exception was declared. A VEX-born exception is
	// declared by the instance (scan.vex) and carries LevelInstance; VEX
	// names its document.
	Level Level `json:"level"`
	// Scope narrows the exception (FR-031 amendment 2026-11-16, R-15).
	// Nil keeps the exception level-wide, as at milestone 6.
	Scope *ExceptionScope `json:"scope,omitempty"`
	// VEX names the local OpenVEX document an exception was translated
	// from (R-15); empty for an exception written by hand.
	VEX string `json:"vex,omitempty"`
}

// The sources of an exception, as the policy view and the
// tobby_scan_exceptions metric name them: where its author wrote it.
const (
	SourceInstance  = "instance"
	SourceRetriever = "retriever"
	SourceVEX       = "vex"
)

// Source names where the exception was written: "vex" for one translated
// from an OpenVEX document, otherwise its level.
func (e *Exception) Source() string {
	switch {
	case e.VEX != "":
		return SourceVEX
	case e.Level == LevelRetriever:
		return SourceRetriever
	}
	return SourceInstance
}

// Unbounded reports whether the exception carries no expiry: a Retriever
// exception of the v1alpha1 format, applied and reported as such.
func (e *Exception) Unbounded() bool { return e.Expires.IsZero() }

// ExceptionScope narrows an exception to the items it was meant for
// (R-15). The criteria present combine with AND; a criterion the item
// cannot be compared with (an unknown digest, repository or recipe)
// excludes it — a scope never matches by default.
type ExceptionScope struct {
	// Digest is a manifest digest, "sha256:<64 hex>", compared exactly
	// with the ingredient's digest.
	Digest string `json:"digest,omitempty"`
	// Repository is a nominal source repository pattern, canonical form
	// (ADR-0013), with the glob grammar of trust scopes.
	Repository string `json:"repository,omitempty"`
	// Recipe is a recipe name, optionally "name@version".
	Recipe string `json:"recipe,omitempty"`
	// Packages narrows a VEX-born exception to the affected packages its
	// statement names as subcomponents of the product: a finding in any
	// other package of the item is not covered. Never set by hand.
	Packages []string `json:"packages,omitempty"`
}

// IsZero reports whether the scope narrows nothing.
func (s *ExceptionScope) IsZero() bool {
	return s == nil || s.Digest == "" && s.Repository == "" && s.Recipe == "" && len(s.Packages) == 0
}

// String renders the scope for a reason parameter: the criteria present,
// "digest=… repository=… recipe=… packages=…".
func (s *ExceptionScope) String() string {
	if s.IsZero() {
		return ""
	}
	var parts []string
	if s.Digest != "" {
		parts = append(parts, "digest="+s.Digest)
	}
	if s.Repository != "" {
		parts = append(parts, "repository="+s.Repository)
	}
	if s.Recipe != "" {
		parts = append(parts, "recipe="+s.Recipe)
	}
	if len(s.Packages) > 0 {
		parts = append(parts, "packages="+strings.Join(s.Packages, ","))
	}
	return strings.Join(parts, " ")
}

// Covers reports whether the scope includes the finding f of the item
// subj. Every criterion present must hold.
func (s *ExceptionScope) Covers(subj *Subject, f *Finding) bool {
	if s == nil {
		return true
	}
	if s.Digest != "" && (subj.Digest == "" || s.Digest != subj.Digest) {
		return false
	}
	if s.Repository != "" && (subj.Repository == "" || !pathglob.Match(s.Repository, subj.Repository)) {
		return false
	}
	if s.Recipe != "" {
		if subj.Recipe == "" {
			return false
		}
		if strings.Contains(s.Recipe, "@") {
			if s.Recipe != subj.Recipe {
				return false
			}
		} else if name, _, _ := strings.Cut(subj.Recipe, "@"); name != s.Recipe {
			return false
		}
	}
	if len(s.Packages) > 0 && (f == nil || !slices.Contains(s.Packages, f.Package)) {
		return false
	}
	return true
}

// ExpiredAt reports whether the exception no longer applies at now: it
// carries an expiry, and now is past it.
func (e *Exception) ExpiredAt(now time.Time) bool {
	return !e.Expires.IsZero() && now.After(e.Expires)
}

// Levels is the raw input of one evaluation: what each level declares,
// before combination. Nil pointers mean "this level says nothing".
type Levels struct {
	// Instance is the configuration's scan.* section; it always has a
	// mode and a threshold (defaults apply).
	InstanceMode       Mode
	InstanceThreshold  float64
	InstanceExceptions []Exception
	// RetrieverThreshold and RetrieverExceptions come from the served
	// Retriever's spec.scanPolicy.
	RetrieverThreshold  *float64
	RetrieverExceptions []Exception
	// RecipeThreshold comes from the recipe annotation
	// recipe.tobby.dev/scan-threshold; it can only lower the result.
	RecipeThreshold *float64
}

// Policy is the policy in effect for one recipe, after combination.
type Policy struct {
	Mode Mode `json:"mode"`
	// ModeLevel is the level that made the policy blocking: the instance
	// when scan.mode is block, otherwise the Retriever or the recipe whose
	// threshold made it so (a threshold posed for a perimeter is blocking
	// for that perimeter — milestone-6 opening, correction of 2026-11-11).
	// Added after the contract; omitted from documents written before it.
	ModeLevel Level `json:"mode_level,omitempty"`
	// Threshold is the effective CVSS threshold, inclusive.
	Threshold float64 `json:"threshold"`
	// ThresholdLevel is the level that set it.
	ThresholdLevel Level `json:"threshold_level"`
	// IgnoredRecipeThreshold records a recipe annotation that would have
	// RAISED the threshold and was therefore ignored — reported, never
	// silent.
	IgnoredRecipeThreshold *float64 `json:"ignored_recipe_threshold,omitempty"`
	// Exceptions is the cumulated list, instance then Retriever.
	Exceptions []Exception `json:"exceptions,omitempty"`
}

// Verdict is the outcome of evaluating one item against its Policy.
type Verdict string

const (
	// VerdictPass means no finding violates the policy.
	VerdictPass Verdict = "pass"
	// VerdictWarn means findings violate the policy and the mode is advisory;
	// the item proceeds.
	VerdictWarn Verdict = "warn"
	// VerdictBlock means findings violate the policy (or the database is stale
	// under scan.maxDbAge.action block) and the item is refused.
	VerdictBlock Verdict = "block"
	// VerdictNotScanned means no Result could be produced — scanner missing,
	// database absent, scan failed. Under ModeBlock the item is refused
	// as well; the Reasons say why it was not scanned.
	VerdictNotScanned Verdict = "not-scanned"
	// VerdictOutOfScope means an ingredient declared as something other
	// than a container image was inspected and holds no image (ADR-0017):
	// there is nothing the scanner reads, and it proceeds whatever the
	// mode. Content says what was inspected.
	VerdictOutOfScope Verdict = "out-of-scope"
)

// Reason is one cause of a verdict, as a taxonomy code plus parameters —
// never a sentence (ADR-0015 §4).
type Reason struct {
	Code   taxonomy.Code     `json:"code"`
	Params map[string]string `json:"params,omitempty"`
}

// Evaluation is the evaluated policy of one item — "verdict, reasons,
// exceptions applied".
type Evaluation struct {
	Verdict Verdict `json:"verdict"`
	// Policy is the combined policy the item was evaluated against.
	Policy Policy `json:"policy"`
	// Reasons explain a warn, block or not-scanned verdict, and carry the
	// non-blocking notes every verdict may have — a stale database under
	// the warn action (TBY-SCN-004), an ignored recipe annotation, an
	// expired exception, unscored findings (TBY-SCN-020..022). A pass
	// verdict has no reason other than those notes.
	Reasons []Reason `json:"reasons,omitempty"`
	// Violations lists the identifiers of the findings at or above the
	// threshold that no exception covered.
	Violations []string `json:"violations,omitempty"`
	// ExceptionsApplied lists the exceptions that covered a finding which
	// would otherwise have violated the policy.
	ExceptionsApplied []Exception `json:"exceptions_applied,omitempty"`
	// ExceptionsExpired lists the exceptions that matched a finding but
	// were past their expiry, and therefore did not apply.
	ExceptionsExpired []Exception `json:"exceptions_expired,omitempty"`
	// ExceptionsOutOfScope lists the exceptions whose identifier matched
	// a violating finding but whose scope excludes the item (R-15): they
	// did not apply, and TBY-SCN-026 says so beside the finding.
	ExceptionsOutOfScope []Exception `json:"exceptions_out_of_scope,omitempty"`
	// Unscored lists the identifiers of UNKNOWN-severity findings without
	// a CVSS score: never compared with the threshold, never dropped.
	Unscored []string `json:"unscored,omitempty"`
}

// Counts summarizes findings by severity, for the UI and the metrics.
type Counts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
}

// Add counts one finding of severity s; anything outside the closed set
// counts as unknown.
func (c *Counts) Add(s Severity) {
	switch s {
	case SeverityCritical:
		c.Critical++
	case SeverityHigh:
		c.High++
	case SeverityMedium:
		c.Medium++
	case SeverityLow:
		c.Low++
	default:
		c.Unknown++
	}
}

// Report is the scan section of one item of a task report (FR-031: "results
// reported per item in the task, the API and the metrics"). It is the
// JSON the API serves under items[].scan.
type Report struct {
	// Scanner, ScannedAt and Database are absent when the item was not
	// scanned (Evaluation.Verdict == VerdictNotScanned).
	Scanner   *Info      `json:"scanner,omitempty"`
	ScannedAt time.Time  `json:"scanned_at,omitzero"`
	Database  *Database  `json:"database,omitempty"`
	Counts    Counts     `json:"counts"`
	Findings  []Finding  `json:"findings,omitempty"`
	Policy    Evaluation `json:"evaluation"`
	// Content is set for an ingredient not declared as a container image:
	// its declared kind and what its content was detected as (ADR-0017).
	Content *Content `json:"content,omitempty"`
}
