// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/buildinfo"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// ManifestFormat is the media manifest layout version this build writes
// and reads. Like the store format (R-26) it is SemVer on a layout: a bump
// is a documented migration, never a silent reinterpretation.
const ManifestFormat = 1

// ManifestPath is where the manifest lives inside the store, in slash
// form. It is itself outside coverage: a file cannot inventory itself.
const ManifestPath = "meta/media.json"

// Covered roots, in slash form: the content and the bookkeeping. Anything
// else in the store — _tobby/ tasks and operation logs above all — is out
// of coverage by construction (FR-053, FR-054: the destination writes its
// return logs there, after this inventory was taken).
var coveredRoots = []string{"docker/registry/v2", "meta"}

// Manifest is the media manifest: meta/media.json.
//
// Unsigned by design (ADR-0006). Every field is a claim the destination
// side re-checks against the bytes actually present, except the two that
// cannot be re-checked and are not meant to be — the zone it is addressed
// to and the medium's identity — which are anti-accident guards, not
// security controls.
type Manifest struct {
	// MediaFormat is the version of this document's own layout.
	MediaFormat int `json:"mediaFormat"`
	// StoreFormat is the store layout version, copied from
	// meta/format.json so the medium names it without being opened.
	StoreFormat int `json:"storeFormat"`
	// MediaID identifies the physical medium (R-28): minted with the
	// store, stable across re-synchronizations onto it, different on a
	// fresh one.
	MediaID string `json:"mediaId"`
	// Zone is the identity of the destination zone — the served
	// Retriever's name.
	Zone string `json:"zone"`
	// ProducedBy names the Tobby release and the run that wrote this.
	ProducedBy Producer `json:"producedBy"`
	// ResolvedAt is when the run resolved its Retriever. This is the
	// freshness instant of R-28, and it is deliberately the Retriever's
	// resolution rather than the write: it dates the DELIVERY, not the
	// bookkeeping.
	ResolvedAt time.Time `json:"resolvedAt"`
	// WrittenAt is when this document was written.
	WrittenAt time.Time `json:"writtenAt"`
	// Recipes is what the medium delivers, derived from the store's own
	// recipe graph (meta/recipes.json) rather than recomputed beside it:
	// two sources of the same truth would drift.
	Recipes []Recipe `json:"recipes"`
	// Inventory lists every covered file. Sorted by path, so two runs over
	// the same content produce the same document.
	Inventory []File `json:"inventory"`
	// Totals summarizes the inventory.
	Totals Totals `json:"totals"`
}

// Producer names what wrote a manifest.
type Producer struct {
	// Version is the Tobby release ("dev" on unstamped builds).
	Version string `json:"version"`
	// RunID correlates the medium with the source-side run logs (R-09).
	RunID string `json:"runId,omitempty"`
}

// Recipe is one delivery carried by the medium.
type Recipe struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// CookbookRepo is the nominal cookbook repository — what the
	// destination's trust scopes match against (FR-036).
	CookbookRepo string `json:"cookbookRepo"`
	// ArtifactRepo and ArtifactTag locate the recipe artifact ON THE
	// MEDIUM (the relocated path, base prefix included). The nominal
	// repository above cannot be turned back into this one by a reader:
	// the base prefix belonged to the instance that produced the medium.
	ArtifactRepo string `json:"artifactRepo"`
	ArtifactTag  string `json:"artifactTag,omitempty"`
	// Digest is the recipe artifact's manifest digest — the signed bytes
	// (RECIPE-SPEC §12.2).
	Digest      string       `json:"digest"`
	ResolvedAt  time.Time    `json:"resolvedAt"`
	Ingredients []Ingredient `json:"ingredients"`
}

// Ingredient is one pinned ingredient of a delivered recipe.
type Ingredient struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Repo is the relocated repository the ingredient occupies on the
	// medium (FR-035).
	Repo   string `json:"repo"`
	Tag    string `json:"tag,omitempty"`
	Digest string `json:"digest"`
}

// File is one inventory entry: a path relative to the store root, in
// slash form — a manifest never carries a Windows separator, whichever
// platform wrote it.
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

// Totals is the volumetry of the inventory.
type Totals struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// MetaSource is the read surface the writer needs from an opened store.
// *store.Store implements it.
type MetaSource interface {
	// Root is the store directory — the medium itself (FR-050).
	Root() string
	// MediaID is the medium's identity (R-28).
	MediaID() string
	// RecipeRecords is the store's recipe graph: what the medium
	// delivers, and the reachability set every verdict is computed from.
	RecipeRecords() ([]store.RecipeRecord, error)
}

// WriteOptions parameterizes one manifest write.
type WriteOptions struct {
	// Zone is the identity of the zone the medium is addressed to (the
	// served Retriever's name).
	Zone string
	// RunID is the synchronization that produced this state (R-09).
	RunID string
	// ResolvedAt is when that run resolved its Retriever; zero means now.
	ResolvedAt time.Time
	// Progress receives the inventory walk's progress, if non-nil
	// (FR-054: verification progress is displayed — writing is the same
	// walk, and the same operator is watching it).
	Progress func(Progress)
}

// Write inventories the store and writes meta/media.json into it,
// atomically (NFR-010: temp file then rename, as every other meta/ ledger
// is written).
//
// It is called at the end of every mirror synchronization that produces
// the transportable store, after any prune (FR-054): the inventory has to
// describe what the medium finally holds, not what it held mid-run.
//
// The returned manifest is the document that was written.
func Write(ctx context.Context, src MetaSource, opts WriteOptions) (*Manifest, error) {
	root := src.Root()
	storeFormat, err := readStoreFormat(root)
	if err != nil {
		return nil, err
	}
	records, err := src.RecipeRecords()
	if err != nil {
		return nil, err
	}
	inventory, totals, err := inventoryOf(ctx, root, opts.Progress)
	if err != nil {
		return nil, err
	}
	resolvedAt := opts.ResolvedAt
	if resolvedAt.IsZero() {
		resolvedAt = time.Now().UTC()
	}

	m := &Manifest{
		MediaFormat: ManifestFormat,
		StoreFormat: storeFormat,
		MediaID:     src.MediaID(),
		Zone:        opts.Zone,
		ProducedBy:  Producer{Version: buildinfo.Version(), RunID: opts.RunID},
		ResolvedAt:  resolvedAt.UTC(),
		WrittenAt:   time.Now().UTC(),
		Recipes:     recipesOf(records),
		Inventory:   inventory,
		Totals:      totals,
	}
	if err := writeJSON(filepath.Join(root, filepath.FromSlash(ManifestPath)), m); err != nil {
		return nil, err
	}
	return m, nil
}

// recipesOf projects the store's recipe graph onto the manifest, sorted by
// name and version so the document is reproducible.
func recipesOf(records []store.RecipeRecord) []Recipe {
	out := make([]Recipe, 0, len(records))
	for i := range records {
		r := &records[i]
		rec := Recipe{
			Name: r.Name, Version: r.Version,
			CookbookRepo: r.CookbookRepo,
			ArtifactRepo: r.ArtifactRepo, ArtifactTag: r.ArtifactTag,
			Digest: r.Digest, ResolvedAt: r.ResolvedAt,
			Ingredients: make([]Ingredient, 0, len(r.Ingredients)),
		}
		for _, ing := range r.Ingredients {
			rec.Ingredients = append(rec.Ingredients, Ingredient{
				Name: ing.Name, Kind: ing.Kind, Repo: ing.Repo,
				Tag: ing.Tag, Digest: ing.Digest,
			})
		}
		sort.Slice(rec.Ingredients, func(a, b int) bool { return rec.Ingredients[a].Name < rec.Ingredients[b].Name })
		out = append(out, rec)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Name != out[b].Name {
			return out[a].Name < out[b].Name
		}
		return out[a].Version < out[b].Version
	})
	return out
}

// inventoryOf walks the covered roots and digests every regular file.
func inventoryOf(ctx context.Context, root string, progress func(Progress)) ([]File, Totals, error) {
	var (
		files  []File
		totals Totals
	)
	for _, sub := range coveredRoots {
		dir := filepath.Join(root, filepath.FromSlash(sub))
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			switch {
			case errors.Is(err, os.ErrNotExist):
				// A store that has synchronized nothing yet has no
				// content tree: an empty inventory is a legitimate one.
				return fs.SkipAll
			case err != nil:
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.IsDir() || !d.Type().IsRegular() {
				// Directories carry no bytes, and anything that is not a
				// regular file — a symlink, a socket left behind — is not
				// content Tobby put there. It stays out of the inventory
				// and the destination reports it as uncovered rather than
				// following it (NFR-011).
				return nil
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return rerr
			}
			slash := filepath.ToSlash(rel)
			if !covered(slash) {
				return nil
			}
			dgst, size, herr := hashFile(p)
			if herr != nil {
				return herr
			}
			files = append(files, File{Path: slash, Size: size, Digest: dgst})
			totals.Files++
			totals.Bytes += size
			report(progress, Progress{
				Stage: StageInventory, Files: totals.Files, Bytes: totals.Bytes,
			})
			return nil
		})
		if err != nil {
			return nil, Totals{}, fmt.Errorf("media: inventorying %s: %w", sub, err)
		}
	}
	sort.Slice(files, func(a, b int) bool { return files[a].Path < files[b].Path })
	return files, totals, nil
}

// covered reports whether a slash path inside the store belongs to the
// manifest's coverage. It is the single definition both sides use: the
// writer to decide what to inventory, the verifier to decide what an
// absent inventory entry is a finding about.
func covered(slash string) bool {
	if slash == ManifestPath {
		// A manifest cannot inventory itself.
		return false
	}
	if strings.HasPrefix(path.Base(slash), ".tmp-") {
		// The atomic-write temp files of meta/ (and any left behind by a
		// crash): transient by definition, and inventorying one would
		// make the manifest describe a file that is about to be renamed.
		return false
	}
	for _, sub := range coveredRoots {
		if slash == sub || strings.HasPrefix(slash, sub+"/") {
			return true
		}
	}
	return false
}

// hashFile streams one file and returns its "sha256:<hex>" digest and its
// size. Files here are blobs: they are never held whole in memory
// (NFR-007).
func hashFile(p string) (dgst string, size int64, err error) {
	f, err := os.Open(p) //nolint:gosec // G304: a path produced by our own walk of the store
	if err != nil {
		return "", 0, err
	}
	defer f.Close() //nolint:errcheck // read side
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

// readStoreFormat copies the store's own format version out of
// meta/format.json — the file, not this build's constant: the manifest
// must state what the medium says about itself.
func readStoreFormat(root string) (int, error) {
	p := filepath.Join(root, "meta", "format.json")
	raw, err := os.ReadFile(p) //nolint:gosec // G304: the store's own metadata file
	if err != nil {
		return 0, fmt.Errorf("media: reading the store format version: %w", err)
	}
	var f struct {
		StoreFormat int `json:"storeFormat"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, fmt.Errorf("media: parsing %s: %w", p, err)
	}
	return f.StoreFormat, nil
}

// writeJSON writes v atomically: temp file in the target directory, then
// rename (NFR-010, the same contract as the store's own meta/ ledgers — a
// crash never leaves a half-written manifest, and a destination never
// reads one).
func writeJSON(p string, v any) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return fmt.Errorf("media: creating %s: %w", filepath.Dir(p), err)
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("media: encoding %s: %w", p, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return fmt.Errorf("media: creating temp file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("media: writing %s: %w", p, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("media: flushing %s: %w", p, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("media: closing %s: %w", p, err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("media: replacing %s: %w", p, err)
	}
	return nil
}
