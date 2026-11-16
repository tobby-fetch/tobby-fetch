// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/assurance"
)

// provenanceFixture is the FR-098 acceptance store: one recipe-managed
// repository, one admitted under a relaxed scope, one unit import, one
// packed FileSet and one pushed repository — plus the recipe artifact's
// own repository, which a recipe manages too.
func provenanceFixture(t *testing.T) *Store {
	t.Helper()
	st := openMetaTestStore(t)
	for _, repo := range []string{
		"docker.io/library/nginx", "docker.io/library/redis", "ghcr.io/team/tool",
		"localhost/filesets/docs", "zone/seeded/app", "registry.example.com/cookbook/web",
	} {
		seedManifest(t, st, repo, "1.0.0", "layer of "+repo)
	}
	if err := st.PutRecipeRecord(&RecipeRecord{
		Name: "web", Version: "1.0.0", Verified: true,
		ArtifactRepo: "registry.example.com/cookbook/web",
		Ingredients:  []IngredientRecord{{Name: "nginx", Repo: "docker.io/library/nginx", Digest: "sha256:x"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRecipeRecord(&RecipeRecord{
		Name: "cache", Version: "2.0.0", Verified: false, TrustScope: "lab",
		Ingredients: []IngredientRecord{{Name: "redis", Repo: "docker.io/library/redis", Digest: "sha256:y"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordUnitImport("ghcr.io/team/tool"); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkManualImport("localhost/filesets/docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordSeed("zone/seeded/app"); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestClassifyEveryProvenanceClass locks the one classification rule of
// FR-098 every surface reads.
func TestClassifyEveryProvenanceClass(t *testing.T) {
	st := provenanceFixture(t)
	cases := map[string]Classification{
		"docker.io/library/nginx":           {Class: ProvenanceRecipe, Recipes: []string{"web@1.0.0"}},
		"docker.io/library/redis":           {Class: ProvenanceRecipe, Recipes: []string{"cache@2.0.0"}, Assurance: assurance.ForUnsigned("lab")},
		"registry.example.com/cookbook/web": {Class: ProvenanceRecipe, Recipes: []string{"web@1.0.0"}},
		"ghcr.io/team/tool":                 {Class: ProvenanceUnitImport},
		"localhost/filesets/docs":           {Class: ProvenanceUnitImport, Origin: OriginLocalPack},
		"zone/seeded/app":                   {Class: ProvenanceSeed},
		// No record at all: pushed by a store written before R-17.
		"never/recorded": {Class: ProvenanceSeed},
	}
	c, err := st.Classifier()
	if err != nil {
		t.Fatal(err)
	}
	for repo, want := range cases {
		if got := c.Classify(repo); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: classified %+v, want %+v", repo, got, want)
		}
		if got := st.Classify(repo); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: Store.Classify %+v, want %+v", repo, got, want)
		}
	}

	counts, err := st.UnmanagedRepositories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// FR-098 acceptance: two unit-import (the import and the packed
	// FileSet) and one seed; recipe content — reduced assurance included —
	// is managed.
	if counts[ProvenanceUnitImport] != 2 || counts[ProvenanceSeed] != 1 || len(counts) != 2 {
		t.Errorf("unmanaged = %v, want 2 unit-import and 1 seed", counts)
	}
}

// TestRecordingNeverDowngrades keeps the ledger conservative: a push or a
// unit import never turns a repository into a class that is easier to
// remove, and never overrides a live recipe. Only a missing entry, or the
// stale entry of a recipe no longer recorded, is written.
func TestRecordingNeverDowngrades(t *testing.T) {
	st := provenanceFixture(t)
	for _, c := range []struct {
		repo string
		rec  func(string) (bool, error)
		want ProvenanceClass
	}{
		{"zone/seeded/app", st.RecordUnitImport, ProvenanceSeed},           // seeded stays seeded
		{"ghcr.io/team/tool", st.RecordSeed, ProvenanceUnitImport},         // first class kept
		{"localhost/filesets/docs", st.RecordSeed, ProvenanceUnitImport},   // local pack kept
		{"docker.io/library/nginx", st.RecordUnitImport, ProvenanceRecipe}, // live recipe wins
		{"docker.io/library/nginx", st.RecordSeed, ProvenanceRecipe},
	} {
		wrote, err := c.rec(c.repo)
		if err != nil {
			t.Fatal(err)
		}
		if wrote {
			t.Errorf("%s: recording over an existing class wrote", c.repo)
		}
		if got := st.Classify(c.repo).Class; got != c.want {
			t.Errorf("%s: class %s, want %s", c.repo, got, c.want)
		}
	}
	if p, _ := st.ProvenanceOf("localhost/filesets/docs"); p.Origin != OriginLocalPack {
		t.Errorf("local-pack origin lost: %+v", p)
	}

	// A stale recipe entry — its recipe no longer recorded — is replaced.
	if err := st.SetProvenance("old/recipe/repo", &Provenance{Class: ProvenanceRecipe, Recipe: "gone", RecipeVersion: "1"}); err != nil {
		t.Fatal(err)
	}
	if wrote, err := st.RecordUnitImport("old/recipe/repo"); err != nil || !wrote {
		t.Fatalf("stale recipe entry not replaced: %v %v", wrote, err)
	}
	if got := st.Classify("old/recipe/repo").Class; got != ProvenanceUnitImport {
		t.Errorf("class %s, want unit-import", got)
	}
}

// TestRemovalLogCarriesTheProvenance locks the FR-090 half of FR-098 on
// the removal record: it says what was removed.
func TestRemovalLogCarriesTheProvenance(t *testing.T) {
	st := provenanceFixture(t)
	var buf bytes.Buffer
	if err := st.DeleteRepository(context.Background(), "ghcr.io/team/tool", slog.New(slog.NewJSONHandler(&buf, nil))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"provenance":"unit-import"`) {
		t.Errorf("removal record misses the provenance field: %s", buf.String())
	}
}
