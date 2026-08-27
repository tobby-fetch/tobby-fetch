// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/preflight"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The plan screen (FR-055 amendment R-04, UI-SPEC §6) and its FR-061
// parity with POST /api/v1/plan.

// stubPlanner returns a fixed report. The plan ENGINE is tested where it
// lives (internal/engine); what this file is about is what the screen
// does with a report — which is where the localization, the ordering and
// the refusal rendering live.
type stubPlanner struct {
	plan *engine.Plan
	err  error
	// calls counts the runs, so the GET/POST split is observable: a
	// screen that planned on page load would show up here.
	calls int
}

func (s *stubPlanner) Plan(context.Context, engine.PlanOptions) (*engine.Plan, error) {
	s.calls++
	return s.plan, s.err
}

// samplePlan is a report with one of everything the screen renders.
func samplePlan() *engine.Plan {
	return &engine.Plan{
		Mode: "mirror", Source: "./retriever.yaml", Zone: "zone-a",
		Outcome: engine.OutcomeChangesPlanned,
		Totals: engine.PlanTotals{
			Recipes: 1, Ingredients: 2, New: 1, UpToDate: 1,
			TransferBytes: 3145728, TotalBytes: 6291456,
			StoreBytes: 1048576, ProjectedStoreBytes: 4194304,
			LargestFileBytes: 2097152,
		},
		Policy: engine.PolicyReport{
			AllowlistDeclared: true,
			AllowlistPatterns: []string{"registry.example.com"},
			Hosts: []engine.HostVerdict{
				{Host: "registry.example.com", Allowed: true, Role: "source"},
				{Host: "rogue.example.net", Allowed: false, Role: "cookbook"},
			},
		},
		Prune: engine.PrunePlan{
			Evaluated:  true,
			TotalBytes: 4096,
			Repositories: []engine.PruneEntry{
				{Repo: "docker.io/bitnami/old", Recipe: "legacy", Bytes: 4096},
			},
		},
		Recipes: []engine.PlanRecipe{{
			Name: "wordpress", Requested: "^6.8", Resolved: "6.8.2",
			Cookbook:  "registry.example.com/cookbook/wordpress",
			Signature: engine.SignatureUnsignedAdmitted, TrustScope: "legacy-zone",
			TransferBytes: 3145728,
			Ingredients: []engine.PlanIngredient{
				{
					Name: "app", Kind: "ContainerImage",
					Ref: "docker.io/bitnami/wordpress", Effective: "mirror.example.com/docker.io/bitnami/wordpress",
					Status: engine.StatusNew, PushStatus: engine.StatusNotProbed,
					TransferBytes: 3145728,
				},
				{
					Name: "chart", Kind: "HelmChart",
					Ref:    "docker.io/bitnamicharts/wordpress",
					Status: engine.StatusUpToDate, PushStatus: engine.StatusNotProbed,
				},
			},
		}},
		Checks: []preflight.Check{{
			Target: preflight.TargetStore, Path: "/var/lib/tobby",
			Filesystem:    preflight.Filesystem{Type: "0xdeadbeef", Detection: "statfs(2) f_type"},
			Space:         preflight.Space{FreeBytes: 10 << 30, TotalBytes: 100 << 30, Known: true},
			MarginPercent: 10, ReservedBytes: 1 << 30, UsableBytes: 9 << 30,
			ProjectedBytes: 3145728, LargestFileBytes: 2097152,
			Warnings: []preflight.Warning{preflight.WarnFilesystemUnidentified},
		}},
	}
}

func newPlanUI(t *testing.T, p Planner) *UI {
	t.Helper()
	return newTestUIWithOptions(t, &Options{RetrieverSource: "./retriever.yaml", Planner: p}, nil)
}

// TestPlanScreenDoesNotPlanOnPageLoad: a plan contacts every registry the
// Retriever names, so a page load must not be one. The form is offered;
// the run happens on submission.
func TestPlanScreenDoesNotPlanOnPageLoad(t *testing.T) {
	p := &stubPlanner{plan: samplePlan()}
	u := newPlanUI(t, p)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := get(t, mux, c, "/recipes/plan", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("plan screen = %d, want 200", w.Code)
	}
	if p.calls != 0 {
		t.Errorf("loading the screen ran %d plans, want 0", p.calls)
	}
	body := w.Body.String()
	if !strings.Contains(body, `action="/recipes/plan"`) {
		t.Error("the screen offers no way to run a plan")
	}
	if !strings.Contains(body, "./retriever.yaml") {
		t.Error("the screen does not show the configured Retriever (FR-010)")
	}
}

// TestPlanScreenRendersTheReport walks the sections FR-055 and R-04 ask
// for: the space verdict, the totals, the resolution, the statuses, the
// policy verdicts and the projected prune.
func TestPlanScreenRendersTheReport(t *testing.T) {
	p := &stubPlanner{plan: samplePlan()}
	u := newPlanUI(t, p)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := postForm(t, mux, c, "/recipes/plan", "csrf="+csrfOf(t, u, c)+"&retriever=&document=", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("plan submission = %d, want 200: %s", w.Code, w.Body.String())
	}
	if p.calls != 1 {
		t.Errorf("the submission ran %d plans, want 1", p.calls)
	}
	body := w.Body.String()
	for _, want := range []string{
		// FR-055: the target, its unidentified filesystem, the margin.
		"/var/lib/tobby", "not identified", "statfs(2) f_type",
		// FR-055: the volumes, readable, with the exact bytes in the
		// title attribute a machine can still read.
		"3 MiB", `title="3145728"`,
		// FR-021: requested → resolved.
		"^6.8", "6.8.2",
		// FR-026: the statuses, in the frozen glossary.
		"new", "up-to-date",
		// FR-036: the effective endpoint beside the nominal one.
		"mirror.example.com",
		// FR-030: the allow-list verdict, both ways.
		"registry.example.com", "rogue.example.net",
		// FR-045: the projected prune.
		"docker.io/bitnami/old",
		// FR-066: the exit code a pipeline would see.
		"5",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the plan screen does not render %q", want)
		}
	}
	// FR-033: an admitted-unsigned recipe never renders as plainly
	// verified — the declared scope is named.
	if !strings.Contains(body, "legacy-zone") {
		t.Error("the screen hides the trust scope that admitted an unsigned recipe")
	}
	// FR-055: the "cannot be identified" warning is a sentence, not a
	// code.
	if !strings.Contains(body, "could not be identified") {
		t.Error("the unidentified-filesystem warning is not rendered in words")
	}
}

// TestPlanScreenIsBilingual is FR-063 on this screen: every visible label
// exists in both catalogs, and the completeness test elsewhere would only
// catch a missing key — not one that renders as its own bracketed id.
func TestPlanScreenIsBilingual(t *testing.T) {
	p := &stubPlanner{plan: samplePlan()}
	u := newPlanUI(t, p)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	for lang, want := range map[string]string{
		"en": "Changes planned",
		"fr": "Des changements sont prévus",
	} {
		p.calls = 0
		w := postForm(t, mux, c, "/recipes/plan",
			"csrf="+csrfOf(t, u, c)+"&retriever=&document=",
			map[string]string{"Accept-Language": lang})
		if w.Code != http.StatusOK {
			t.Fatalf("[%s] plan = %d", lang, w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, want) {
			t.Errorf("[%s] the outcome is not rendered in the requested language", lang)
		}
		if strings.Contains(body, "[plan.") {
			t.Errorf("[%s] a plan label rendered as its own message id", lang)
		}
	}
}

// TestPlanScreenRendersARefusal: an FR-055 refusal renders as the same
// three-part taxonomy block every other error goes through (R-03), with
// the code and its /help anchor.
func TestPlanScreenRendersARefusal(t *testing.T) {
	plan := samplePlan()
	plan.Outcome = engine.OutcomeFailed
	plan.Checks[0].ProjectedBytes = 100 << 30
	plan.Checks[0].ShortfallBytes = 91 << 30
	plan.Checks[0].RefusalCode = taxonomy.CodeInsufficientSpace
	p := &stubPlanner{plan: plan}
	u := newPlanUI(t, p)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := postForm(t, mux, c, "/recipes/plan", "csrf="+csrfOf(t, u, c), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("plan = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, string(taxonomy.CodeInsufficientSpace)) {
		t.Error("the refusal does not carry its stable code")
	}
	if !strings.Contains(body, "/help#"+string(taxonomy.CodeInsufficientSpace)) {
		t.Error("the refusal does not link to its troubleshooting anchor")
	}
	// The shortfall, in bytes, is what an operator acts on.
	if !strings.Contains(body, "97710505984") {
		t.Error("the refusal does not state the shortfall in bytes")
	}
}

// TestPlanScreenWithoutAPlannerSaysSo: an instance wired without a recipe
// engine renders the form and refuses the run with an explanation, rather
// than showing an empty report.
func TestPlanScreenWithoutAPlannerSaysSo(t *testing.T) {
	u := newTestUIWithOptions(t, &Options{RetrieverSource: "./retriever.yaml"}, nil)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	if w := get(t, mux, c, "/recipes/plan", nil); w.Code != http.StatusOK {
		t.Fatalf("plan screen = %d, want 200 even without a planner", w.Code)
	}
	w := postForm(t, mux, c, "/recipes/plan", "csrf="+csrfOf(t, u, c), nil)
	if !strings.Contains(w.Body.String(), string(taxonomy.CodeConfigInvalid)) {
		t.Errorf("the refusal does not name the configuration problem: %s", w.Body.String())
	}
}
