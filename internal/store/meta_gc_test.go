// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
)

func openMetaTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestStoreFormatVersion pins the R-26 compatibility policy: a fresh store
// is stamped with the current format version; an unsupported one refuses
// to open, naming both versions and the standard-tooling escape route
// (FR-051).
func TestStoreFormatVersion(t *testing.T) {
	root := t.TempDir()
	st, err := Open(context.Background(), root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	raw, err := os.ReadFile(filepath.Join(root, "meta", "format.json")) //nolint:gosec // G304: test-owned temp path
	if err != nil {
		t.Fatalf("fresh store not stamped: %v", err)
	}
	var f formatFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.StoreFormat != FormatVersion {
		t.Fatalf("stamped version = %d, want %d", f.StoreFormat, FormatVersion)
	}

	// A future-format store refuses to open with an actionable message:
	// both versions named, escape route through standard OCI tooling.
	if err := os.WriteFile(filepath.Join(root, "meta", "format.json"),
		[]byte(`{"storeFormat": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Open(context.Background(), root, slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("future-format store must refuse to open")
	}
	for _, want := range []string{"99", "1", "FR-051", "skopeo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("format error misses %q: %v", want, err)
		}
	}

	// A pre-versioning store (no meta/format.json) opens and gets stamped:
	// reading older layouts of the 1.x series is the two-way guarantee.
	if err := os.Remove(filepath.Join(root, "meta", "format.json")); err != nil {
		t.Fatal(err)
	}
	st, err = Open(context.Background(), root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("pre-versioning store must open: %v", err)
	}
	_ = st.Close()
}

// TestProvenanceAndRecipeRecords covers the meta ledgers: provenance
// classes (FR-045 groundwork), the recipe graph, and the recipe-managed
// lookup behind the FR-044 amendment.
func TestProvenanceAndRecipeRecords(t *testing.T) {
	st := openMetaTestStore(t)

	if _, ok := st.ProvenanceOf("docker.io/library/redis"); ok {
		t.Error("unknown repository must have no provenance (seed class)")
	}
	if err := st.SetProvenance("docker.io/library/redis", &Provenance{
		Class: ProvenanceUnitImport, RunID: "run-1",
	}); err != nil {
		t.Fatal(err)
	}
	p, ok := st.ProvenanceOf("docker.io/library/redis")
	if !ok || p.Class != ProvenanceUnitImport || p.RecordedAt.IsZero() {
		t.Fatalf("provenance = %+v, ok=%v", p, ok)
	}

	rec := RecipeRecord{
		Name: "wordpress", Version: "6.8.2",
		CookbookRepo: "registry.example.com/cookbook/wordpress",
		Digest:       "sha256:0123", Zone: "zone-a",
		ResolvedAt: time.Now().UTC(), Verified: true,
		Ingredients: []IngredientRecord{
			{Name: "app", Kind: "ContainerImage", Repo: "docker.io/bitnami/wordpress", Digest: "sha256:aa"},
		},
	}
	if err := st.PutRecipeRecord(&rec); err != nil {
		t.Fatal(err)
	}
	records, err := st.RecipeRecords()
	if err != nil || len(records) != 1 || records[0].Zone != "zone-a" {
		t.Fatalf("records = %+v, err=%v", records, err)
	}
	if got := st.ManagingRecipes("docker.io/bitnami/wordpress"); len(got) != 1 || got[0] != "wordpress@6.8.2" {
		t.Fatalf("ManagingRecipes = %v", got)
	}
	if got := st.ManagingRecipes("docker.io/library/redis"); got != nil {
		t.Fatalf("unmanaged repo reports %v", got)
	}
	if err := st.DeleteRecipeRecord("wordpress", "6.8.2"); err != nil {
		t.Fatal(err)
	}
	if got := st.ManagingRecipes("docker.io/bitnami/wordpress"); got != nil {
		t.Fatalf("deleted record still manages %v", got)
	}
}

// seedManifest writes a tiny single-manifest artifact into repo:tag and
// returns the manifest and layer digests.
func seedManifest(t *testing.T, st *Store, repo, tag, layerContent string) (manifestDigest, layerDigest string) {
	t.Helper()
	ctx := context.Background()
	layer := []byte(layerContent)
	layerDgst := digest.FromBytes(layer)
	cfg := []byte(`{}`)
	cfgDgst := digest.FromBytes(cfg)
	for _, b := range []struct {
		d digest.Digest
		p []byte
	}{{layerDgst, layer}, {cfgDgst, cfg}} {
		if err := st.WriteBlob(ctx, repo, b.d, bytes.NewReader(b.p)); err != nil {
			t.Fatal(err)
		}
	}
	man := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    cfgDgst.String(), "size": len(cfg),
		},
		"layers": []map[string]any{{
			"mediaType": "application/vnd.oci.image.layer.v1.tar",
			"digest":    layerDgst.String(), "size": len(layer),
		}},
	}
	raw, err := json.Marshal(man)
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.PutManifest(ctx, repo, "application/vnd.oci.image.manifest.v1+json", raw, tag)
	if err != nil {
		t.Fatal(err)
	}
	return d.String(), layerDgst.String()
}

// blobExists reports whether the blob data file is on disk.
func blobExists(st *Store, dgst string) bool {
	hex := strings.TrimPrefix(dgst, "sha256:")
	_, err := os.Stat(filepath.Join(st.Root(), "docker", "registry", "v2",
		"blobs", "sha256", hex[:2], hex, "data"))
	return err == nil
}

// TestDeleteRepositoryAndSweep covers the FR-044 amendment mechanics:
// removing a unit-imported repository deletes its manifests and its
// unshared blobs; blobs shared with another repository survive; the
// recipe-managed guard refuses with the managing recipes named.
func TestDeleteRepositoryAndSweep(t *testing.T) {
	st := openMetaTestStore(t)
	ctx := context.Background()
	logger := slog.New(slog.DiscardHandler)

	old := sweepGrace
	sweepGrace = 0
	defer func() { sweepGrace = old }()

	// Two repositories sharing one blob ("shared"), each with an
	// exclusive one.
	_, sharedBlob := seedManifest(t, st, "docker.io/a/one", "1.0", "shared")
	_, exclusiveBlob := seedManifest(t, st, "docker.io/a/one", "1.1", "only-one")
	_, keptBlob := seedManifest(t, st, "docker.io/b/two", "2.0", "shared")
	if sharedBlob != keptBlob {
		t.Fatalf("fixture: %s and %s should share the blob", sharedBlob, keptBlob)
	}
	if err := st.SetProvenance("docker.io/a/one", &Provenance{Class: ProvenanceUnitImport}); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteRepository(ctx, "docker.io/a/one", logger); err != nil {
		t.Fatalf("DeleteRepository: %v", err)
	}
	if _, err := st.RepoInfo(ctx, "docker.io/a/one"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted repository still resolves: %v", err)
	}
	if blobExists(st, exclusiveBlob) {
		t.Error("exclusive blob survived the sweep")
	}
	if !blobExists(st, sharedBlob) {
		t.Error("shared blob was swept although docker.io/b/two references it")
	}
	if _, ok := st.ProvenanceOf("docker.io/a/one"); ok {
		t.Error("provenance entry survived the removal")
	}
	// The surviving repository still serves its content.
	if _, _, _, err := st.RawManifest(ctx, "docker.io/b/two", "2.0"); err != nil {
		t.Errorf("surviving repository broken: %v", err)
	}

	// Recipe-managed content refuses individual removal (FR-044
	// amendment), naming the recipes.
	seedManifest(t, st, "docker.io/c/managed", "3.0", "managed")
	if err := st.PutRecipeRecord(&RecipeRecord{
		Name: "app", Version: "1.0",
		Ingredients: []IngredientRecord{{Name: "m", Repo: "docker.io/c/managed", Digest: "sha256:cc"}},
	}); err != nil {
		t.Fatal(err)
	}
	err := st.DeleteRepository(ctx, "docker.io/c/managed", logger)
	var managed *RecipeManagedError
	if !errors.As(err, &managed) || len(managed.Recipes) != 1 || managed.Recipes[0] != "app@1.0" {
		t.Fatalf("recipe-managed delete = %v, want RecipeManagedError naming app@1.0", err)
	}

	// Unknown repository: ErrNotFound.
	if err := st.DeleteRepository(ctx, "docker.io/ghost/none", logger); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown repository delete = %v, want ErrNotFound", err)
	}
}

// TestDeleteTagKeepsSharedContent: untagging one of two tags on distinct
// manifests sweeps only the unreferenced one (the recipe-removal path).
func TestDeleteTagKeepsSharedContent(t *testing.T) {
	st := openMetaTestStore(t)
	ctx := context.Background()

	old := sweepGrace
	sweepGrace = 0
	defer func() { sweepGrace = old }()

	man1, blob1 := seedManifest(t, st, "docker.io/a/multi", "1.0", "v1-content")
	man2, blob2 := seedManifest(t, st, "docker.io/a/multi", "2.0", "v2-content")
	if err := st.DeleteTag(ctx, "docker.io/a/multi", "1.0"); err != nil {
		t.Fatal(err)
	}
	if blobExists(st, blob1) {
		t.Error("untagged manifest's blob survived")
	}
	if !blobExists(st, blob2) {
		t.Error("remaining tag's blob was swept")
	}
	if st.HasManifest(ctx, "docker.io/a/multi", man1) {
		t.Error("untagged manifest still resolvable")
	}
	if !st.HasManifest(ctx, "docker.io/a/multi", man2) {
		t.Error("remaining manifest gone")
	}
}

// TestBlobReaderAndTags covers the raw read accessors the verifier and
// the FileSet server build on.
func TestBlobReaderAndTags(t *testing.T) {
	st := openMetaTestStore(t)
	ctx := context.Background()
	_, layerDgst := seedManifest(t, st, "docker.io/a/read", "1.0", "read-me")

	rc, err := st.BlobReader(ctx, "docker.io/a/read", layerDgst)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(data) != "read-me" {
		t.Fatalf("blob = %q, err=%v", data, err)
	}
	if _, err := st.BlobReader(ctx, "docker.io/a/read", "sha256:"+strings.Repeat("0", 64)); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown blob = %v, want ErrNotFound", err)
	}
	tags, err := st.Tags(ctx, "docker.io/a/read")
	if err != nil || len(tags) != 1 || tags[0] != "1.0" {
		t.Fatalf("tags = %v, err=%v", tags, err)
	}
}
