// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// TestManifestCoversContentAndBookkeeping locks the coverage rule: the
// content tree and the store's ledgers, the manifest never itself, and
// nothing under _tobby/ — which is where the destination writes its return
// logs (FR-053, FR-054).
func TestManifestCoversContentAndBookkeeping(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", "1.0.0", true)

	// A task file, in the area FR-054 keeps outside coverage.
	taskDir := f.path("_tobby/tasks")
	if err := os.MkdirAll(taskDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "task.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := f.write(time.Now().UTC())

	if m.MediaFormat != media.ManifestFormat || m.StoreFormat != store.FormatVersion {
		t.Errorf("format versions = media %d / store %d", m.MediaFormat, m.StoreFormat)
	}
	if m.Zone != zoneA || m.MediaID == "" || m.ProducedBy.RunID == "" || m.ProducedBy.Version == "" {
		t.Errorf("manifest header is incomplete: %+v", m)
	}

	var sawContent, sawMeta bool
	for _, e := range m.Inventory {
		switch {
		case e.Path == media.ManifestPath:
			t.Error("the manifest inventories itself")
		case strings.HasPrefix(e.Path, "_tobby/"):
			t.Errorf("%s is inside coverage; _tobby/ must stay outside it", e.Path)
		case strings.HasPrefix(e.Path, "docker/registry/v2/"):
			sawContent = true
		case strings.HasPrefix(e.Path, "meta/"):
			sawMeta = true
		default:
			t.Errorf("%s is neither content nor bookkeeping", e.Path)
		}
		if strings.Contains(e.Path, `\`) {
			t.Errorf("%s carries a Windows separator", e.Path)
		}
	}
	if !sawContent || !sawMeta {
		t.Errorf("coverage missed a root: content=%v meta=%v", sawContent, sawMeta)
	}
	for _, want := range []string{"meta/format.json", "meta/recipes.json", "meta/media-id.json"} {
		if !inventoried(m, want) {
			t.Errorf("%s is not inventoried", want)
		}
	}
}

// TestInventoryDigestsAreTheRealBytes: the inventory is only worth
// something if its digests are computed from the files, not from what the
// store believed it wrote.
func TestInventoryDigestsAreTheRealBytes(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", "1.0.0", true)
	m := f.write(time.Now().UTC())

	var total int64
	for _, e := range m.Inventory {
		raw, err := os.ReadFile(f.path(e.Path))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Path, err)
		}
		sum := sha256.Sum256(raw)
		if want := "sha256:" + hex.EncodeToString(sum[:]); e.Digest != want {
			t.Errorf("%s: manifest says %s, bytes say %s", e.Path, e.Digest, want)
		}
		if int64(len(raw)) != e.Size {
			t.Errorf("%s: manifest says %d bytes, file holds %d", e.Path, e.Size, len(raw))
		}
		total += e.Size
	}
	if m.Totals.Files != len(m.Inventory) || m.Totals.Bytes != total {
		t.Errorf("totals = %+v, want %d files / %d bytes", m.Totals, len(m.Inventory), total)
	}
}

// TestManifestIsSortedAndReproducible: two writes over the same content
// produce the same document, so a diff between two media means a
// difference in content and not in walk order.
func TestManifestIsSortedAndReproducible(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", "1.0.0", true)
	f.addRecipe("beta", "2.0.0", true)
	at := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)

	first := f.write(at)
	if !sort.SliceIsSorted(first.Inventory, func(i, j int) bool {
		return first.Inventory[i].Path < first.Inventory[j].Path
	}) {
		t.Error("the inventory is not sorted by path")
	}
	if len(first.Recipes) != 2 || first.Recipes[0].Name != "alpha" {
		t.Errorf("recipes are not sorted by name: %+v", first.Recipes)
	}

	second := f.write(at)
	// The second write inventories the first manifest's siblings again;
	// only the manifest itself changed, and it is outside coverage.
	if len(first.Inventory) != len(second.Inventory) {
		t.Fatalf("inventory size changed between two identical writes: %d then %d",
			len(first.Inventory), len(second.Inventory))
	}
	for i := range first.Inventory {
		if first.Inventory[i] != second.Inventory[i] {
			t.Fatalf("entry %d changed: %+v then %+v", i, first.Inventory[i], second.Inventory[i])
		}
	}
}

// TestManifestCarriesTheRecipeGraph: the recipes section is a projection
// of meta/recipes.json, ingredients included — the destination reads its
// reachability set from there.
func TestManifestCarriesTheRecipeGraph(t *testing.T) {
	f := newFixture(t)
	rec := f.addRecipe("alpha", "1.0.0", true)
	m := f.write(time.Now().UTC())

	if len(m.Recipes) != 1 {
		t.Fatalf("manifest carries %d recipes, want 1", len(m.Recipes))
	}
	got := m.Recipes[0]
	if got.Name != rec.Name || got.Version != rec.Version || got.Digest != rec.Digest {
		t.Errorf("recipe identity lost: %+v", got)
	}
	if got.ArtifactRepo != rec.ArtifactRepo || got.ArtifactTag != rec.ArtifactTag {
		t.Errorf("the artifact's location on the medium is missing: %+v", got)
	}
	if len(got.Ingredients) != 1 || got.Ingredients[0].Digest != rec.Ingredients[0].Digest {
		t.Errorf("pinned ingredients lost: %+v", got.Ingredients)
	}
}

// TestManifestIsWrittenAtomically: a reader never sees a half-written
// manifest (NFR-010), and no temp file survives the write.
func TestManifestIsWrittenAtomically(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", "1.0.0", true)
	f.write(time.Now().UTC())

	entries, err := os.ReadDir(f.path("meta"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("a temp file survived the write: %s", e.Name())
		}
	}
	raw, err := os.ReadFile(f.path(media.ManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	var m media.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("the written manifest does not parse: %v", err)
	}
}

// TestMediaIDIsStableAcrossReopens is the R-28 acceptance: the same store
// keeps its identity, a fresh one gets a different one.
func TestMediaIDIsStableAcrossReopens(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(io.Discard, slog.LevelError)

	first, err := store.Open(context.Background(), root, logger)
	if err != nil {
		t.Fatal(err)
	}
	id := first.MediaID()
	if id == "" {
		t.Fatal("a freshly created store has no media identifier")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := store.Open(context.Background(), root, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close() //nolint:errcheck // test teardown
	if again.MediaID() != id {
		t.Errorf("re-opening the same store changed its identity: %q then %q", id, again.MediaID())
	}

	other, err := store.Open(context.Background(), t.TempDir(), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close() //nolint:errcheck // test teardown
	if other.MediaID() == id {
		t.Error("a brand-new store was given the identity of another medium")
	}
}

// TestWriteReportsProgress: the writer walks the same bytes the verifier
// does, and the same operator is watching.
func TestWriteReportsProgress(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", "1.0.0", true)

	seen := 0
	m, err := media.Write(context.Background(), f.st, media.WriteOptions{
		Zone: zoneA, RunID: "run", Progress: func(p media.Progress) {
			if p.Stage != media.StageInventory {
				t.Errorf("stage = %q during a write, want %q", p.Stage, media.StageInventory)
			}
			seen = p.Files
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != m.Totals.Files {
		t.Errorf("progress stopped at %d files, manifest counts %d", seen, m.Totals.Files)
	}
}

// TestWriteDefaultsResolvedAt: an unset resolution instant is "now", never
// the zero time — a zero timestamp would make every medium look ancient to
// the freshness guard.
func TestWriteDefaultsResolvedAt(t *testing.T) {
	f := newFixture(t)
	before := time.Now().UTC().Add(-time.Second)
	m, err := media.Write(context.Background(), f.st, media.WriteOptions{Zone: zoneA})
	if err != nil {
		t.Fatal(err)
	}
	if m.ResolvedAt.Before(before) {
		t.Errorf("resolvedAt = %s, want approximately now", m.ResolvedAt)
	}
}

func inventoried(m *media.Manifest, path string) bool {
	for _, e := range m.Inventory {
		if e.Path == path {
			return true
		}
	}
	return false
}
