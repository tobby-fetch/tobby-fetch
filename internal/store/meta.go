// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/runid"
)

// Store metadata: the meta/ directory of the self-contained store
// (ADR-0006 layout: registry content, logs, meta). It holds the store
// format version (R-26), the provenance ledger (FR-044/FR-045: who put a
// repository there), and the recipe graph (which recipe manages which
// content — the reachability fact FR-044 GC, FR-045 prune, and the
// milestone-5 media verification all read).
//
// Every file is small JSON, written atomically (temp file + rename): a
// crash never leaves a half-written ledger (NFR-010). None of it is a
// trust anchor — authenticity always comes from recipe signatures and
// pinned digests (decision n°10) — losing meta/ loses bookkeeping, not
// integrity.

// FormatVersion is the store layout version this build writes and reads.
// The compatibility policy (R-26) is SemVer on the storage layout: within
// the 1.x series every release reads stores written by every other; a
// bump is a major event with a documented migration.
const FormatVersion = 1

// formatFile records the store format version in the store itself, so a
// transported store names its own version (the milestone-5 media manifest
// references it, FR-054).
type formatFile struct {
	StoreFormat int `json:"storeFormat"`
}

// checkFormat validates the store's format version, stamping fresh (or
// pre-versioning) stores with the current one. An unsupported version
// fails with both versions named and the standard-tooling escape route
// (R-26, FR-051).
func checkFormat(root string) error {
	path := filepath.Join(root, "meta", "format.json")
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the store's own metadata file
	switch {
	case errors.Is(err, os.ErrNotExist):
		return writeJSON(path, formatFile{StoreFormat: FormatVersion})
	case err != nil:
		return fmt.Errorf("store: reading format version: %w", err)
	}
	var f formatFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("store: parsing %s: %w", path, err)
	}
	if f.StoreFormat != FormatVersion {
		return fmt.Errorf(
			"store: format version %d is not supported by this build (it supports version %d): "+
				"use a Tobby release matching the store, or export/import the content with standard "+
				"OCI tooling (skopeo, oras, crane) through the OCI image layout (FR-051)",
			f.StoreFormat, FormatVersion)
	}
	return nil
}

// mediaIDFile records the identity of the physical medium this store is
// (FR-054 amendment R-28). It lives in meta/ rather than in the instance
// state directory on purpose, and it is the one identity that does: the
// store IS the medium, so the identifier has to travel with it — that is
// what lets a destination-side incident be traced back to a drawer and a
// label. Everything else about an instance stays behind (R-16).
type mediaIDFile struct {
	MediaID string `json:"mediaId"`
}

// ensureMediaID returns the store's media identifier, minting and
// persisting one the first time the directory is opened.
//
// Passthrough instances get one too, even though their store never
// travels: it costs one small file, and it gives the run logs of both
// modes the same correlation key (R-09). A store re-opened, re-synchronized
// or copied keeps its identifier — that is the "stable across subsequent
// synchronizations" of the amendment — while a freshly created directory
// gets a new one.
func ensureMediaID(root string) (string, error) {
	path := filepath.Join(root, "meta", "media-id.json")
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the store's own metadata file
	switch {
	case errors.Is(err, os.ErrNotExist):
		id := runid.NewMedia()
		if err := writeJSON(path, mediaIDFile{MediaID: id}); err != nil {
			return "", err
		}
		return id, nil
	case err != nil:
		return "", fmt.Errorf("store: reading media identifier: %w", err)
	}
	var f mediaIDFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return "", fmt.Errorf("store: parsing %s: %w", path, err)
	}
	if f.MediaID == "" {
		return "", fmt.Errorf("store: %s holds no media identifier", path)
	}
	return f.MediaID, nil
}

// MediaID returns the identity of the medium this store is (FR-054
// amendment R-28), minted at creation and stable for the store's life.
func (s *Store) MediaID() string { return s.mediaID }

// ProvenanceClass says how a repository entered the store (FR-045: the
// distinction decides prune eligibility; the FR-044 amendment decides
// admin removability).
type ProvenanceClass string

// The provenance classes. Repositories with no recorded provenance are
// ProvenanceSeed: content pushed through /v2/ by a standard client.
const (
	ProvenanceRecipe     ProvenanceClass = "recipe"
	ProvenanceUnitImport ProvenanceClass = "unit-import"
	ProvenanceSeed       ProvenanceClass = "seed"
)

// ProvenanceOrigin refines a class for content that entered by a local
// operation rather than over the network.
type ProvenanceOrigin string

// OriginLocalPack marks a FileSet packed on this host from a local file
// tree (FR-048). It is a unit import — FR-045 never prunes it, the
// FR-044 amendment makes it individually removable — but one that came
// from nowhere: no source registry, no signature (ADR-0007: Tobby signs
// nothing), no Retriever entry to bring it back. FR-048 requires it to
// be distinguishable from a Recipe-delivered FileSet in listings and
// reports, and this is the field that distinguishes it.
const OriginLocalPack ProvenanceOrigin = "local-pack"

// Provenance is the recorded origin of one repository.
type Provenance struct {
	Class ProvenanceClass `json:"class"`
	// Origin refines the class; empty for everything that arrived over
	// the network.
	Origin ProvenanceOrigin `json:"origin,omitempty"`
	// Recipe and RecipeVersion identify the managing recipe for
	// recipe-managed content.
	Recipe        string `json:"recipe,omitempty"`
	RecipeVersion string `json:"recipeVersion,omitempty"`
	// RunID correlates with the run that wrote the content (FR-090, R-09).
	RunID      string    `json:"runId,omitempty"`
	RecordedAt time.Time `json:"recordedAt"`
}

// provenanceFile is the meta/provenance.json ledger.
type provenanceFile struct {
	Repositories map[string]Provenance `json:"repositories"`
}

// IngredientRecord is one ingredient of a recorded recipe: where it landed
// and under which pinned digest.
type IngredientRecord struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Repo   string `json:"repo"`
	Tag    string `json:"tag,omitempty"`
	Digest string `json:"digest"`
}

// RecipeRecord is the stored recipe graph entry: the recipe artifact and
// the content it manages. Zone and ResolvedAt feed the milestone-5 media
// manifest (FR-054); the ingredient list is the reachability set of
// FR-044/FR-045.
type RecipeRecord struct {
	Name string `json:"name"`
	// Version is the recipe's metadata.version.
	Version string `json:"version"`
	// CookbookRepo is the NOMINAL cookbook repository the recipe came from
	// ("registry.example.com/cookbook/wordpress") — the space trust scopes
	// match, invariant across cascade hops (FR-036).
	CookbookRepo string `json:"cookbookRepo"`
	// ArtifactRepo is where the recipe artifact itself sits in THIS store:
	// the relocated cookbook path, base prefix included (FR-035). It is
	// not derivable from CookbookRepo by a reader of the store, because the
	// base prefix belongs to the instance that wrote it and not to the
	// medium — and destination-side media verification (FR-054) needs both
	// halves: the nominal one to pick the trust roots, this one to find
	// the artifact and its signature on the medium.
	ArtifactRepo string `json:"artifactRepo,omitempty"`
	// ArtifactTag is the tag the artifact carries under ArtifactRepo (the
	// resolved version tag, FR-021).
	ArtifactTag string    `json:"artifactTag,omitempty"`
	Digest      string    `json:"digest"`
	RunID       string    `json:"runId,omitempty"`
	Zone        string    `json:"zone,omitempty"`
	ResolvedAt  time.Time `json:"resolvedAt"`
	Verified    bool      `json:"verified"`
	// TrustScope names the declared relaxed scope that admitted the recipe
	// when Verified is false (never empty in that case — FR-033: no
	// undeclared bypass exists).
	TrustScope  string             `json:"trustScope,omitempty"`
	Ingredients []IngredientRecord `json:"ingredients"`
}

// recipesFile is the meta/recipes.json graph.
type recipesFile struct {
	Recipes map[string]RecipeRecord `json:"recipes"`
}

func recipeKey(name, version string) string { return name + "@" + version }

// SetProvenance records how repo entered the store. First write wins for
// class "recipe" over "unit-import"? No: the latest writer is the truth —
// a repo re-managed by a recipe upgrades its class, and a recipe removal
// downgrades nothing (the ledger entry goes away with the repository).
func (s *Store) SetProvenance(repo string, p *Provenance) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	var f provenanceFile
	if err := s.readMeta("provenance.json", &f); err != nil {
		return err
	}
	if f.Repositories == nil {
		f.Repositories = map[string]Provenance{}
	}
	entry := *p
	if entry.RecordedAt.IsZero() {
		entry.RecordedAt = time.Now().UTC()
	}
	f.Repositories[repo] = entry
	return writeJSON(filepath.Join(s.root, "meta", "provenance.json"), f)
}

// MarkManualImport records repo as a FileSet packed on this host from a
// local file tree (FR-048). It is a unit import — FR-045 never prunes
// one, and the FR-044 amendment keeps it individually removable — of
// local origin, which is what listings show to tell it apart from a
// Recipe-delivered FileSet.
func (s *Store) MarkManualImport(repo string) error {
	return s.SetProvenance(repo, &Provenance{Class: ProvenanceUnitImport, Origin: OriginLocalPack})
}

// ProvenanceOf returns the recorded origin of repo. Absence means seeded
// content (pushed through /v2/ by a standard client).
func (s *Store) ProvenanceOf(repo string) (Provenance, bool) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	var f provenanceFile
	if err := s.readMeta("provenance.json", &f); err != nil {
		return Provenance{}, false
	}
	p, ok := f.Repositories[repo]
	return p, ok
}

// dropProvenance forgets a deleted repository's entry.
func (s *Store) dropProvenance(repo string) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	var f provenanceFile
	if err := s.readMeta("provenance.json", &f); err != nil {
		return err
	}
	if _, ok := f.Repositories[repo]; !ok {
		return nil
	}
	delete(f.Repositories, repo)
	return writeJSON(filepath.Join(s.root, "meta", "provenance.json"), f)
}

// PutRecipeRecord stores (or replaces) one recipe graph entry.
func (s *Store) PutRecipeRecord(r *RecipeRecord) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	var f recipesFile
	if err := s.readMeta("recipes.json", &f); err != nil {
		return err
	}
	if f.Recipes == nil {
		f.Recipes = map[string]RecipeRecord{}
	}
	f.Recipes[recipeKey(r.Name, r.Version)] = *r
	return writeJSON(filepath.Join(s.root, "meta", "recipes.json"), f)
}

// RecipeRecords returns every stored recipe graph entry.
func (s *Store) RecipeRecords() ([]RecipeRecord, error) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	var f recipesFile
	if err := s.readMeta("recipes.json", &f); err != nil {
		return nil, err
	}
	out := make([]RecipeRecord, 0, len(f.Recipes))
	for key := range f.Recipes {
		out = append(out, f.Recipes[key])
	}
	return out, nil
}

// DeleteRecipeRecord forgets one recipe graph entry.
func (s *Store) DeleteRecipeRecord(name, version string) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	var f recipesFile
	if err := s.readMeta("recipes.json", &f); err != nil {
		return err
	}
	delete(f.Recipes, recipeKey(name, version))
	return writeJSON(filepath.Join(s.root, "meta", "recipes.json"), f)
}

// ManagingRecipes names the recipes whose recorded ingredient set includes
// repo — the FR-044 amendment's "managed by recipe X" explanation, and the
// guard refusing individual removal of recipe-managed content.
func (s *Store) ManagingRecipes(repo string) []string {
	records, err := s.RecipeRecords()
	if err != nil {
		return nil
	}
	var names []string
	for i := range records {
		r := &records[i]
		for _, ing := range r.Ingredients {
			if ing.Repo == repo {
				names = append(names, recipeKey(r.Name, r.Version))
				break
			}
		}
	}
	return names
}

// readMeta loads one meta/ JSON file into out; a missing file is an empty
// ledger, not an error.
func (s *Store) readMeta(name string, out any) error {
	raw, err := os.ReadFile(filepath.Join(s.root, "meta", name)) //nolint:gosec // G304: the store's own metadata
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: reading meta/%s: %w", name, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("store: parsing meta/%s: %w", name, err)
	}
	return nil
}

// writeJSON writes v atomically: temp file in the target directory, then
// rename (NFR-010: a crash never leaves a torn file).
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("store: creating %s: %w", filepath.Dir(path), err)
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encoding %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("store: creating temp file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("store: writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("store: closing %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("store: replacing %s: %w", path, err)
	}
	return nil
}
