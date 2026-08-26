// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Fixtures for the destination side of a physical transfer (FR-052).
//
// A medium is not fabricated here: it is PRODUCED, by running a real
// mirror synchronization from a real source registry onto a fresh store
// and writing the media manifest exactly as a source instance does. A
// hand-built fixture would agree with whatever the destination side
// expects, which is precisely the agreement under test.

// mediumRecipe describes one delivery to put on a medium.
type mediumRecipe struct {
	name    string
	version string
	// bundleSig signs in the Sigstore bundle layout (cosign 3.x default,
	// §12.2) instead of the classic attached tag. B-015 lived in the gap
	// between the two.
	bundleSig bool
}

// medium is a produced transportable store, with the identifiers a
// destination-side assertion needs.
type medium struct {
	st   *store.Store
	zone string
	// recipeDigest maps "name@version" onto the recipe artifact's
	// manifest digest — the signed bytes, and the subject of the
	// signature tags at the destination.
	recipeDigest map[string]string
	// ingredientDigest maps "name@version" onto its single ingredient's
	// pinned manifest digest.
	ingredientDigest map[string]string
	// resolvedAt is the medium's freshness instant (R-28).
	resolvedAt time.Time
}

func (m *medium) root() string { return m.st.Root() }

// servedZone is the zone every fixture medium is addressed to. The
// mismatch cases vary the zone the DESTINATION INSTANCE claims to serve,
// not the one the medium was produced for: that is the direction the
// FR-054 guard runs in.
const servedZone = "zone-alpha"

// seedMedium produces a transportable store for servedZone, carrying
// recipes signed by kp, and writes its media manifest (FR-054).
func seedMedium(t *testing.T, kp *sigtest.KeyPair, recipes ...mediumRecipe) *medium {
	zone := servedZone
	t.Helper()
	src := newRegistry(t)
	m := &medium{
		zone:             zone,
		recipeDigest:     map[string]string{},
		ingredientDigest: map[string]string{},
	}
	entries := make([]spec.RecipeSelector, 0, len(recipes))
	for _, r := range recipes {
		imgRepo := "docker.io/library/" + r.name
		imgDig := seedImage(t, src, imgRepo, r.version)
		yaml := cookedRecipeYAML(t, r.name, r.version, []spec.Ingredient{{
			Name: "app", Kind: spec.IngredientContainerImage,
			Ref: imgRepo, Version: r.version, Digest: imgDig,
		}})
		cookbookRepo := "docker.io/cookbook/" + r.name
		manDig := publishRecipe(t, src.st, cookbookRepo, r.version, yaml)
		if r.bundleSig {
			signManifestBundle(t, src.st, cookbookRepo, manDig, kp)
		} else {
			signManifest(t, src.st, cookbookRepo, manDig, kp)
		}
		id := r.name + "@" + r.version
		m.recipeDigest[id] = manDig
		m.ingredientDigest[id] = imgDig
		entries = append(entries, spec.RecipeSelector{Name: r.name, Version: r.version})
	}

	retr := retrieverFile(t, zone, "docker.io/cookbook", entries)
	m.st = openStore(t)
	eng := New(m.st, newRemotes(t, map[string]string{"docker.io": src.addr}),
		trustFor(t, nil, kp), retr, "", syncCfg())
	eng.SetMediaManifest(func(ctx context.Context, z, runID string, resolvedAt time.Time) error {
		m.resolvedAt = resolvedAt
		_, err := media.Write(ctx, m.st, media.WriteOptions{Zone: z, RunID: runID, ResolvedAt: resolvedAt})
		return err
	})
	task, err := runSync(t, eng)
	if err != nil {
		t.Fatalf("producing the medium: %v", err)
	}
	if agg := task.Aggregate(); agg.Failed != 0 {
		t.Fatalf("producing the medium: %+v (items %s)", agg, itemNames(task))
	}
	return m
}

// destinationFor builds the destination-side engine reading the medium:
// this instance's own trust roots, its own zone identity, its own
// freshness register in a state directory OUTSIDE the medium (R-28).
func destinationFor(t *testing.T, m *medium, dest *destRegistry, zone string, roots ...*sigtest.KeyPair) (*Engine, *media.Imports) {
	t.Helper()
	imports, err := media.OpenImports(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng := New(m.st, newRemotes(t, nil), trustFor(t, nil, roots...), "", "", syncCfg())
	eng.SetMediaImport(zone, imports)
	if dest != nil {
		withDestination(t, eng, dest, config.Destination{}, nil)
	}
	return eng, imports
}

// runImport executes one media import on a fabricated task and returns it.
func runImport(t *testing.T, eng *Engine, opts MediaOptions) (*tasks.Task, error) {
	t.Helper()
	task := &tasks.Task{
		ID: "tsk_media", RunID: "run_media", Type: tasks.TypeMediaImport,
		Status: tasks.StatusRunning,
	}
	err := eng.importMedia(context.Background(), newTaskSink(task, func() {}), discardLogger(), opts)
	return task, err
}

// rewriteResolvedAt re-dates a medium by rewriting its manifest through
// the writer that produced it: a hand-edited timestamp would leave the
// document consistent with nothing, and the guard under test is about
// dates, not about a manifest that no longer parses.
func rewriteResolvedAt(t *testing.T, m *medium, when time.Time) {
	t.Helper()
	if _, err := media.Write(context.Background(), m.st, media.WriteOptions{
		Zone: m.zone, RunID: "run_redated", ResolvedAt: when,
	}); err != nil {
		t.Fatalf("re-dating the medium: %v", err)
	}
	m.resolvedAt = when.UTC()
}

// isCode reports whether err is the taxonomy error with this code.
func isCode(err error, code taxonomy.Code) bool {
	var te *taxonomy.Error
	return errors.As(err, &te) && te.Code() == code
}

// blobDataPath is where the registry backend keeps the bytes of one
// digest, under the medium's root.
func blobDataPath(root, dgst string) string {
	h := strings.TrimPrefix(dgst, "sha256:")
	return filepath.Join(root, "docker", "registry", "v2", "blobs", "sha256", h[:2], h, "data")
}

// corruptBlob flips one byte of a stored blob, keeping its size: the
// medium then fails on the DIGEST rather than on the size, which is the
// FR-054 acceptance ("corrupting any covered file is detected and blocks
// the push, naming the file").
func corruptBlob(t *testing.T, root, dgst string) string {
	t.Helper()
	path := blobDataPath(root, dgst)
	raw, err := os.ReadFile(path) //nolint:gosec // G304: a path this test just built
	if err != nil {
		t.Fatalf("reading the blob to corrupt: %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("blob %s is empty: corrupting it would prove nothing", dgst)
	}
	raw[len(raw)/2] ^= 0xff
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// dropInventoryEntry removes one file from the media manifest's
// inventory, leaving the file itself intact on the medium.
//
// It is the damage a corrupted blob cannot simulate. Content that is
// present and correct but that the manifest does not vouch for would push
// perfectly well; the only thing stopping it is the FR-054 rule that a
// recipe reaching an uninventoried file is blocked. A test built on a
// corrupted blob cannot tell that rule from the push simply failing on
// bytes it could not read.
//
// The manifest is deliberately editable this way: it is the one file
// outside its own coverage.
func dropInventoryEntry(t *testing.T, root, slashPath string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(media.ManifestPath))
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the medium's own manifest
	if err != nil {
		t.Fatal(err)
	}
	var m media.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	kept := m.Inventory[:0]
	var dropped bool
	for _, e := range m.Inventory {
		if e.Path == slashPath {
			dropped = true
			continue
		}
		kept = append(kept, e)
	}
	if !dropped {
		t.Fatalf("the inventory does not list %s, so dropping it proves nothing", slashPath)
	}
	m.Inventory = kept
	m.Totals.Files = len(kept)
	var bytes int64
	for _, e := range kept {
		bytes += e.Size
	}
	m.Totals.Bytes = bytes
	out, err := json.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// blobInventoryPath is a blob's path as the inventory spells it.
func blobInventoryPath(dgst string) string {
	h := strings.TrimPrefix(dgst, "sha256:")
	return "docker/registry/v2/blobs/sha256/" + h[:2] + "/" + h + "/data"
}

// plantOnMedium writes a file into the transported store at a slash path,
// the way a hostile or careless producer would.
func plantOnMedium(t *testing.T, root, slashPath string, content []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(slashPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

// coveredFingerprint hashes every file under the media manifest's
// coverage, so a test can assert that an operation wrote NOTHING to the
// medium — the "any local write" half of the FR-054 order, which a
// request log cannot see.
func coveredFingerprint(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, sub := range []string{"docker", "meta"} {
		base := filepath.Join(root, sub)
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			switch {
			case os.IsNotExist(err):
				return fs.SkipAll
			case err != nil:
				return err
			case d.IsDir():
				return nil
			}
			f, oerr := os.Open(path) //nolint:gosec // G304: a path from our own walk
			if oerr != nil {
				return oerr
			}
			defer f.Close() //nolint:errcheck // read side
			h := sha256.New()
			if _, cerr := io.Copy(h, f); cerr != nil {
				return cerr
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			out[filepath.ToSlash(rel)] = hex.EncodeToString(h.Sum(nil))
			return nil
		})
		if err != nil {
			t.Fatalf("fingerprinting %s: %v", base, err)
		}
	}
	return out
}

// diffFingerprints names what changed between two fingerprints, sorted.
func diffFingerprints(before, after map[string]string) []string {
	var changed []string
	for path, sum := range after {
		if before[path] != sum {
			changed = append(changed, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			changed = append(changed, "removed "+path)
		}
	}
	sort.Strings(changed)
	return changed
}

// destTags lists what the destination registry holds under a repository.
func destTags(t *testing.T, d *destRegistry, repo string) []string {
	t.Helper()
	tags, err := d.st.Tags(context.Background(), repo)
	if err != nil {
		t.Fatalf("listing %s on the destination: %v", repo, err)
	}
	sort.Strings(tags)
	return tags
}
