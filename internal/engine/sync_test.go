// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// happyEnv is the full happy-path fixture: one source registry holding
// three ingredient kinds and a signed cooked recipe, one destination
// store, one engine trusting the signing key.
type happyEnv struct {
	src      *registry
	dst      *store.Store
	eng      *Engine
	hostRepo string // relocated host prefix ("127.0.0.1_<port>")
	idx      seededIndex
	chartDig string
	modelDig string
	manDig   string // recipe artifact manifest digest
	trust    *TrustPolicy
}

const testZone = "zone-lab"

// newHappyEnv seeds the complete scenario: multi-arch image (selective
// platforms), chart with embedded dependency, custom OCI artifact, cooked
// recipe published per §11.2 and signed per §12.2.
func newHappyEnv(t *testing.T) *happyEnv {
	t.Helper()
	src := newRegistry(t)
	dst := openStore(t)

	idx := seedIndex(t, src, "ingredients/wordpress", "6.8.2",
		v1.Platform{OS: "linux", Architecture: "amd64"},
		v1.Platform{OS: "linux", Architecture: "arm64"},
		v1.Platform{OS: "linux", Architecture: "arm", Variant: "v7"},
	)
	chartDig := seedChart(t, src, "charts/wordpress", "24.2.6", true)
	modelDig := seedArtifact(t, src, "models/embedding", "1.0.0", "application/vnd.example.model.v1+json")

	recipeYAML := cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{
		{
			Name: "app", Kind: spec.IngredientContainerImage,
			Ref: src.addr + "/ingredients/wordpress", Version: "6.8.2", Digest: idx.digest,
			Platforms: []string{"linux/amd64", "linux/arm64"}, // arm/v7 deliberately left out (FR-022)
		},
		{
			Name: "chart", Kind: spec.IngredientHelmChart,
			Ref: src.addr + "/charts/wordpress", Version: "24.2.6", Digest: chartDig,
		},
		{
			Name: "model", Kind: spec.IngredientOCIArtifact,
			Ref: src.addr + "/models/embedding", Version: "1.0.0", Digest: modelDig,
			ArtifactType: "application/vnd.example.model.v1+json",
		},
	})
	manDig := publishRecipe(t, src.st, "cookbook/wordpress", "6.8.2", recipeYAML)
	kp := newKeyPair(t)
	signManifest(t, src.st, "cookbook/wordpress", manDig, kp)

	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{
		{Name: "wordpress", Version: "6.8.2"},
	})
	trust := trustFor(t, nil, kp)
	eng := New(dst, newRemotes(t, nil), trust, retr, "", syncCfg())

	return &happyEnv{
		src: src, dst: dst, eng: eng, trust: trust,
		hostRepo: strings.ReplaceAll(src.addr, ":", "_"),
		idx:      idx, chartDig: chartDig, modelDig: modelDig, manDig: manDig,
	}
}

// TestSyncHappyPath locks the full milestone-3 loop (roadmap 3.1→3.4):
// Retriever → resolution (FR-021) → verification (FR-033) → bit-exact
// sparse transfer under relocation (FR-020/FR-022/FR-035) → recipe
// artifact + signature stored → provenance and recipe graph recorded.
func TestSyncHappyPath(t *testing.T) {
	env := newHappyEnv(t)
	ctx := context.Background()

	// Observability hooks (NFR-008/FR-091): counted, never load-bearing.
	var started, ended, moved atomic.Int64
	env.eng.SetMeters(Meters{
		TransferStarted: func() { started.Add(1) },
		TransferDone:    func() { ended.Add(1) },
		BytesMoved:      func(n int64) { moved.Add(n) },
	})

	task, err := runSync(t, env.eng)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if started.Load() != 3 || ended.Load() != 3 || moved.Load() == 0 {
		t.Errorf("meters: started=%d done=%d bytes=%d, want 3 balanced transfers moving bytes",
			started.Load(), ended.Load(), moved.Load())
	}

	const rid = "wordpress@6.8.2"
	for _, name := range []string{rid + "/app", rid + "/chart", rid + "/model", rid + "/recipe"} {
		it := itemByName(t, task, name)
		if it.Status != tasks.StatusDone {
			t.Errorf("item %s = %s (error: %+v), want done", name, it.Status, it.Error)
		}
	}
	if agg := task.Aggregate(); agg.Failed != 0 || agg.Total != 4 {
		t.Errorf("aggregates = %+v, want 4 items, 0 failed", agg)
	}
	for _, name := range []string{rid + "/app", rid + "/chart", rid + "/model"} {
		if it := itemByName(t, task, name); it.SizeBytes == 0 {
			t.Errorf("item %s transferred 0 bytes on a fresh destination", name)
		}
	}

	// Relocated destination paths (FR-035): nominal canonical host prefix,
	// ":" folded to "_".
	appRepo := env.hostRepo + "/ingredients/wordpress"
	chartRepo := env.hostRepo + "/charts/wordpress"
	modelRepo := env.hostRepo + "/models/embedding"
	recipeRepo := env.hostRepo + "/cookbook/wordpress"

	// FR-022: the ORIGINAL index bytes are tagged (pinned digest kept)…
	if d, ok := env.dst.ResolveTag(ctx, appRepo, "6.8.2"); !ok || d != env.idx.digest {
		t.Errorf("app tag resolves %q (ok=%v), want the pinned index digest %s", d, ok, env.idx.digest)
	}
	// …selected children present, the unselected child absent (sparse).
	for _, p := range []string{"linux/amd64", "linux/arm64"} {
		if !env.dst.HasManifest(ctx, appRepo, env.idx.children[p]) {
			t.Errorf("selected platform %s missing from the destination", p)
		}
	}
	if env.dst.HasManifest(ctx, appRepo, env.idx.children["linux/arm/v7"]) {
		t.Errorf("unselected platform linux/arm/v7 was transferred (want sparse, FR-022)")
	}

	if d, ok := env.dst.ResolveTag(ctx, chartRepo, "24.2.6"); !ok || d != env.chartDig {
		t.Errorf("chart tag resolves %q (ok=%v), want %s", d, ok, env.chartDig)
	}
	if d, ok := env.dst.ResolveTag(ctx, modelRepo, "1.0.0"); !ok || d != env.modelDig {
		t.Errorf("model tag resolves %q (ok=%v), want %s", d, ok, env.modelDig)
	}

	// FR-024 report: the embedded dependency is visible, chart named.
	if len(task.ChartDependencies) != 1 || task.ChartDependencies[0].Chart != "wordpress" ||
		task.ChartDependencies[0].Name != "mariadb" || !task.ChartDependencies[0].Embedded {
		t.Errorf("chart dependency report = %+v", task.ChartDependencies)
	}

	// The recipe artifact and its signature travel into the local store
	// (cascade groundwork): version tag + cosign ".sig" tag.
	tags, err := env.dst.Tags(ctx, recipeRepo)
	if err != nil {
		t.Fatalf("recipe repo tags: %v", err)
	}
	wantSig := SignatureTag(env.manDig)
	if !containsString(tags, "6.8.2") || !containsString(tags, wantSig) {
		t.Errorf("recipe repo tags = %v, want 6.8.2 and %s", tags, wantSig)
	}
	if d, ok := env.dst.ResolveTag(ctx, recipeRepo, "6.8.2"); !ok || d != env.manDig {
		t.Errorf("recipe tag resolves %q (ok=%v), want the source manifest digest %s", d, ok, env.manDig)
	}

	// FR-045 provenance: everything the run wrote is class "recipe".
	for _, repo := range []string{appRepo, chartRepo, modelRepo, recipeRepo} {
		p, ok := env.dst.ProvenanceOf(repo)
		if !ok || p.Class != store.ProvenanceRecipe {
			t.Errorf("provenance of %s = %+v (ok=%v), want class recipe", repo, p, ok)
		}
	}

	// FR-044 recipe graph: one complete record.
	recs, err := env.dst.RecipeRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("recipe records = %+v, want exactly one", recs)
	}
	rec := recs[0]
	if rec.Name != "wordpress" || rec.Version != "6.8.2" ||
		rec.CookbookRepo != env.src.addr+"/cookbook/wordpress" ||
		rec.Digest != env.manDig || rec.Zone != testZone ||
		!rec.Verified || rec.TrustScope != "" {
		t.Errorf("recipe record = %+v", rec)
	}
	// FR-054: where the artifact landed in THIS store, which a reader of
	// the transported store cannot recompute from the nominal repository.
	if rec.ArtifactRepo != env.hostRepo+"/cookbook/wordpress" || rec.ArtifactTag != "6.8.2" {
		t.Errorf("recipe record does not locate its artifact on the medium: repo=%q tag=%q",
			rec.ArtifactRepo, rec.ArtifactTag)
	}
	if len(rec.Ingredients) != 3 {
		t.Fatalf("recorded ingredients = %+v, want 3", rec.Ingredients)
	}
	for _, ing := range rec.Ingredients {
		if ing.Repo == "" || ing.Digest == "" || !strings.HasPrefix(ing.Repo, env.hostRepo+"/") {
			t.Errorf("ingredient record %+v lacks its relocated repo or digest", ing)
		}
	}

	// FR-021 resolution report: the recipe row and one row per ingredient.
	rr := resolutionFor(t, task, rid, "")
	if rr.Requested != "6.8.2" || rr.Resolved != "6.8.2" || rr.Digest != env.manDig {
		t.Errorf("recipe resolution = %+v", rr)
	}
	for ing, dig := range map[string]string{"app": env.idx.digest, "chart": env.chartDig, "model": env.modelDig} {
		row := resolutionFor(t, task, rid, ing)
		if row.Digest != dig || row.Status != "new" || row.Requested == "" || row.Resolved != row.Requested {
			t.Errorf("ingredient %s resolution = %+v", ing, row)
		}
	}
}

// TestSyncIdempotent locks NFR-009: an identical second run moves zero
// bytes, every item lands skipped/up-to-date, no errors.
func TestSyncIdempotent(t *testing.T) {
	env := newHappyEnv(t)

	if _, err := runSync(t, env.eng); err != nil {
		t.Fatalf("first run: %v", err)
	}
	task, err := runSync(t, env.eng)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	agg := task.Aggregate()
	if agg.Failed != 0 || agg.Done != 0 || agg.Skipped != 4 {
		t.Errorf("second-run aggregates = %+v, want everything skipped", agg)
	}
	var moved int64
	for _, it := range task.Items {
		moved += it.SizeBytes
		if it.Error != nil {
			t.Errorf("item %s carries error %+v on an idempotent re-run", it.Name, it.Error)
		}
	}
	if moved != 0 {
		t.Errorf("second run transferred %d bytes, want 0 (NFR-009)", moved)
	}
	for _, ing := range []string{"app", "chart", "model"} {
		if row := resolutionFor(t, task, "wordpress@6.8.2", ing); row.Status != "up-to-date" {
			t.Errorf("ingredient %s status = %q, want up-to-date (FR-026)", ing, row.Status)
		}
	}
}

// TestSyncResume locks FR-029: a resumed task's settled items are not
// re-transferred; pending ones complete.
func TestSyncResume(t *testing.T) {
	env := newHappyEnv(t)
	ctx := context.Background()
	const rid = "wordpress@6.8.2"

	// Fabricate an interrupted task: the chart item already settled.
	task := &tasks.Task{
		ID: "tsk_resume", RunID: "run_resume", Type: tasks.TypeSync,
		Status: tasks.StatusRunning, Resumed: true,
		Items: []tasks.Item{{Name: rid + "/chart", Status: tasks.StatusDone, Digest: env.chartDig}},
	}
	if err := runSyncTask(t, env.eng, task, discardLogger()); err != nil {
		t.Fatalf("resumed run: %v", err)
	}

	// The settled item stayed settled and moved nothing: the chart content
	// is absent from the destination — proof it was not re-fetched.
	chartItem := itemByName(t, task, rid+"/chart")
	if chartItem.Status != tasks.StatusDone || chartItem.SizeBytes != 0 {
		t.Errorf("settled chart item = %+v, want done with 0 transferred bytes", chartItem)
	}
	if env.dst.HasManifest(ctx, env.hostRepo+"/charts/wordpress", env.chartDig) {
		t.Errorf("settled chart item was re-transferred (FR-029)")
	}

	// The other items completed normally.
	for _, name := range []string{rid + "/app", rid + "/model", rid + "/recipe"} {
		if it := itemByName(t, task, name); it.Status != tasks.StatusDone {
			t.Errorf("item %s = %s (error: %+v), want done", name, it.Status, it.Error)
		}
	}
	if d, ok := env.dst.ResolveTag(ctx, env.hostRepo+"/models/embedding", "1.0.0"); !ok || d != env.modelDig {
		t.Errorf("model tag resolves %q (ok=%v), want %s", d, ok, env.modelDig)
	}

	// The recipe record still lists every ingredient, settled ones
	// included (the graph is complete even across resumption).
	recs, err := env.dst.RecipeRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || len(recs[0].Ingredients) != 3 {
		t.Errorf("recipe records after resume = %+v, want 1 record with 3 ingredients", recs)
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestSyncWritesMediaManifest locks the FR-054 hook: a mirror instance
// ends every synchronization by writing the medium's manifest, with the
// zone it is addressed to, the run that produced it, and the instant the
// Retriever was resolved — the freshness instant of R-28.
func TestSyncWritesMediaManifest(t *testing.T) {
	env := newHappyEnv(t)
	before := time.Now().UTC().Add(-time.Second)

	var calls int
	var gotZone, gotRun string
	var gotResolved time.Time
	env.eng.SetMediaManifest(func(_ context.Context, zone, runID string, resolvedAt time.Time) error {
		calls++
		gotZone, gotRun, gotResolved = zone, runID, resolvedAt
		return nil
	})

	if _, err := runSync(t, env.eng); err != nil {
		t.Fatalf("run: %v", err)
	}
	if calls != 1 {
		t.Fatalf("the media manifest was written %d times, want exactly once per run", calls)
	}
	if gotZone != testZone {
		t.Errorf("manifest zone = %q, want %q", gotZone, testZone)
	}
	if gotRun != "run_test" {
		t.Errorf("manifest run = %q, want the run that produced the medium", gotRun)
	}
	if gotResolved.Before(before) || gotResolved.After(time.Now().UTC().Add(time.Second)) {
		t.Errorf("resolution instant = %s, want the moment of this run", gotResolved)
	}
}

// TestSyncWithoutMediaWriterWritesNothing: passthrough installs no writer,
// and its store — a cache, not a medium — is never inventoried.
func TestSyncWithoutMediaWriterWritesNothing(t *testing.T) {
	env := newHappyEnv(t)
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.dst.Root(), "meta", "media.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a media manifest appeared without a writer installed: %v", err)
	}
}

// TestSyncFailsWhenTheManifestCannotBeWritten: a medium without a manifest
// is refused whole on the destination side (R-19), so reporting the run as
// successful would promise a delivery that cannot be delivered.
func TestSyncFailsWhenTheManifestCannotBeWritten(t *testing.T) {
	env := newHappyEnv(t)
	env.eng.SetMediaManifest(func(context.Context, string, string, time.Time) error {
		return errors.New("no space left on the medium")
	})
	if _, err := runSync(t, env.eng); err == nil {
		t.Fatal("the run reported success although the medium carries no manifest")
	}
}

// TestMediaManifestOfARealSynchronizationVerifies is the end-to-end check
// this milestone actually rests on: a store produced by the REAL fetch
// pipeline — sparse multi-arch index (FR-022), chart, custom artifact,
// signed recipe with its signature artifacts — is inventoried and then
// verified back, clean.
//
// Written against the pipeline rather than against a hand-built store on
// purpose: the reachability walk encodes what the registry backend lays
// down on disk, and a fixture that agreed with the walk while the product
// disagreed with both would prove nothing.
func TestMediaManifestOfARealSynchronizationVerifies(t *testing.T) {
	env := newHappyEnv(t)
	ctx := context.Background()
	env.eng.SetMediaManifest(func(ctx context.Context, zone, runID string, resolvedAt time.Time) error {
		_, err := media.Write(ctx, env.dst, media.WriteOptions{Zone: zone, RunID: runID, ResolvedAt: resolvedAt})
		return err
	})
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatalf("run: %v", err)
	}

	rep, err := media.Verify(ctx, env.dst, media.VerifyOptions{
		Zone: testZone, Trust: MediaTrust{Policy: env.trust},
	})
	if err != nil {
		t.Fatalf("verifying the medium: %v", err)
	}
	if rep.Verdict != media.VerdictPushable {
		t.Fatalf("verdict = %q, want %q (blocks %+v, recipes %+v)",
			rep.Verdict, media.VerdictPushable, rep.Blocks, rep.Recipes)
	}
	if len(rep.Recipes) != 1 || !rep.Recipes[0].Pushable {
		t.Fatalf("recipe verdicts = %+v", rep.Recipes)
	}
	if rep.Recipes[0].KeyFingerprint == "" {
		t.Error("the recipe verified without naming the key that did it")
	}
	// Everything the medium carries is delivered by the recipe: a real
	// synchronization leaves nothing behind (the sparse index's unselected
	// child is absent, not extraneous).
	for _, f := range rep.Findings {
		t.Errorf("a freshly synchronized medium reports %s on %s", f.Code, f.Path)
	}
	if rep.Media == nil || rep.Media.MediaID == "" || rep.Media.ProducedBy.RunID != "run_test" {
		t.Errorf("media identity lost: %+v", rep.Media)
	}
}
