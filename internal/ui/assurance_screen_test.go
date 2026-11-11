// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/assurance"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// The reduced-assurance marker on the screens (FR-037, R-22). Every screen
// that reports an item admitted under a relaxed scope shows the one marker
// — and, as importantly, shows it on THAT item only: a screen that marks
// everything marks nothing.

func markerCount(body string) int { return strings.Count(body, `data-assurance="reduced"`) }

// TestRecipesScreenMarksOnlyWhatARelaxedScopeAdmitted: the listing carries
// the marker for the recipe admitted unsigned under a declared scope and
// for no other, in both languages.
func TestRecipesScreenMarksOnlyWhatARelaxedScopeAdmitted(t *testing.T) {
	st := openTestStore(t)
	for _, r := range []store.RecipeRecord{
		{Name: "signed", Version: "1.0.0", CookbookRepo: "reg.example.com/cookbook/signed", Verified: true},
		{Name: "bare", Version: "1.0.0", CookbookRepo: "reg.example.com/cookbook/bare", Verified: false, TrustScope: "lab"},
	} {
		if err := st.PutRecipeRecord(&r); err != nil {
			t.Fatal(err)
		}
	}
	u := newTestUIWithOptions(t, &Options{Store: st, Queue: newTestQueue(t)}, nil)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	body := get(t, mux, c, "/recipes", nil).Body.String()
	if got := markerCount(body); got != 1 {
		t.Errorf("the listing carries %d markers, want exactly one (the unsigned recipe)", got)
	}
	if !strings.Contains(body, "Reduced assurance") || !strings.Contains(body, "trust scope lab") {
		t.Errorf("the marker does not say what it is and which scope admitted it:\n%s", body)
	}

	fr := get(t, mux, c, "/recipes", map[string]string{"Accept-Language": "fr"}).Body.String()
	if !strings.Contains(fr, "Assurance réduite") {
		t.Error("the marker is not translated: assurance.reduced is missing in French")
	}
}

// TestTaskDetailMarksTheItemsOfTheRelaxedRecipe: the per-item table of a
// task shows the marker on the items that carry one.
func TestTaskDetailMarksTheItemsOfTheRelaxedRecipe(t *testing.T) {
	u, q, _ := newTestUIWithQueue(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	task, err := q.Create(tasks.TypeSync, "reg.example.com/retriever.yaml", "alexis", []tasks.Item{
		{Name: "bare@1.0.0/app", Assurance: assurance.ForUnsigned("lab")},
		{Name: "bare@1.0.0/recipe", Assurance: assurance.ForUnsigned("lab")},
		{Name: "signed@1.0.0/app"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, c, "/tasks/"+task.ID, nil).Body.String()
	if got := markerCount(body); got != 2 {
		t.Errorf("the task carries %d markers, want 2 (one per item of the relaxed recipe)", got)
	}
}

// TestMediaScreenShowsTheOwnVerdictBesideTheSourceClaim: on the
// destination, this instance's marker and the source zone's claim are two
// different things on the screen, and a claim never stands in for the
// instance's own verdict.
func TestMediaScreenShowsTheOwnVerdictBesideTheSourceClaim(t *testing.T) {
	u, gate := mediaUI(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	gate.Observe(&media.Report{
		Verdict: media.VerdictPushable,
		Recipes: []media.RecipeVerdict{
			// Both: this instance relaxed too, the source said so as well.
			{Name: "wordpress", Version: "6.8.2", Digest: dgst(0xa1), Pushable: true, Unsigned: true, TrustScope: "zone-lab",
				Assurance: assurance.ForUnsigned("zone-lab"), SourceAssurance: assurance.ForUnsigned("source-lab")},
			// The source claimed nothing; this instance still relaxes.
			{Name: "redis", Version: "7.2.5", Digest: dgst(0xc3), Pushable: true, Unsigned: true, TrustScope: "zone-lab",
				Assurance: assurance.ForUnsigned("zone-lab")},
		},
	})
	body := get(t, mux, c, "/media", nil).Body.String()
	if got := markerCount(body); got != 2 {
		t.Errorf("own markers = %d, want 2: a manifest that claims nothing must never hide this instance's verdict", got)
	}
	if got := strings.Count(body, `data-assurance-claim="reduced"`); got != 1 {
		t.Errorf("source claims = %d, want 1 (only wordpress was claimed)", got)
	}
	if !strings.Contains(body, "Declared by the source zone: reduced assurance (source-lab)") {
		t.Error("the claim does not name the source's own scope")
	}
}
