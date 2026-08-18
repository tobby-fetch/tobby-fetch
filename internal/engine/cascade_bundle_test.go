// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"net/http/httptest"
	"testing"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// TestCascadeCarriesBundleSignatures locks the half of §12.2 that was
// missing: a signature travels with its content whatever LAYOUT it
// arrives in.
//
// The verifier was taught to read both cosign layouts — the classic
// attached "sha256-<hex>.sig" tag and the bundle artifact that REFERS to
// its subject, which cosign 3.x emits by default. The copy was only ever
// taught the first. The result was a signature that verified on the zone
// that fetched it and was simply absent one hop down: the zone below
// refused content its upstream had accepted, with "no signature artifact
// found" and nothing an operator could act on.
//
// The shape of the test is the shape of the failure: hop A signs in the
// bundle layout, hop B synchronizes, and hop C — chained on B, reading
// only what B stored — must reach the same verdict as B did.
func TestCascadeCarriesBundleSignatures(t *testing.T) {
	ctx := context.Background()
	kp := newKeyPair(t)

	a := newRegistry(t)
	imgDig := seedImage(t, a, "docker.io/library/nginx", "1.25.0")
	yaml := cookedRecipeYAML(t, "nginx", "1.25.0", []spec.Ingredient{{
		Name: "app", Kind: spec.IngredientContainerImage,
		Ref: "docker.io/library/nginx", Version: "1.25.0", Digest: imgDig,
	}})
	manDig := publishRecipe(t, a.st, "docker.io/cookbook/nginx", "1.25.0", yaml)
	// No ".sig" tag anywhere: the bundle layout is the ONLY signature.
	sigDig := signManifestBundle(t, a.st, "docker.io/cookbook/nginx", manDig, kp)

	retr := retrieverFile(t, "zone-b", "docker.io/cookbook", []spec.RecipeSelector{
		{Name: "nginx", Version: "1.25.0"},
	})

	dstB := openStore(t)
	engB := New(dstB, newRemotes(t, map[string]string{"docker.io": a.addr}), trustFor(t, nil, kp), retr, "", syncCfg())
	taskB := &tasks.Task{ID: "tsk_b", RunID: "run_b", Type: tasks.TypeSync, Status: tasks.StatusRunning}
	if err := runSyncTask(t, engB, taskB, discardLogger()); err != nil {
		t.Fatalf("hop B run: %v", err)
	}
	if agg := taskB.Aggregate(); agg.Failed != 0 {
		t.Fatalf("hop B aggregates = %+v (items %s)", agg, itemNames(taskB))
	}

	// What B stored is what C will have to work from. Both the referring
	// artifact and the index that makes it findable must be there: the
	// embedded registry serves no Referrers API, so without the fallback
	// tag the artifact exists and nobody can reach it.
	if _, _, _, err := dstB.RawManifest(ctx, "docker.io/cookbook/nginx", sigDig); err != nil {
		t.Errorf("hop B did not store the bundle signature artifact: %v", err)
	}
	fallback := "sha256-" + manDig[len("sha256:"):]
	tagsB, err := dstB.Tags(ctx, "docker.io/cookbook/nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(tagsB, fallback) {
		t.Errorf("hop B tags = %v, want the referrers fallback tag %s — without it the signature is unreachable",
			tagsB, fallback)
	}

	// Hop C: chained on B, and the verdict must match B's.
	srvB := httptest.NewServer(dstB.APIHandler())
	t.Cleanup(srvB.Close)
	dstC := openStore(t)
	engC := New(dstC, newRemotes(t, map[string]string{"docker.io": srvB.Listener.Addr().String()}), trustFor(t, nil, kp), retr, "", syncCfg())
	taskC := &tasks.Task{ID: "tsk_c", RunID: "run_c", Type: tasks.TypeSync, Status: tasks.StatusRunning}
	if err := runSyncTask(t, engC, taskC, discardLogger()); err != nil {
		t.Fatalf("hop C run: %v", err)
	}
	if agg := taskC.Aggregate(); agg.Failed != 0 {
		t.Fatalf("hop C aggregates = %+v (items %s): the signature did not survive the hop",
			agg, itemNames(taskC))
	}
	recsC, err := dstC.RecipeRecords()
	if err != nil || len(recsC) != 1 || !recsC[0].Verified {
		t.Fatalf("hop C records = %+v (err %v), want one VERIFIED record", recsC, err)
	}
}
