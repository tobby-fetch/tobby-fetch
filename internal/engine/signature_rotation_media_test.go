// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"testing"
	"time"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// TestKeyRotationCrossesTheAirGap is B-081 on the product's core path: an
// isolated zone imported a delivery signed with key A; upstream re-signs
// the SAME digests with key B and the isolated zone's trust rotates to B
// alone. The medium is re-produced by a mirror synchronization onto the
// same store (a medium is a mirrored store), carried over, verified and
// imported. The recipe gains B in both cosign layouts, the ingredient in
// the bundle layout; trust.requireIngredientSignatures makes the
// ingredient's signature load-bearing.
func TestKeyRotationCrossesTheAirGap(t *testing.T) {
	ctx := context.Background()
	kpA, kpB := newKeyPair(t), newKeyPair(t)

	// Upstream: one recipe, one ingredient, both signed with A.
	src := newRegistry(t)
	const imgRepo, cookbookRepo = "docker.io/library/alpine", "docker.io/cookbook/alpine"
	imgDig := seedImage(t, src, imgRepo, "3.22.1")
	signManifest(t, src.st, imgRepo, imgDig, kpA)
	yaml := cookedRecipeYAML(t, "alpine", "3.22.1", []spec.Ingredient{{
		Name: "app", Kind: spec.IngredientContainerImage, Ref: imgRepo, Version: "3.22.1", Digest: imgDig,
	}})
	manDig := publishRecipe(t, src.st, cookbookRepo, "3.22.1", yaml)
	signManifest(t, src.st, cookbookRepo, manDig, kpA)

	// The mirror instance writing the medium. Its trust follows upstream.
	onMedium := openStore(t)
	retr := retrieverFile(t, servedZone, "docker.io/cookbook", []spec.RecipeSelector{{Name: "alpine", Version: "3.22.1"}})
	producer := New(onMedium, newRemotes(t, map[string]string{"docker.io": src.addr}), trustFor(t, nil, kpA), retr, "", syncCfg())
	producer.SetMediaManifest(func(ctx context.Context, z, runID string, at time.Time) error {
		_, err := media.Write(ctx, onMedium, media.WriteOptions{Zone: z, RunID: runID, ResolvedAt: at})
		return err
	})
	produce := func(step string) {
		t.Helper()
		task, err := runSync(t, producer)
		if err != nil {
			t.Fatalf("%s: producing the medium: %v", step, err)
		}
		if agg := task.Aggregate(); agg.Failed != 0 {
			t.Fatalf("%s: producing the medium: %+v (items %s)", step, agg, itemNames(task))
		}
	}

	// The isolated zone: its own register of imports, its own registry.
	imports, err := media.OpenImports(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest := newDestRegistry(t)
	isolated := func(trust *TrustPolicy) *Engine {
		eng := New(onMedium, newRemotes(t, nil), trust, "", "", syncCfg())
		eng.SetMediaImport(servedZone, imports)
		withDestination(t, eng, dest, config.Destination{}, nil)
		return eng
	}
	importAll := func(step string, eng *Engine) {
		t.Helper()
		task, err := runImport(t, eng, MediaOptions{})
		if err != nil {
			t.Fatalf("%s: import: %v", step, err)
		}
		if it := itemByName(t, task, pushItemPrefix+"alpine@3.22.1/recipe"); it.Status == tasks.StatusFailed {
			t.Fatalf("%s: recipe push failed: %+v", step, it.Error)
		}
	}

	// 1. Under A: produce, import.
	produce("under A")
	importAll("under A", isolated(trustFor(t, nil, kpA)))

	// 2. Rotation upstream: the same digests re-signed with B — the
	// recipe in both layouts, the ingredient in the bundle layout.
	signManifest(t, src.st, cookbookRepo, manDig, kpB)
	signManifestBundle(t, src.st, cookbookRepo, manDig, kpB)
	signManifestBundle(t, src.st, imgRepo, imgDig, kpB)
	producer.trust = trustFor(t, nil, kpB)
	produce("after rotation")

	// 3. The isolated zone now trusts B alone: verify, then import.
	eng := isolated(requiringTrust(t, nil, kpB))
	rep, err := eng.VerifyMedia(ctx, discardLogger(), MediaOptions{})
	if err != nil {
		t.Fatalf("media verify: %v", err)
	}
	if rep.Verdict != media.VerdictPushable {
		t.Fatalf("media verify under B = %s (%+v), want pushable", rep.Verdict, rep.Recipes)
	}
	importAll("after rotation", eng)

	keysB := trustFor(t, nil, kpB).Decide("").Keys
	for _, c := range []struct{ repo, dgst string }{
		{cookbookRepoOf("alpine"), manDig},
		{imgRepo, imgDig},
	} {
		if _, err := sigverify.Verify(ctx, &storeManifests{src: dest.st, repo: c.repo}, c.repo, c.dgst, keysB); err != nil {
			t.Errorf("destination: %s@%s does not verify with B alone: %v", c.repo, c.dgst, err)
		}
	}
	// Both layouts of the recipe's B signature crossed.
	tags := destTags(t, dest, cookbookRepoOf("alpine"))
	for _, want := range []string{SignatureTag(manDig), referrersFallbackTag(manDig)} {
		if !containsString(tags, want) {
			t.Errorf("destination cookbook tags %v lack %s", tags, want)
		}
	}

	// 4. The same medium again: nothing to write.
	dest.reset()
	importAll("re-import", eng)
	if writes := dest.writes(); len(writes) != 0 {
		t.Errorf("re-importing an already delivered medium wrote: %v", writes)
	}
}
