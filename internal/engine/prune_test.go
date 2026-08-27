// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"

	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// Prune to the Retriever (FR-045 amendment, R-33).
//
// The fixture is a real two-recipe zone: one recipe stays in the
// Retriever, one is dropped. Everything below asserts against the real
// embedded store — what a repository still answers, what its tags are —
// because the requirement is about content that has actually gone, not
// about a ledger that says it did.

// pruneEnv is a zone with two recipes, "alpha" and "beta", synchronized
// into a destination store. beta carries three ingredients so the
// protected roots can be exercised on real candidates: content that IS in
// a stale record and survives anyway, because of where it came from.
type pruneEnv struct {
	src      *registry
	dst      *store.Store
	eng      *Engine
	retrPath string
	hostRepo string
	// prune is what the NEXT run is asked to do. It lives on the task,
	// not on the engine (FR-045): mirror mode confirms it at trigger
	// time, passthrough reads it from configuration, and a resumed run
	// must carry the decision that was made rather than a default.
	prune bool
}

func newPruneEnv(t *testing.T) *pruneEnv {
	t.Helper()
	src := newRegistry(t)
	dst := openStore(t)
	kp := newKeyPair(t)

	alphaDig := seedImage(t, src, "ingredients/alpha", "1.0.0")
	betaDig := seedImage(t, src, "ingredients/beta", "2.0.0")
	vulnDig := seedImage(t, src, "ingredients/vulndb", "2.0.0")
	seedDig := seedImage(t, src, "ingredients/seeded", "2.0.0")
	// One ingredient both recipes pin at the same digest: relocation maps
	// it to ONE repository (FR-035), which is what makes prune a
	// tag-reachability question rather than a per-recipe one.
	sharedDig := seedImage(t, src, "ingredients/shared", "3.0.0")

	publishSigned(t, src, kp, "alpha", "1.0.0", []spec.Ingredient{
		ingredient(src, "app", "ingredients/alpha", "1.0.0", alphaDig),
		ingredient(src, "shared", "ingredients/shared", "3.0.0", sharedDig),
	})
	publishSigned(t, src, kp, "beta", "2.0.0", []spec.Ingredient{
		ingredient(src, "app", "ingredients/beta", "2.0.0", betaDig),
		ingredient(src, "vulndb", "ingredients/vulndb", "2.0.0", vulnDig),
		ingredient(src, "seeded", "ingredients/seeded", "2.0.0", seedDig),
		ingredient(src, "shared", "ingredients/shared", "3.0.0", sharedDig),
	})

	env := &pruneEnv{
		src: src, dst: dst,
		retrPath: filepath.Join(t.TempDir(), "retriever.yaml"),
		hostRepo: strings.ReplaceAll(src.addr, ":", "_"),
	}
	env.setRetriever(t, "alpha", "beta")
	env.eng = New(dst, newRemotes(t, nil), trustFor(t, nil, kp), env.retrPath, "", syncCfg())
	return env
}

// ingredient builds one pinned ingredient row of the fixture.
func ingredient(src *registry, name, repoPath, version, dgst string) spec.Ingredient {
	return spec.Ingredient{
		Name: name, Kind: spec.IngredientContainerImage,
		Ref: src.addr + "/" + repoPath, Version: version, Digest: dgst,
	}
}

// publishSigned publishes one cooked, signed recipe into the fixture's
// cookbook.
func publishSigned(t *testing.T, src *registry, kp *sigtest.KeyPair, recipeName, version string, ings []spec.Ingredient) {
	t.Helper()
	repo := "cookbook/" + recipeName
	dig := publishRecipe(t, src.st, repo, version, cookedRecipeYAML(t, recipeName, version, ings))
	signManifest(t, src.st, repo, dig, kp)
}

// setRetriever rewrites the Retriever document in place, which is how a
// zone drops a recipe: the source path never changes, its content does.
func (e *pruneEnv) setRetriever(t *testing.T, names ...string) {
	t.Helper()
	entries := make([]spec.RecipeSelector, 0, len(names))
	for _, n := range names {
		version := "1.0.0"
		if n != "alpha" {
			version = "2.0.0"
		}
		entries = append(entries, spec.RecipeSelector{Name: n, Version: version})
	}
	raw := retrieverYAML(t, testZone, e.src.addr+"/cookbook", entries)
	if err := os.WriteFile(e.retrPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// repo names the relocated store path of one source repository (FR-035).
func (e *pruneEnv) repo(path string) string { return e.hostRepo + "/" + path }

// present reports whether a repository still answers with any tag.
func (e *pruneEnv) present(t *testing.T, repo string) bool {
	t.Helper()
	tags, err := e.dst.Tags(context.Background(), repo)
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(tags) > 0
}

// protect rewrites a repository's recorded provenance, standing in for the
// two ways content arrives outside a Recipe run: a unit import (FR-023)
// and a push through /v2/ by a standard client (UC3 seeding, recorded as
// class "seed" — the same verdict the ledger gives an absent entry). The
// offline vulnerability database (FR-032) arrives through one of those two
// doors, which is why it needs no rule of its own.
func (e *pruneEnv) protect(t *testing.T, repo string, class store.ProvenanceClass) {
	t.Helper()
	if err := e.dst.SetProvenance(repo, &store.Provenance{Class: class}); err != nil {
		t.Fatal(err)
	}
}

// runWithLog runs one cycle and returns the task plus the run log — the
// FR-053 media log, where FR-045 requires pruned items to be listed.
func (e *pruneEnv) runWithLog(t *testing.T) (task *tasks.Task, runLog string) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	task = &tasks.Task{
		ID: "tsk_test", RunID: "run_test", Type: tasks.TypeSync,
		Status: tasks.StatusRunning, Prune: e.prune,
	}
	if err := runSyncTask(t, e.eng, task, logger); err != nil {
		t.Fatalf("run: %v", err)
	}
	return task, buf.String()
}

// TestPruneRemovesContentDroppedFromTheRetriever is the R-33 acceptance:
// with the opt-in on, content the Retriever no longer references is gone
// at the next reconciliation and listed in the run log, while the
// protected roots survive untouched.
func TestPruneRemovesContentDroppedFromTheRetriever(t *testing.T) {
	env := newPruneEnv(t)
	if _, _ = env.runWithLog(t); !env.present(t, env.repo("ingredients/beta")) {
		t.Fatal("the fixture did not synchronize beta in the first place")
	}

	// The protected roots, on content a stale record DOES list: without
	// this the exclusion would never be reached and the test would prove
	// nothing about it.
	env.protect(t, env.repo("ingredients/vulndb"), store.ProvenanceUnitImport)
	env.protect(t, env.repo("ingredients/seeded"), store.ProvenanceSeed)

	env.setRetriever(t, "alpha")
	env.prune = true
	task, logs := env.runWithLog(t)

	if env.present(t, env.repo("ingredients/beta")) {
		t.Error("content dropped from the Retriever survived a prune")
	}
	if env.present(t, env.repo("cookbook/beta")) {
		t.Error("the recipe artifact of a dropped recipe survived a prune")
	}
	if !env.present(t, env.repo("ingredients/alpha")) {
		t.Error("prune removed content the Retriever still references")
	}
	for _, protected := range []string{"ingredients/vulndb", "ingredients/seeded"} {
		if !env.present(t, env.repo(protected)) {
			t.Errorf("%s was pruned: only recipe-managed content is ever eligible (FR-045)", protected)
		}
	}

	// The graph forgets the recipe, so the next cycle computes
	// reachability against a state that exists.
	recs, err := env.dst.RecipeRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Name != "alpha" {
		t.Errorf("recipe records after prune = %+v, want only alpha", recs)
	}

	// FR-053: every removed item is named in the run log, not merely
	// counted — after the medium has travelled, a count answers nothing.
	if !strings.Contains(logs, `"content pruned"`) ||
		!strings.Contains(logs, env.repo("ingredients/beta")) {
		t.Errorf("the run log does not name the pruned content:\n%s", logs)
	}
	// …and on the task, for the detail screen and its API mirror.
	var prunedRepos []string
	for _, p := range task.Pruned {
		prunedRepos = append(prunedRepos, p.Repo)
		if p.Recipe != "beta@2.0.0" {
			t.Errorf("pruned row %+v does not name the recipe that brought it", p)
		}
	}
	if !containsString(prunedRepos, env.repo("ingredients/beta")) {
		t.Errorf("task prune report = %+v", task.Pruned)
	}
	for _, p := range task.Pruned {
		if strings.Contains(p.Repo, "vulndb") || strings.Contains(p.Repo, "seeded") {
			t.Errorf("a protected root appears in the prune report: %+v", p)
		}
	}
}

// TestPruneRunsOnlyWhenTheRunAskedFor is the other half of the decision
// (FR-075 posture): the very same drop leaves the store untouched when
// the run was not asked to remove anything. That is what a passthrough
// cycle looks like by default, and what a mirror trigger looks like when
// the operator unticks the box after reading the projection.
func TestPruneRunsOnlyWhenTheRunAskedFor(t *testing.T) {
	env := newPruneEnv(t)
	env.runWithLog(t)

	env.setRetriever(t, "alpha")
	task, logs := env.runWithLog(t)

	if !env.present(t, env.repo("ingredients/beta")) {
		t.Error("content was removed by a reconciliation nobody asked to prune")
	}
	if !env.present(t, env.repo("cookbook/beta")) {
		t.Error("a recipe artifact was removed by a reconciliation nobody asked to prune")
	}
	if len(task.Pruned) != 0 {
		t.Errorf("prune report on a run that did not ask = %+v, want empty", task.Pruned)
	}
	if strings.Contains(logs, `"content pruned"`) {
		t.Errorf("a run that did not ask to prune logged one:\n%s", logs)
	}
	recs, err := env.dst.RecipeRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Errorf("recipe records = %+v, want both kept when the run did not ask to prune", recs)
	}
}

// TestPruneSkippedOnAnIncompleteRun locks the hazard that makes prune
// dangerous at all: a recipe that failed to resolve contributes no graph
// entry, so its content is indistinguishable from content the Retriever
// dropped. Pruning then would delete, on the strength of a network
// failure, the very content the next zone is waiting for.
func TestPruneSkippedOnAnIncompleteRun(t *testing.T) {
	env := newPruneEnv(t)
	env.runWithLog(t)

	// alpha stays and resolves; "ghost" is in the Retriever and in no
	// cookbook, so the entry fails. beta is dropped — and must survive
	// anyway, because this run cannot tell why it is missing.
	env.setRetriever(t, "alpha", "ghost")
	env.prune = true
	task, logs := env.runWithLog(t)

	if agg := task.Aggregate(); agg.Failed == 0 {
		t.Fatalf("the fixture resolved everything (%+v): the skip is not being exercised", agg)
	}
	if !env.present(t, env.repo("ingredients/beta")) {
		t.Error("a partial run pruned content: an unresolved recipe is not a dropped one")
	}
	if len(task.Pruned) != 0 {
		t.Errorf("prune report on a partial run = %+v, want empty", task.Pruned)
	}
	if !strings.Contains(logs, "prune skipped") {
		t.Errorf("the skip was silent:\n%s", logs)
	}
}

// TestPruneRemovesTheSignatureArtifacts: signatures are tagged objects,
// so leaving them behind keeps the manifest reachable and the prune frees
// nothing at all (§12.2, FR-044 mark-and-sweep from tags).
func TestPruneRemovesTheSignatureArtifacts(t *testing.T) {
	env := newPruneEnv(t)
	env.runWithLog(t)

	cookbookRepo := env.repo("cookbook/beta")
	before, err := env.dst.Tags(context.Background(), cookbookRepo)
	if err != nil {
		t.Fatal(err)
	}
	var sigTags int
	for _, tag := range before {
		if strings.HasPrefix(tag, "sha256-") {
			sigTags++
		}
	}
	if sigTags == 0 {
		t.Fatalf("the fixture stored no signature artifact (tags: %v)", before)
	}

	env.setRetriever(t, "alpha")
	env.prune = true
	env.runWithLog(t)

	if _, err := env.dst.Tags(context.Background(), cookbookRepo); !errors.Is(err, store.ErrNotFound) {
		tags, _ := env.dst.Tags(context.Background(), cookbookRepo)
		t.Errorf("the pruned cookbook repository still answers with tags %v", tags)
	}
	// A repository with no tag left is not content any more: it goes with
	// its tags, and the provenance ledger forgets it — the media manifest
	// must not have to describe an empty directory (FR-054).
	if p, ok := env.dst.ProvenanceOf(cookbookRepo); ok {
		t.Errorf("the pruned repository kept its provenance entry: %+v", p)
	}
}

// TestPruneKeepsContentSharedWithARetainedRecipe: two recipes pinning the
// same ingredient share ONE relocated repository (FR-035), so removing one
// of them must leave the shared tag alone. Reachability decides, not the
// stale record that happens to list it.
func TestPruneKeepsContentSharedWithARetainedRecipe(t *testing.T) {
	env := newPruneEnv(t)
	env.runWithLog(t)
	shared := env.repo("ingredients/shared")
	if !env.present(t, shared) {
		t.Fatal("the fixture did not synchronize the shared ingredient")
	}

	env.setRetriever(t, "alpha")
	env.prune = true
	task, _ := env.runWithLog(t)

	if !env.present(t, shared) {
		t.Error("prune removed a tag the retained recipe still references (FR-035 shared relocation)")
	}
	for _, p := range task.Pruned {
		if p.Repo == shared {
			t.Errorf("the shared ingredient appears in the prune report: %+v", p)
		}
	}
	// The dropped recipe's exclusive content still goes: without this the
	// test could pass by pruning nothing at all.
	if env.present(t, env.repo("ingredients/beta")) {
		t.Error("exclusive content of the dropped recipe survived")
	}
}

// TestPruneReportSurvivesTheTaskRoundTrip: the removal report is part of
// the persisted task, so the detail screen and the API read the same rows
// after a restart (FR-061).
func TestPruneReportSurvivesTheTaskRoundTrip(t *testing.T) {
	task := &tasks.Task{ID: "tsk_x", Pruned: []tasks.Pruned{
		{Repo: "host/ingredients/beta", Tag: "2.0.0", Digest: "sha256:abc", Recipe: "beta@2.0.0"},
	}}
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var back tasks.Task
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Pruned) != 1 || back.Pruned[0].Repo != "host/ingredients/beta" ||
		back.Pruned[0].Recipe != "beta@2.0.0" {
		t.Errorf("prune report after a round trip = %+v", back.Pruned)
	}
	// A task that pruned nothing carries no key at all: the report is a
	// milestone-5 addition, and an older reader must not have to know it.
	empty, err := json.Marshal(&tasks.Task{ID: "tsk_y"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "pruned") {
		t.Errorf("an empty prune report serializes a key: %s", empty)
	}
}

// TestProjectPruneMatchesWhatTheRunRemoves is the FR-045 confirmation
// requirement: the list and the total size an operator is shown before the
// trigger must be what actually goes. Two implementations of the same
// question is how a confirmation becomes a lie, so the projection and the
// run share one candidate computation — and this test is what keeps that
// true.
func TestProjectPruneMatchesWhatTheRunRemoves(t *testing.T) {
	env := newPruneEnv(t)
	env.runWithLog(t)
	env.setRetriever(t, "alpha")

	projection, err := env.eng.ProjectPrune(context.Background())
	if err != nil {
		t.Fatalf("ProjectPrune: %v", err)
	}
	if len(projection.Items) == 0 {
		t.Fatal("the projection lists nothing after a recipe was dropped")
	}
	if projection.TotalBytes <= 0 {
		t.Errorf("projected total = %d bytes: the confirmation must state a size, not just a list",
			projection.TotalBytes)
	}
	projected := map[string]bool{}
	for _, it := range projection.Items {
		projected[it.Repo+":"+it.Tag] = true
		if it.Recipe != "beta@2.0.0" {
			t.Errorf("projected row %+v does not name the recipe that brought it", it)
		}
	}
	// The shared ingredient and the protected roots are absent from the
	// projection for the same reasons they survive the run.
	if projected[env.repo("ingredients/shared")+":3.0.0"] {
		t.Error("the projection lists a tag the retained recipe still references")
	}

	env.prune = true
	task, _ := env.runWithLog(t)
	removed := map[string]bool{}
	for _, p := range task.Pruned {
		removed[p.Repo+":"+p.Tag] = true
	}
	if len(removed) != len(projected) {
		t.Errorf("the run removed %d items, the confirmation promised %d", len(removed), len(projected))
	}
	for ref := range projected {
		if !removed[ref] {
			t.Errorf("%s was shown in the confirmation and not removed", ref)
		}
	}
	for ref := range removed {
		if !projected[ref] {
			t.Errorf("%s was removed without appearing in the confirmation", ref)
		}
	}
}

// TestProjectPruneRefusesAnUnresolvableRetriever: "nothing would be
// removed" and "I could not work out what would be removed" are opposite
// statements, and confirming a removal against the second one is how an
// operator deletes a zone's content by pressing a button that looked
// harmless.
func TestProjectPruneRefusesAnUnresolvableRetriever(t *testing.T) {
	env := newPruneEnv(t)
	env.runWithLog(t)
	env.setRetriever(t, "alpha", "ghost")

	projection, err := env.eng.ProjectPrune(context.Background())
	if err == nil {
		t.Fatalf("ProjectPrune succeeded on an unresolvable Retriever: %+v", projection)
	}
	if len(projection.Items) != 0 {
		t.Errorf("a failed projection still listed items: %+v", projection.Items)
	}
}
