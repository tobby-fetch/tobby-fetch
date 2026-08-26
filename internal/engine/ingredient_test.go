// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestSyncIngredientFailures locks the per-ingredient verification gates
// with item isolation: an OCIArtifact served with the wrong artifactType
// fails TBY-SIG-003 (§7.3), a chart without its embedded dependency fails
// TBY-CHT-001 (FR-024) with the report row filled — while the healthy
// sibling ingredient of the SAME recipe still lands.
func TestSyncIngredientFailures(t *testing.T) {
	src := newRegistry(t)
	dst := openStore(t)
	kp := newKeyPair(t)

	imgDig := seedImage(t, src, "library/app", "1.0.0")
	badChartDig := seedChart(t, src, "charts/wordpress", "24.2.6", false) // dependency NOT embedded
	modelDig := seedArtifact(t, src, "models/embedding", "1.0.0", "application/vnd.wrong.type+json")

	yaml := cookedRecipeYAML(t, "broken", "1.0.0", []spec.Ingredient{
		{
			Name: "app", Kind: spec.IngredientContainerImage,
			Ref: src.addr + "/library/app", Version: "1.0.0", Digest: imgDig,
		},
		{
			Name: "chart", Kind: spec.IngredientHelmChart,
			Ref: src.addr + "/charts/wordpress", Version: "24.2.6", Digest: badChartDig,
		},
		{
			Name: "model", Kind: spec.IngredientOCIArtifact,
			Ref: src.addr + "/models/embedding", Version: "1.0.0", Digest: modelDig,
			ArtifactType: "application/vnd.example.model.v1+json",
		},
	})
	manDig := publishRecipe(t, src.st, "cookbook/broken", "1.0.0", yaml)
	signManifest(t, src.st, "cookbook/broken", manDig, kp)

	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{
		{Name: "broken", Version: "1.0.0"},
	})
	eng := New(dst, newRemotes(t, nil), trustFor(t, nil, kp), retr, "", syncCfg())
	task, err := runSync(t, eng)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	const rid = "broken@1.0.0"

	// §7.3: artifactType mismatch is a verification failure naming both
	// types.
	model := itemByName(t, task, rid+"/model")
	if model.Status != tasks.StatusFailed || model.Error == nil || model.Error.Code != taxonomy.CodeArtifactType {
		t.Fatalf("model item = %+v, want failed TBY-SIG-003", model)
	}
	if model.Error.Params["expected"] != "application/vnd.example.model.v1+json" ||
		model.Error.Params["actual"] != "application/vnd.wrong.type+json" {
		t.Errorf("artifactType params = %+v, want expected/actual media types", model.Error.Params)
	}

	// FR-024: the missing embedded dependency blocks the chart, and the
	// report names chart and dependency.
	chart := itemByName(t, task, rid+"/chart")
	if chart.Status != tasks.StatusFailed || chart.Error == nil || chart.Error.Code != taxonomy.CodeChartDependency {
		t.Fatalf("chart item = %+v, want failed TBY-CHT-001", chart)
	}
	var found bool
	for _, row := range task.ChartDependencies {
		if row.Chart == "wordpress" && row.Name == "mariadb" && !row.Embedded {
			found = true
		}
	}
	if !found {
		t.Errorf("chart dependency report = %+v, want a non-embedded mariadb row for chart wordpress", task.ChartDependencies)
	}

	// Isolation: the healthy ingredient and the recipe artifact landed.
	for _, name := range []string{rid + "/app", rid + "/recipe"} {
		if it := itemByName(t, task, name); it.Status != tasks.StatusDone {
			t.Errorf("item %s = %s (error: %+v), want done despite sibling failures", name, it.Status, it.Error)
		}
	}

	// The recipe graph only records what actually landed.
	recs, err := dst.RecipeRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || len(recs[0].Ingredients) != 1 || recs[0].Ingredients[0].Name != "app" {
		t.Errorf("recipe records = %+v, want only the app ingredient recorded", recs)
	}
}

// TestSyncNestedIndexFullCopy locks the recursive index copy: an index
// containing a nested index (no platform selection) is transferred whole,
// nested children included, under the pinned top-level digest.
func TestSyncNestedIndexFullCopy(t *testing.T) {
	src := newRegistry(t)
	dst := openStore(t)
	kp := newKeyPair(t)
	ctx := context.Background()

	inner, err := random.Index(256, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	outer := mutate.AppendManifests(v1.ImageIndex(empty.Index),
		mutate.IndexAddendum{Add: inner},
		mutate.IndexAddendum{
			Add:        leaf,
			Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}},
		})
	outer = mutate.IndexMediaType(outer, types.OCIImageIndex)
	ref, err := name.ParseReference(src.addr + "/nested/bundle:2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, outer); err != nil {
		t.Fatal(err)
	}
	outerDig, err := outer.Digest()
	if err != nil {
		t.Fatal(err)
	}
	innerDig, err := inner.Digest()
	if err != nil {
		t.Fatal(err)
	}

	yaml := cookedRecipeYAML(t, "bundle", "2.0.0", []spec.Ingredient{{
		Name: "all", Kind: spec.IngredientContainerImage,
		Ref: src.addr + "/nested/bundle", Version: "2.0.0", Digest: outerDig.String(),
	}})
	manDig := publishRecipe(t, src.st, "cookbook/bundle", "2.0.0", yaml)
	signManifest(t, src.st, "cookbook/bundle", manDig, kp)

	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{
		{Name: "bundle", Version: "2.0.0"},
	})
	task, err := runSync(t, New(dst, newRemotes(t, nil), trustFor(t, nil, kp), retr, "", syncCfg()))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if agg := task.Aggregate(); agg.Failed != 0 {
		t.Fatalf("aggregates = %+v (items %s)", agg, itemNames(task))
	}

	repo := strings.ReplaceAll(src.addr, ":", "_") + "/nested/bundle"
	if d, ok := dst.ResolveTag(ctx, repo, "2.0.0"); !ok || d != outerDig.String() {
		t.Errorf("tag resolves %q (ok=%v), want the pinned outer index %s", d, ok, outerDig)
	}
	if !dst.HasManifest(ctx, repo, innerDig.String()) {
		t.Errorf("nested index %s missing from the destination", innerDig)
	}
	innerMan, err := inner.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range innerMan.Manifests {
		if !dst.HasManifest(ctx, repo, c.Digest.String()) {
			t.Errorf("nested child %s missing from the destination", c.Digest)
		}
	}
}

// TestSyncMissingPlatform: a platform requested by the recipe but absent
// from the source index fails the ingredient — never a silent partial.
func TestSyncMissingPlatform(t *testing.T) {
	src := newRegistry(t)
	dst := openStore(t)
	kp := newKeyPair(t)

	idx := seedIndex(t, src, "library/app", "1.0.0", v1.Platform{OS: "linux", Architecture: "amd64"})
	yaml := cookedRecipeYAML(t, "partial", "1.0.0", []spec.Ingredient{{
		Name: "app", Kind: spec.IngredientContainerImage,
		Ref: src.addr + "/library/app", Version: "1.0.0", Digest: idx.digest,
		Platforms: []string{"linux/amd64", "linux/riscv64"},
	}})
	manDig := publishRecipe(t, src.st, "cookbook/partial", "1.0.0", yaml)
	signManifest(t, src.st, "cookbook/partial", manDig, kp)

	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{
		{Name: "partial", Version: "1.0.0"},
	})
	task, err := runSync(t, New(dst, newRemotes(t, nil), trustFor(t, nil, kp), retr, "", syncCfg()))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	it := itemByName(t, task, "partial@1.0.0/app")
	if it.Status != tasks.StatusFailed || it.Error == nil {
		t.Errorf("item = %+v, want failed on the absent platform (FR-022)", it)
	}
}

// TestSyncSelectsPlatformWhoseVariantTheRecipeOmits is B-020, reproduced
// on the shape real registries actually publish.
//
// RECIPE-SPEC §7.1 writes the field as "os/arch[/variant]" — the variant
// is optional — and its own normative example asks for
// platforms: ["linux/amd64", "linux/arm64"]. Docker's official images
// describe their arm64 child as linux/arm64 WITH variant v8, so an index
// seeded the way the world publishes them (rather than the way the old
// fixtures did, variant-free) is what the recipe meets in production.
//
// The whole ingredient failed, so the assertion is on both platforms
// landing under the pinned index digest — the FR-022 sparse index — not
// merely on the item's status.
func TestSyncSelectsPlatformWhoseVariantTheRecipeOmits(t *testing.T) {
	src := newRegistry(t)
	dst := openStore(t)
	kp := newKeyPair(t)
	ctx := context.Background()

	idx := seedIndex(t, src, "library/alpine", "3.22.1",
		v1.Platform{OS: "linux", Architecture: "amd64"},
		v1.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"},
		v1.Platform{OS: "linux", Architecture: "arm", Variant: "v7"},
	)
	yaml := cookedRecipeYAML(t, "base", "1.0.0", []spec.Ingredient{{
		Name: "alpine", Kind: spec.IngredientContainerImage,
		Ref: src.addr + "/library/alpine", Version: "3.22.1", Digest: idx.digest,
		Platforms: []string{"linux/amd64", "linux/arm64"},
	}})
	manDig := publishRecipe(t, src.st, "cookbook/base", "1.0.0", yaml)
	signManifest(t, src.st, "cookbook/base", manDig, kp)

	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{
		{Name: "base", Version: "1.0.0"},
	})
	task, err := runSync(t, New(dst, newRemotes(t, nil), trustFor(t, nil, kp), retr, "", syncCfg()))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	it := itemByName(t, task, "base@1.0.0/alpine")
	if it.Status != tasks.StatusDone {
		t.Fatalf("item = %+v (detail %q), want done: an omitted variant selects the platform",
			it.Status, errDetail(it))
	}

	repo := strings.ReplaceAll(src.addr, ":", "_") + "/library/alpine"
	if d, ok := dst.ResolveTag(ctx, repo, "3.22.1"); !ok || d != idx.digest {
		t.Errorf("tag resolves %q (ok=%v), want the pinned index %s", d, ok, idx.digest)
	}
	for _, label := range []string{"linux/amd64", "linux/arm64/v8"} {
		if !dst.HasManifest(ctx, repo, idx.children[label]) {
			t.Errorf("selected platform %s is missing from the store", label)
		}
	}
	// The selection is still a selection: arm/v7 was not asked for, so the
	// index stays sparse (FR-022).
	if dst.HasManifest(ctx, repo, idx.children["linux/arm/v7"]) {
		t.Error("linux/arm/v7 was transferred although the recipe never asked for it")
	}
}

// errDetail renders an item's recorded cause for failure messages (B-021).
func errDetail(it *tasks.Item) string {
	if it.Error == nil {
		return ""
	}
	return string(it.Error.Code) + ": " + it.Error.Detail
}

// TestFailedIngredientIsLoggedWithItsCause is B-021: an ingredient that
// fails must leave a trail an operator can follow.
//
// The failure fixture is deliberately the most opaque one the engine
// produces — an absent platform, which carries no taxonomy entry of its
// own and settles as TBY-SRV-001, whose corrective action is literally
// "follow the correlation identifier in the logs" (FR-090). Before the
// fix that instruction led nowhere: the item recorded the code and
// nothing else, and the ingredient goroutine emitted no record at all,
// at any level. The test therefore asserts both halves of the trail —
// the log line with the FR-090 correlation fields, and the cause carried
// on the item so the task detail and /api/v1/tasks/{id} can show it.
func TestFailedIngredientIsLoggedWithItsCause(t *testing.T) {
	src := newRegistry(t)
	dst := openStore(t)
	kp := newKeyPair(t)

	idx := seedIndex(t, src, "library/app", "1.0.0", v1.Platform{OS: "linux", Architecture: "amd64"})
	yaml := cookedRecipeYAML(t, "opaque", "1.0.0", []spec.Ingredient{{
		Name: "app", Kind: spec.IngredientContainerImage,
		Ref: src.addr + "/library/app", Version: "1.0.0", Digest: idx.digest,
		Platforms: []string{"linux/amd64", "linux/riscv64"},
	}})
	manDig := publishRecipe(t, src.st, "cookbook/opaque", "1.0.0", yaml)
	signManifest(t, src.st, "cookbook/opaque", manDig, kp)

	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{
		{Name: "opaque", Version: "1.0.0"},
	})
	var logBuf bytes.Buffer
	eng := New(dst, newRemotes(t, nil), trustFor(t, nil, kp), retr, "", syncCfg())
	task := &tasks.Task{ID: "tsk_b021", RunID: "run_b021", Type: tasks.TypeSync, Status: tasks.StatusRunning}
	if err := runSyncTask(t, eng, task, slog.New(slog.NewTextHandler(&logBuf, nil))); err != nil {
		t.Fatalf("run: %v", err)
	}

	it := itemByName(t, task, "opaque@1.0.0/app")
	if it.Status != tasks.StatusFailed || it.Error == nil {
		t.Fatalf("item = %+v, want failed", it)
	}
	if it.Error.Code != taxonomy.CodeInternal {
		t.Fatalf("item code = %s, want the opaque %s this test is about", it.Error.Code, taxonomy.CodeInternal)
	}
	// The cause travels with the item, as a field of its own: the code
	// stays the code (the taxonomy is untouched), the technical detail
	// rides beside it so every surface can show it (FR-061).
	if !strings.Contains(it.Error.Detail, "linux/riscv64") {
		t.Errorf("item error detail = %q, want the absent platform named", it.Error.Detail)
	}

	// The log line, with the correlation fields the corrective action of
	// TBY-SRV-001 sends the operator looking for (FR-090).
	logs := logBuf.String()
	for _, want := range []string{
		"ingredient synchronization failed",
		"recipe=opaque", "ingredient=app",
		"code=" + string(taxonomy.CodeInternal), "linux/riscv64",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q (B-021, FR-090):\n%s", want, logs)
		}
	}
}

// TestSyncRejectsLayoutViolation locks the §11.2 consumer obligation: a
// cookbook artifact that does not follow the recipe layout is rejected as
// a validation failure (TBY-VAL-001) before its document is read.
func TestSyncRejectsLayoutViolation(t *testing.T) {
	src, imgDig := seedTrustCookbook(t)
	dst := openStore(t)
	yaml := trustRecipeYAML(t, src, "layout", imgDig)
	// Published with a foreign artifactType: consumers MUST reject.
	publishRecipeLayout(t, src.st, "cookbook/layout", "1.0.0", yaml,
		"application/vnd.example.other.v1+yaml", mediaTypeEmptyConfig, MediaTypeRecipe)

	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{
		{Name: "layout", Version: "1.0.0"},
	})
	eng := New(dst, newRemotes(t, nil), trustFor(t, nil, newKeyPair(t)), retr, "", syncCfg())
	task, err := runSync(t, eng)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	it := itemByName(t, task, "layout@1.0.0")
	if it.Status != tasks.StatusFailed || it.Error == nil || it.Error.Code != taxonomy.CodeValidation {
		t.Errorf("layout item = %+v, want failed TBY-VAL-001 (§11.2)", it)
	}
}
