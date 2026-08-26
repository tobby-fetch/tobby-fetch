// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The plan screen (FR-055 amendment R-04, UI-SPEC §6): /recipes/plan
// simulates a synchronization and shows the whole report — resolved
// versions (FR-021), per-digest statuses (FR-026), volumes and the
// free-space verdict (FR-055), projected prune (FR-045), and the policy
// verdicts reachable without a transfer (FR-030, FR-033). The strict
// mirror of POST /api/v1/plan (FR-061).
//
// The GET renders the form and nothing else. A plan contacts every
// registry the Retriever names, and a screen that ran one on every page
// load would turn a browser refresh — or a link preview — into a burst of
// outbound requests nobody asked for. The plan runs on the POST, which is
// also what carries the candidate document.
//
// Every label the templates print is a catalog key computed here, never a
// string built in a template: the report's own vocabulary (outcomes,
// signature verdicts, warnings) is a frozen glossary of hyphenated
// identifiers, and turning one into a message id is Go's job (FR-063,
// ADR-0015).

package ui

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/preflight"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Planner is the plan engine as this screen consumes it. An interface
// rather than the concrete type, for the reason every other seam in this
// package is one: the screen is rendered in tests against a store with no
// registry behind it, and a nil here renders the form inert rather than
// panicking.
type Planner interface {
	Plan(ctx context.Context, opts engine.PlanOptions) (*engine.Plan, error)
}

// planData feeds the /recipes/plan page.
type planData struct {
	// Source is the configured Retriever (FR-010), shown as the default
	// subject of the plan.
	Source string
	// Candidate and Document are what the operator submitted, echoed back
	// into the form.
	Candidate string
	Document  string
	// Plan is the report, nil before the first run.
	Plan *engine.Plan
	// Recipes decorates the report's recipes with their catalog keys.
	Recipes []planRecipeRow
	// Checks decorates the FR-055 verdicts with their localized blocks.
	Checks []planCheckRow
	// OutcomeKey and OutcomeClass are the verdict's catalog key and badge
	// modifier.
	OutcomeKey   string
	OutcomeClass string
	// ExitCode is what the CLI would exit with for this outcome (FR-066),
	// shown so a screen and a pipeline describe the same run identically.
	ExitCode int
}

// planRecipeRow is one planned recipe as the screen shows it.
type planRecipeRow struct {
	engine.PlanRecipe
	SignatureKey   string
	SignatureClass string
}

// planCheckRow is one pre-flight verdict as the screen shows it.
type planCheckRow struct {
	preflight.Check
	// WarningKeys are the catalog keys of the check's warnings.
	WarningKeys []string
	// RefusalErr is the localized refusal, nil when the check passed.
	RefusalErr *ErrView
}

// planScreen serves GET /recipes/plan: the form, no plan run.
func (u *UI) planScreen(w http.ResponseWriter, r *http.Request) {
	u.render.Page(w, r, "recipe-plan", &planData{Source: u.retrieverSource})
}

// planSubmit serves POST /recipes/plan: run the plan and render it.
func (u *UI) planSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
			"file": "the submitted form", "path": "(root)", "constraint": err.Error(),
		}))
		return
	}
	data := &planData{
		Source:    u.retrieverSource,
		Candidate: strings.TrimSpace(r.PostFormValue("retriever")),
		Document:  r.PostFormValue("document"),
	}
	if u.planner == nil {
		u.contentError(w, r, "recipe-plan", data, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "this instance runs no recipe engine: there is nothing to plan",
		}))
		return
	}
	if data.Source == "" && data.Candidate == "" && strings.TrimSpace(data.Document) == "" {
		u.contentError(w, r, "recipe-plan", data, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "no Retriever source is configured (retriever.source, FR-010) and none was submitted: a plan has nothing to resolve",
		}))
		return
	}

	plan, err := u.planner.Plan(r.Context(), engine.PlanOptions{
		Retriever:         data.Candidate,
		RetrieverDocument: []byte(data.Document),
	})
	if err != nil {
		u.contentError(w, r, "recipe-plan", data, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	decoratePlan(data, requestLang(r), plan)
	u.render.Page(w, r, "recipe-plan", data)
}

// decoratePlan turns a report into what the template renders.
func decoratePlan(data *planData, lang string, plan *engine.Plan) {
	data.Plan = plan
	data.ExitCode = plan.ExitCode()
	data.OutcomeKey = "plan.outcome_" + keyOf(string(plan.Outcome))
	data.OutcomeClass = outcomeClass(plan.Outcome)
	for i := range plan.Recipes {
		data.Recipes = append(data.Recipes, planRecipeRow{
			PlanRecipe:     plan.Recipes[i],
			SignatureKey:   "plan.sig_" + keyOf(string(plan.Recipes[i].Signature)),
			SignatureClass: signatureClass(plan.Recipes[i].Signature),
		})
	}
	for i := range plan.Checks {
		c := plan.Checks[i]
		row := planCheckRow{Check: c}
		for _, warn := range c.Warnings {
			row.WarningKeys = append(row.WarningKeys, "plan.warn_"+keyOf(string(warn)))
		}
		if !c.OK() {
			if e := refusalFor(&c); e != nil {
				row.RefusalErr = errView(lang, e)
			}
		}
		data.Checks = append(data.Checks, row)
	}
}

// keyOf turns a frozen glossary value ("changes-planned") into the
// catalog-key fragment the message ids use ("changes_planned").
func keyOf(v string) string { return strings.ReplaceAll(v, "-", "_") }

// outcomeClass maps a verdict onto a badge modifier of app.css.
func outcomeClass(o engine.Outcome) string {
	switch o {
	case engine.OutcomeUpToDate:
		return "up-to-date"
	case engine.OutcomeChangesPlanned:
		return "outdated"
	default:
		return "failed"
	}
}

// signatureClass maps an FR-033 verdict onto a badge modifier. An
// admitted-unsigned recipe gets the "done with errors" pill, never the
// plain success one: the relaxation is declared, and it stays visible.
func signatureClass(s engine.Signature) string {
	switch s {
	case engine.SignatureVerified:
		return "up-to-date"
	case engine.SignatureRefused:
		return "failed"
	default:
		return "done-errors"
	}
}

// refusalFor rebuilds the refusal error of a check, so the screen renders
// the same three-part block every other error goes through (R-03) rather
// than a sentence written here.
func refusalFor(c *preflight.Check) *taxonomy.Error {
	switch c.RefusalCode {
	case taxonomy.CodeFileTooLarge:
		return taxonomy.New(taxonomy.CodeFileTooLarge, taxonomy.Params{
			"path": c.Path, "filesystem": c.Filesystem.Type,
			"limit": bytesParam(c.Filesystem.MaxFileSize), "size": bytesParam(c.LargestFileBytes),
			"what": string(c.Target),
		})
	case taxonomy.CodeInsufficientSpace:
		return taxonomy.New(taxonomy.CodeInsufficientSpace, taxonomy.Params{
			"path": c.Path, "needed": bytesParam(c.ProjectedBytes),
			"available": bytesParam(c.UsableBytes), "shortfall": bytesParam(c.ShortfallBytes),
			"margin": bytesParam(int64(c.MarginPercent)), "free": bytesParam(c.Space.FreeBytes),
		})
	default:
		return nil
	}
}

// bytesParam renders a byte count for a taxonomy parameter: raw digits,
// because the templates state exact bytes and the localized form belongs
// to the message, not to the number (ADR-0015 §7).
func bytesParam(n int64) string { return strconv.FormatInt(n, 10) }
