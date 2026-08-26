// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/preflight"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The plan report (FR-055, amendment 2026-08-11 / R-04).
//
// This file is a data contract, deliberately kept apart from the code
// that fills it. Three surfaces render it — the CLI's `--output json`,
// `POST /api/v1/plan`, and the /recipes/plan screen — and the milestone's
// Media screen (R-02) consumes it as-is. So every field is exported,
// every field has a JSON name, and nothing in here is localized: a report
// is stored, re-read, and re-rendered in the reader's language (R-03,
// ADR-0015 §7), which a frozen English sentence would make impossible.
//
// Byte counts are raw int64 and timestamps are RFC 3339 through
// encoding/json's time.Time, the same rule the rest of the API follows.

// Outcome is the verdict of a plan, and the thing a CI gate branches on.
type Outcome string

// The plan outcomes. They map onto process exit codes through ExitCode.
const (
	// OutcomeUpToDate is "nothing to do": every ingredient of every
	// recipe is already present at the pinned digest.
	OutcomeUpToDate Outcome = "up-to-date"
	// OutcomeChangesPlanned is "there is work": at least one item is new
	// or outdated, or a prune is projected.
	OutcomeChangesPlanned Outcome = "changes-planned"
	// OutcomePolicyRefused is a refusal by explicit policy — the registry
	// allow-list of FR-030. The plan stops short of the destination:
	// FR-055 requires a policy-refused plan not to contact it.
	OutcomePolicyRefused Outcome = "policy-refused"
	// OutcomeVerificationFailed is a signature verdict no configured
	// trust root satisfies (FR-033), or content that does not match its
	// pinned digest.
	OutcomeVerificationFailed Outcome = "verification-failed"
	// OutcomeFailed is everything else: an unreachable registry, an
	// unresolvable version, an unreadable store.
	OutcomeFailed Outcome = "failed"
)

// ExitCode maps the outcome onto the process exit code of FR-066, so the
// CLI, the API and any script agree on what happened. "Nothing to do" and
// "changes planned" are both successes with different codes — a gate has
// to be able to tell them apart without parsing anything.
func (o Outcome) ExitCode() int {
	switch o {
	case OutcomeUpToDate:
		return taxonomy.ExitOK
	case OutcomeChangesPlanned:
		return taxonomy.ExitChangesPlanned
	case OutcomePolicyRefused:
		return taxonomy.ExitPolicy
	case OutcomeVerificationFailed:
		return taxonomy.ExitVerification
	default:
		return taxonomy.ExitFailure
	}
}

// ItemStatus is the FR-026 per-digest status of one ingredient relative to
// a target. The values are the importer's frozen glossary — they are
// never translated (ADR-0015 §7).
type ItemStatus string

// The FR-026 statuses, plus the one a plan needs and a transfer does not:
// a status that could not be established at all.
const (
	StatusNew       ItemStatus = "new"
	StatusOutdated  ItemStatus = "outdated"
	StatusUpToDate  ItemStatus = "up-to-date"
	StatusUnknown   ItemStatus = "unknown"
	StatusNotProbed ItemStatus = "not-probed"
)

// Problem is a taxonomy error as a plan carries it: the stable code and
// its parameters, never a rendered sentence (R-03). Subject names what
// the problem is about — a recipe reference, an ingredient reference.
type Problem struct {
	Code taxonomy.Code `json:"code"`
	// Class is the taxonomy class as a word ("operational", "policy",
	// "verification"), so a consumer that does not link against the
	// taxonomy can still sort by severity.
	Class   string         `json:"class"`
	Subject string         `json:"subject,omitempty"`
	Params  map[string]any `json:"params,omitempty"`
}

// newProblem captures a taxonomy error for the report.
func newProblem(subject string, e *taxonomy.Error) *Problem {
	p := &Problem{Code: e.Code(), Subject: subject, Class: className(e.Entry().Class)}
	if len(e.ParamsMap()) > 0 {
		p.Params = map[string]any(e.ParamsMap())
	}
	return p
}

// className renders a taxonomy class for the report.
func className(c taxonomy.Class) string {
	switch c {
	case taxonomy.ClassPolicy:
		return "policy"
	case taxonomy.ClassVerification:
		return "verification"
	default:
		return "operational"
	}
}

// Signature is the FR-033 verdict on a recipe, evaluated without pulling
// any ingredient content — which is exactly the subset R-04 asks a plan
// to report.
type Signature string

// The signature verdicts.
const (
	// SignatureVerified: a configured trust root validated the recipe.
	SignatureVerified Signature = "verified"
	// SignatureUnsignedAdmitted: no signature, admitted by an explicitly
	// declared allowUnsigned scope (RECIPE-SPEC §12.3). Never silent.
	SignatureUnsignedAdmitted Signature = "unsigned-admitted"
	// SignatureRefused: verification ran and failed.
	SignatureRefused Signature = "refused"
	// SignatureNotEvaluated: the recipe was never reached (the version
	// did not resolve, the registry refused the connection).
	SignatureNotEvaluated Signature = "not-evaluated"
)

// PlanIngredient is one ingredient of a planned recipe.
type PlanIngredient struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Ref is the nominal reference the recipe names; Effective is the
	// endpoint actually contacted when source substitution applies
	// (FR-036), empty when it does not.
	Ref       string `json:"ref"`
	Effective string `json:"effective,omitempty"`
	// Repo is the relocated repository the content lands in (FR-035).
	Repo    string `json:"repo"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	// Status is the FR-026 verdict against the local store.
	Status ItemStatus `json:"status"`
	// PushStatus is the FR-026 verdict against the configured
	// destination, "not-probed" when there is none or when the plan
	// stopped before contacting it.
	PushStatus ItemStatus `json:"push_status"`
	// TotalBytes is everything the ingredient is made of, deduplicated by
	// digest within the ingredient; TransferBytes is the part the local
	// store does not already hold.
	TotalBytes    int64 `json:"total_bytes"`
	TransferBytes int64 `json:"transfer_bytes"`
	// LargestFileBytes is the biggest single blob — the number the FAT32
	// verdict of FR-055 is about.
	LargestFileBytes int64    `json:"largest_file_bytes"`
	Problem          *Problem `json:"problem,omitempty"`
}

// PlanRecipe is one entry of the Retriever as the plan resolved it.
type PlanRecipe struct {
	Name string `json:"name"`
	// Requested is the version expression from the Retriever, Resolved
	// the concrete tag it resolved to (FR-021).
	Requested string `json:"requested"`
	Resolved  string `json:"resolved,omitempty"`
	// Digest is the recipe artifact's pinned digest.
	Digest string `json:"digest,omitempty"`
	// Cookbook is the nominal cookbook repository the recipe came from.
	Cookbook string `json:"cookbook"`
	// TrustScope is the declared scope that decided the verdict (FR-033).
	TrustScope  string           `json:"trust_scope,omitempty"`
	Signature   Signature        `json:"signature"`
	Ingredients []PlanIngredient `json:"ingredients,omitempty"`
	// TotalBytes and TransferBytes are deduplicated by digest WITHIN this
	// recipe. They do not add up to the totals when two recipes share a
	// base layer, and that is the correct behaviour: the shared layer
	// belongs to both recipes and crosses the wire once.
	TotalBytes       int64    `json:"total_bytes"`
	TransferBytes    int64    `json:"transfer_bytes"`
	LargestFileBytes int64    `json:"largest_file_bytes"`
	Problem          *Problem `json:"problem,omitempty"`
}

// PrunePlan is the projected FR-045 prune: recipe-managed content the
// resolved Retriever no longer references.
type PrunePlan struct {
	// Evaluated is false when the projection was deliberately not
	// computed — see Reason. A plan that failed to resolve one recipe
	// MUST NOT propose deleting that recipe's content: a transient
	// registry error would otherwise read as "this is no longer wanted".
	Evaluated bool `json:"evaluated"`
	// Reason states why Evaluated is false.
	Reason string `json:"reason,omitempty"`
	// Repositories are the prune candidates with their logical size,
	// sorted by path.
	Repositories []PruneEntry `json:"repositories,omitempty"`
	// TotalBytes is the sum of the candidates' sizes.
	TotalBytes int64 `json:"total_bytes"`
}

// PruneEntry is one repository the prune would remove.
type PruneEntry struct {
	Repo string `json:"repo"`
	// Recipe names the recipe that brought the content, when the recipe
	// graph still records it — the FR-045 confirmation screen shows it.
	Recipe string `json:"recipe,omitempty"`
	Bytes  int64  `json:"bytes"`
}

// PlanTotals is the aggregate FR-055 asks to be displayed at trigger
// time. Every byte count here is deduplicated by digest across the whole
// plan and net of what the local store already holds, except TotalBytes,
// which is the gross figure.
type PlanTotals struct {
	Recipes     int `json:"recipes"`
	Ingredients int `json:"ingredients"`
	New         int `json:"new"`
	Outdated    int `json:"outdated"`
	UpToDate    int `json:"up_to_date"`

	TotalBytes       int64 `json:"total_bytes"`
	TransferBytes    int64 `json:"transfer_bytes"`
	LargestFileBytes int64 `json:"largest_file_bytes"`

	// StoreBytes is the store's current on-disk blob size and
	// ProjectedStoreBytes what it becomes once the transfer lands. The
	// projection deliberately ignores the prune: a prune runs AFTER the
	// synchronization (FR-045), so the peak the volume has to survive is
	// the un-pruned one.
	StoreBytes          int64 `json:"store_bytes"`
	ProjectedStoreBytes int64 `json:"projected_store_bytes"`

	// PushUpperBoundBytes is what a promotion would move to the
	// destination, and it is an UPPER BOUND, not a measurement: it sums
	// the artifacts the destination does not already hold at the pinned
	// digest, without asking it blob by blob. The differential push of
	// FR-028 will move less whenever the destination already holds a
	// shared layer. It is reported as a bound rather than omitted
	// because the operator planning a transfer window needs a number
	// that cannot be exceeded, and reported as a bound rather than as a
	// total because a number that quietly overstates is how estimates
	// stop being believed.
	PushUpperBoundBytes int64 `json:"push_upper_bound_bytes"`
	// PushEvaluated reports whether the destination was contacted at all.
	PushEvaluated bool `json:"push_evaluated"`
}

// PolicyReport is the FR-030 allow-list verdict of a plan: the policy in
// force, and the verdict on every registry host the plan would contact.
//
// The refusals alone would not be enough. A plan whose recipes all live
// on allowed registries reports nothing about the allow-list unless the
// verdicts are stated positively, and "no refusal" and "the policy was
// evaluated and passed" look identical from the outside — which is
// exactly the ambiguity FR-030's reporting exists to close.
type PolicyReport struct {
	// AllowlistDeclared distinguishes an unrestricted instance (no
	// allow-list key at all) from one whose list happens to cover
	// everything, the way package policy does.
	AllowlistDeclared bool          `json:"allowlist_declared"`
	AllowlistPatterns []string      `json:"allowlist_patterns,omitempty"`
	Hosts             []HostVerdict `json:"hosts,omitempty"`
}

// HostVerdict is the allow-list verdict on one host, named as it will be
// dialled — under source substitution (FR-036) that is not the host the
// recipe names, and the policy is about the wire (ADR-0013).
type HostVerdict struct {
	Host    string `json:"host"`
	Allowed bool   `json:"allowed"`
	// Role is "source", "cookbook" or "destination".
	Role string `json:"role"`
}

// Plan is the complete side-effect-free report of a synchronization that
// has not run (FR-055 amendment R-04).
type Plan struct {
	// Mode is the instance's operating mode (FR-001): a plan is available
	// in both, and what it means differs — in mirror mode it describes
	// what a manual trigger would do, in passthrough what the next
	// reconciliation cycle would.
	Mode string `json:"mode"`
	// Source is the Retriever the plan ran over: the configured one, or
	// the candidate file the caller supplied.
	Source string `json:"source"`
	// SourceIsCandidate reports that Source is a caller-supplied
	// candidate rather than the instance's configured Retriever — the
	// difference between "what would happen" and "what would happen if I
	// deployed this file".
	SourceIsCandidate bool `json:"source_is_candidate"`
	// Zone is the Retriever's metadata.name.
	Zone        string    `json:"zone,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`

	// Destination is the promotion target host, empty on an instance that
	// promotes nothing.
	Destination string `json:"destination,omitempty"`

	Outcome Outcome      `json:"outcome"`
	Recipes []PlanRecipe `json:"recipes,omitempty"`
	Totals  PlanTotals   `json:"totals"`
	Policy  PolicyReport `json:"policy"`
	Prune   PrunePlan    `json:"prune"`
	// Checks are the FR-055 space and filesystem verdicts of every target
	// this plan would write to.
	Checks []preflight.Check `json:"checks,omitempty"`
	// Problems collects every failure of the run, recipe-scoped ones
	// included, so a consumer can find them all without walking the tree.
	Problems []Problem `json:"problems,omitempty"`
}

// HasChanges reports whether the plan found work to do.
func (p *Plan) HasChanges() bool {
	return p.Totals.New > 0 || p.Totals.Outdated > 0 || p.Prune.TotalBytes > 0 ||
		len(p.Prune.Repositories) > 0 || p.Totals.PushUpperBoundBytes > 0
}

// ExitCode is the process exit code of the plan's outcome (FR-066).
func (p *Plan) ExitCode() int { return p.Outcome.ExitCode() }

// Refused reports whether any pre-flight check blocked the operation.
func (p *Plan) Refused() bool {
	for i := range p.Checks {
		if !p.Checks[i].OK() {
			return true
		}
	}
	return false
}
