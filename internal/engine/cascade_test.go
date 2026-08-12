// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// TestSyncSubstitutionCascade locks FR-036 + FR-035 across a full
// cascade: nominal docker.io references, seeded in an upstream store
// under their nominal relocated path, are pulled by a downstream engine
// through a source substitution WITHOUT touching the recipe — and the
// relocated paths are IDENTICAL at every hop, so a third zone chained on
// the second resolves the exact same repositories (§11.5 invariance).
func TestSyncSubstitutionCascade(t *testing.T) {
	ctx := context.Background()
	kp := newKeyPair(t)

	// Hop A (upstream): nominal docker.io content and a signed recipe,
	// stored under the canonical relocated paths — exactly where an
	// upstream instance holds them.
	a := newRegistry(t)
	imgDig := seedImage(t, a, "docker.io/library/nginx", "1.25.0")
	yaml := cookedRecipeYAML(t, "nginx", "1.25.0", []spec.Ingredient{{
		Name: "app", Kind: spec.IngredientContainerImage,
		Ref: "docker.io/library/nginx", Version: "1.25.0", Digest: imgDig,
	}})
	manDig := publishRecipe(t, a.st, "docker.io/cookbook/nginx", "1.25.0", yaml)
	signManifest(t, a.st, "docker.io/cookbook/nginx", manDig, kp)

	// The retriever speaks ONLY nominal references; the substitution is
	// local configuration (FR-036: the recipe is never modified).
	retr := retrieverFile(t, "zone-b", "docker.io/cookbook", []spec.RecipeSelector{
		{Name: "nginx", Version: "1.25.0"},
	})

	// Hop B: engine substituting docker.io → the upstream endpoint.
	dstB := openStore(t)
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	engB := New(dstB, newRemotes(t, map[string]string{"docker.io": a.addr}), trustFor(t, nil, kp), retr, "", syncCfg())
	taskB := &tasks.Task{ID: "tsk_b", RunID: "run_b", Type: tasks.TypeSync, Status: tasks.StatusRunning}
	if err := runSyncTask(t, engB, taskB, logger); err != nil {
		t.Fatalf("hop B run: %v", err)
	}
	if agg := taskB.Aggregate(); agg.Failed != 0 {
		t.Fatalf("hop B aggregates = %+v (items %s)", agg, itemNames(taskB))
	}

	// The relocated paths in B are the nominal canonical paths — not the
	// substituted endpoint's.
	if d, ok := dstB.ResolveTag(ctx, "docker.io/library/nginx", "1.25.0"); !ok || d != imgDig {
		t.Errorf("hop B nginx tag = %q (ok=%v), want %s under the NOMINAL path", d, ok, imgDig)
	}
	sigTag := SignatureTag(manDig)
	tagsB, err := dstB.Tags(ctx, "docker.io/cookbook/nginx")
	if err != nil || !containsString(tagsB, "1.25.0") || !containsString(tagsB, sigTag) {
		t.Errorf("hop B recipe tags = %v (err %v), want 1.25.0 and %s", tagsB, err, sigTag)
	}

	// FR-036 reporting: the resolution row carries the effective endpoint,
	// and the logs carry BOTH the nominal and the effective reference.
	row := resolutionFor(t, taskB, "nginx@1.25.0", "app")
	wantEffective := a.addr + "/docker.io/library/nginx"
	if row.Effective != wantEffective {
		t.Errorf("resolution Effective = %q, want %q", row.Effective, wantEffective)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "docker.io/library/nginx") || !strings.Contains(logs, wantEffective) {
		t.Errorf("logs lack the nominal and effective references (FR-036):\n%s", logs)
	}

	recsB, err := dstB.RecipeRecords()
	if err != nil || len(recsB) != 1 || !recsB[0].Verified {
		t.Fatalf("hop B records = %+v (err %v), want one verified record", recsB, err)
	}

	// Hop C: chained on B — same retriever, substitution now pointing at
	// B's own /v2/ surface. The signature copied into B must verify again
	// (§12.3 point 3: signatures travel with the content).
	srvB := httptest.NewServer(dstB.APIHandler())
	t.Cleanup(srvB.Close)
	dstC := openStore(t)
	engC := New(dstC, newRemotes(t, map[string]string{"docker.io": srvB.Listener.Addr().String()}), trustFor(t, nil, kp), retr, "", syncCfg())
	taskC, err := runSync(t, engC)
	if err != nil {
		t.Fatalf("hop C run: %v", err)
	}
	if agg := taskC.Aggregate(); agg.Failed != 0 {
		t.Fatalf("hop C aggregates = %+v (items %s)", agg, itemNames(taskC))
	}

	// Path invariance across the cascade: C holds the very same
	// repositories, tags and digests as B (and as A's nominal layout).
	if d, ok := dstC.ResolveTag(ctx, "docker.io/library/nginx", "1.25.0"); !ok || d != imgDig {
		t.Errorf("hop C nginx tag = %q (ok=%v), want %s — paths must not drift across hops", d, ok, imgDig)
	}
	if d, ok := dstC.ResolveTag(ctx, "docker.io/cookbook/nginx", "1.25.0"); !ok || d != manDig {
		t.Errorf("hop C recipe tag = %q (ok=%v), want %s", d, ok, manDig)
	}
	recsC, err := dstC.RecipeRecords()
	if err != nil || len(recsC) != 1 {
		t.Fatalf("hop C records = %+v (err %v)", recsC, err)
	}
	if !recsC[0].Verified {
		t.Errorf("hop C record not Verified: the signature did not survive the cascade (§12.2)")
	}
	if recsC[0].CookbookRepo != recsB[0].CookbookRepo {
		t.Errorf("cookbook repo drifted across hops: B %q, C %q", recsB[0].CookbookRepo, recsC[0].CookbookRepo)
	}
	p, ok := dstC.ProvenanceOf("docker.io/library/nginx")
	if !ok || p.Class != store.ProvenanceRecipe {
		t.Errorf("hop C provenance = %+v (ok=%v), want class recipe", p, ok)
	}
}
