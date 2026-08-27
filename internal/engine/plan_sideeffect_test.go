// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/schedule"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// The central guarantee of this lot (FR-055 amendment R-04): a plan
// writes nothing.
//
// The test fingerprints the WHOLE store — every path under the storage
// root, with its content digest and its mode — before and after a plan,
// and fails on the smallest difference. Not a list of the things a plan
// is known to touch: the entire tree, so that a future edit which starts
// recording provenance, appending to a task, or dropping a cache file
// inside the store is caught on the day it is written rather than on the
// day a transported store turns out to have drifted.
//
// The store is deliberately the one a REAL synchronization just wrote:
// registry blobs, manifest revisions, tag links, and the meta/ ledgers. A
// plan over an empty directory would prove very little.

// storePrint is a content fingerprint of a whole directory tree.
type storePrint map[string]string

// fingerprint walks root and digests every regular file. Directories are
// recorded too: an empty directory a plan created is still a plan that
// wrote to the store.
func fingerprint(t *testing.T, root string) storePrint {
	t.Helper()
	out := storePrint{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			out[rel+"/"] = "dir"
			return nil
		}
		if !d.Type().IsRegular() {
			out[rel] = "irregular:" + d.Type().String()
			return nil
		}
		raw, rerr := os.ReadFile(path) //nolint:gosec // G304: a path this test walked, under its own temporary root
		if rerr != nil {
			return rerr
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		sum := sha256.Sum256(raw)
		out[rel] = hex.EncodeToString(sum[:]) + ":" + info.Mode().String()
		return nil
	})
	if err != nil {
		t.Fatalf("fingerprinting %s: %v", root, err)
	}
	return out
}

// diff reports what changed between two fingerprints, as readable lines.
func (before storePrint) diff(after storePrint) []string {
	var lines []string
	for path, sum := range after {
		old, existed := before[path]
		switch {
		case !existed:
			lines = append(lines, "+ "+path)
		case old != sum:
			lines = append(lines, "~ "+path)
		}
	}
	for path := range before {
		if _, still := after[path]; !still {
			lines = append(lines, "- "+path)
		}
	}
	sort.Strings(lines)
	return lines
}

// TestPlanLeavesTheStoreByteIdentical is the requirement, executed.
//
// FALLIBILITY (proved 2026-08-26): with a single line added to
// Planner.planIngredient —
//
//	_ = p.store.(interface {
//	    SetProvenance(string, *store.Provenance) error
//	}).SetProvenance(repo, &store.Provenance{Class: store.ProvenanceRecipe})
//
// — the test failed with "plan mutated the store: 1 file differs" and
// "+ meta/provenance.json", and passed again once the line was removed.
// The PlanStore interface is what makes that line need a type assertion
// in the first place; the fingerprint is what makes it visible anyway.
func TestPlanLeavesTheStoreByteIdentical(t *testing.T) {
	env := newPlanEnv(t)

	// A real synchronization first, so the plan runs over a store that
	// holds content, links and ledgers.
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatalf("seeding synchronization: %v", err)
	}
	// Then move the desired state on, so the plan has genuine work to
	// report: a new recipe version and an ingredient the store does not
	// hold are exactly the states that tempt an implementation into
	// writing something down.
	env.advance(t)

	before := fingerprint(t, env.storeRoot)
	plan, err := env.planner.Plan(context.Background(), PlanOptions{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	after := fingerprint(t, env.storeRoot)

	if plan.Outcome != OutcomeChangesPlanned {
		t.Fatalf("outcome = %q, want %q — the fixture must give the plan something to find, "+
			"or this test proves nothing (problems: %+v)", plan.Outcome, OutcomeChangesPlanned, plan.Problems)
	}
	if plan.Totals.TransferBytes == 0 {
		t.Fatal("the plan projects no transfer: the fixture is not exercising the write-tempting path")
	}
	if lines := before.diff(after); len(lines) > 0 {
		t.Errorf("plan mutated the store: %d paths differ (FR-055 amendment R-04)", len(lines))
		for _, l := range lines {
			t.Errorf("  %s", l)
		}
	}
}

// TestPlanOverACandidateLeavesTheStoreByteIdentical covers the other
// entry point R-04 names: a plan over a candidate Retriever document that
// exists nowhere yet. It is the CI-gate case, and the one where an
// implementation is most tempted to spool the submitted document
// somewhere.
func TestPlanOverACandidateLeavesTheStoreByteIdentical(t *testing.T) {
	env := newPlanEnv(t)
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatalf("seeding synchronization: %v", err)
	}
	env.advance(t)

	candidate := retrieverYAML(t, planZone, env.cookbook, []spec.RecipeSelector{
		{Name: planRecipe, Version: "1.1.0"},
	})
	before := fingerprint(t, env.storeRoot)
	plan, err := env.planner.Plan(context.Background(), PlanOptions{RetrieverDocument: candidate})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	after := fingerprint(t, env.storeRoot)

	if !plan.SourceIsCandidate {
		t.Error("a plan over a submitted document must report itself as a candidate run")
	}
	if plan.Outcome != OutcomeChangesPlanned {
		t.Fatalf("outcome = %q, want %q (problems: %+v)", plan.Outcome, OutcomeChangesPlanned, plan.Problems)
	}
	if lines := before.diff(after); len(lines) > 0 {
		t.Errorf("plan over a candidate mutated the store: %v", lines)
	}
}

// TestPlanDoesNotTouchThePassthroughSchedule locks the second half of the
// requirement: a plan "SHALL NOT reset or gate the passthrough refresh
// schedule (FR-013)".
//
// It checks both ways the cadence could move — the persisted override in
// the state directory, and the live effective value the scheduler reads
// on every cycle — and fingerprints the whole state directory, which is
// where an implementation tempted to cache its last plan would most
// naturally put it.
//
// FALLIBILITY (proved 2026-08-26): with `_ = iv.Set(time.Hour, "plan",
// time.Now())` reachable from the plan path, the test failed with
// "the plan moved the effective interval: 15m0s → 1h0m0s" and
// "the plan wrote into the state directory: [+ schedule.json]".
func TestPlanDoesNotTouchThePassthroughSchedule(t *testing.T) {
	env := newPlanEnv(t)
	stateRoot := t.TempDir()
	iv, err := schedule.Open(stateRoot, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	beforeInterval := iv.Effective()
	beforeOverridden := iv.Overridden()
	beforeState := fingerprint(t, stateRoot)

	if _, err := env.planner.Plan(context.Background(), PlanOptions{}); err != nil {
		t.Fatalf("plan: %v", err)
	}

	if got := iv.Effective(); got != beforeInterval {
		t.Errorf("the plan moved the effective interval: %s → %s (FR-013)", beforeInterval, got)
	}
	if got := iv.Overridden(); got != beforeOverridden {
		t.Errorf("the plan changed the override state: %v → %v (FR-013)", beforeOverridden, got)
	}
	if lines := beforeState.diff(fingerprint(t, stateRoot)); len(lines) > 0 {
		t.Errorf("the plan wrote into the state directory: %v", lines)
	}
}

// The plan fixture.

const (
	planZone   = "zone-plan"
	planRecipe = "demo"
)

// planEnv is one fully wired plan fixture: a real source registry serving
// a real cookbook, a real local store, and both the Engine that seeds it
// and the Planner under test — the SAME planner the engine's own FR-055
// gate uses, so the tests exercise production wiring, not a parallel one.
type planEnv struct {
	src           *registry
	dst           *store.Store
	storeRoot     string
	eng           *Engine
	planner       *Planner
	retrieverPath string
	cookbook      string
	key           *sigtest.KeyPair
	hostRepo      string
}

func newPlanEnv(t *testing.T) *planEnv {
	t.Helper()
	src := newRegistry(t)
	dst := openStore(t)
	kp := newKeyPair(t)
	cookbook := src.addr + "/cookbook"

	seedPlanRecipe(t, src, kp, "1.0.0")

	// The Retriever lives at a STABLE path: advance() rewrites it in
	// place, which is how a desired state actually changes.
	dir := t.TempDir()
	retrieverPath := filepath.Join(dir, "retriever.yaml")
	writeRetriever(t, retrieverPath, cookbook, "1.0.0")

	eng := New(dst, newRemotes(t, nil), trustFor(t, nil, kp), retrieverPath, "", syncCfg())
	return &planEnv{
		src: src, dst: dst, storeRoot: dst.Root(),
		eng: eng, planner: eng.Planner(),
		retrieverPath: retrieverPath, cookbook: cookbook, key: kp,
		hostRepo: strings.ReplaceAll(src.addr, ":", "_"),
	}
}

// advance publishes a new recipe version with fresh content and points
// the Retriever at it.
func (e *planEnv) advance(t *testing.T) {
	t.Helper()
	seedPlanRecipe(t, e.src, e.key, "1.1.0")
	writeRetriever(t, e.retrieverPath, e.cookbook, "1.1.0")
}

// seedPlanRecipe publishes one signed cooked recipe version, with an
// image whose content is unique to that version.
func seedPlanRecipe(t *testing.T, src *registry, kp *sigtest.KeyPair, version string) {
	t.Helper()
	imageDigest := seedImage(t, src, "ingredients/app", version)
	yaml := cookedRecipeYAML(t, planRecipe, version, []spec.Ingredient{{
		Name: "app", Kind: spec.IngredientContainerImage,
		Ref: src.addr + "/ingredients/app", Version: version, Digest: imageDigest,
	}})
	manifestDigest := publishRecipe(t, src.st, "cookbook/"+planRecipe, version, yaml)
	signManifest(t, src.st, "cookbook/"+planRecipe, manifestDigest, kp)
}

// writeRetriever replaces the Retriever document in place.
func writeRetriever(t *testing.T, path, cookbook, version string) {
	t.Helper()
	raw := retrieverYAML(t, planZone, cookbook, []spec.RecipeSelector{
		{Name: planRecipe, Version: version},
	})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
