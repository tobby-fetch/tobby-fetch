// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"context"
	"path/filepath"
	"sort"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/assurance"
)

// Provenance, content by content (FR-098, R-17). The ledger of meta.go
// records how a repository entered the store; this file turns it into the
// one classification every surface shows — the content browser, the API,
// the FileSet inventory, the log field and the unmanaged-content metric —
// so that none of them can answer differently.
//
// The rule, in order:
//
//  1. A repository that a recorded recipe lists among its ingredients, or
//     whose recipe artifact sits there, is class recipe, managed by those
//     recipes. The live recipe graph wins over the ledger: it is what the
//     FR-044 removal guard and the FR-045 prune read too.
//  2. Otherwise the ledger's class, as recorded: recipe (a stale entry of a
//     recipe no longer recorded, named from the entry itself), unit-import
//     (with its origin: local-pack for a packed FileSet, oci-layout for an
//     OCI layout import), or seed.
//  3. A repository with no entry at all is seed: content pushed through
//     /v2/ by a standard client, which stores written before R-17 never
//     recorded.
//
// The class is a fact about THIS store. It is never written into any
// registry, never carried by a medium as a claim, and decides nothing
// about trust: authenticity still comes from recipe signatures and pinned
// digests alone (decision n°10).

// Classification is the provenance of one repository as every surface
// shows it.
type Classification struct {
	Class ProvenanceClass
	// Origin refines unit-import: OriginLocalPack for a FileSet packed on
	// this host (FR-048), OriginOCILayout for an OCI layout import
	// (FR-051).
	Origin ProvenanceOrigin
	// Recipes names the managing recipes ("name@version", sorted) for
	// class recipe.
	Recipes []string
	// Assurance is the reduced-assurance marker (FR-037) when a managing
	// recipe was admitted under a relaxed trust scope; nil is full
	// assurance — or no recipe at all.
	Assurance *assurance.Marker
}

// Unmanaged reports whether the repository is content no recipe manages:
// the population the tobby_store_unmanaged_repositories metric counts.
func (c *Classification) Unmanaged() bool { return c.Class != ProvenanceRecipe }

// Classifier classifies repositories against one snapshot of the two
// ledgers: a listing page classifies dozens of repositories, and reading
// the ledgers once per row would be dozens of file reads for one answer.
type Classifier struct {
	ledger  map[string]Provenance
	managed map[string][]int
	records []RecipeRecord
	byKey   map[string]int
}

// Classifier reads the provenance ledger and the recipe graph once.
func (s *Store) Classifier() (*Classifier, error) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	return s.classifierLocked()
}

func (s *Store) classifierLocked() (*Classifier, error) {
	var pf provenanceFile
	if err := s.readMeta("provenance.json", &pf); err != nil {
		return nil, err
	}
	var rf recipesFile
	if err := s.readMeta("recipes.json", &rf); err != nil {
		return nil, err
	}
	c := &Classifier{ledger: pf.Repositories, managed: map[string][]int{}, byKey: map[string]int{}}
	keys := make([]string, 0, len(rf.Recipes))
	for k := range rf.Recipes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		rec := rf.Recipes[k]
		rec.Assurance = assurance.ForAdmission(rec.Verified, rec.TrustScope)
		idx := len(c.records)
		c.records = append(c.records, rec)
		c.byKey[recipeKey(rec.Name, rec.Version)] = idx
		seen := map[string]bool{}
		for _, ing := range rec.Ingredients {
			if !seen[ing.Repo] {
				seen[ing.Repo] = true
				c.managed[ing.Repo] = append(c.managed[ing.Repo], idx)
			}
		}
		if rec.ArtifactRepo != "" && !seen[rec.ArtifactRepo] {
			c.managed[rec.ArtifactRepo] = append(c.managed[rec.ArtifactRepo], idx)
		}
	}
	return c, nil
}

// Classify applies the rule of this file to one repository.
func (c *Classifier) Classify(repo string) Classification {
	if idxs := c.managed[repo]; len(idxs) > 0 {
		out := Classification{Class: ProvenanceRecipe}
		for _, i := range idxs {
			r := &c.records[i]
			out.Recipes = append(out.Recipes, recipeKey(r.Name, r.Version))
			if out.Assurance == nil && r.Assurance.Reduced() {
				out.Assurance = r.Assurance
			}
		}
		return out
	}
	p, ok := c.ledger[repo]
	if !ok {
		return Classification{Class: ProvenanceSeed}
	}
	switch p.Class {
	case ProvenanceRecipe:
		out := Classification{Class: ProvenanceRecipe}
		if p.Recipe != "" {
			key := recipeKey(p.Recipe, p.RecipeVersion)
			out.Recipes = []string{key}
			if i, ok := c.byKey[key]; ok {
				out.Assurance = c.records[i].Assurance
			}
		}
		return out
	case ProvenanceUnitImport:
		return Classification{Class: ProvenanceUnitImport, Origin: p.Origin}
	default:
		return Classification{Class: ProvenanceSeed}
	}
}

// Classify classifies one repository against the current ledgers. A
// ledger that cannot be read classifies as seed — the class that is
// neither removable (FR-044 amendment) nor prunable (FR-045), so a read
// failure can never make content easier to delete.
func (s *Store) Classify(repo string) Classification {
	c, err := s.Classifier()
	if err != nil {
		return Classification{Class: ProvenanceSeed}
	}
	return c.Classify(repo)
}

// RecordSeed records repo as seeded content — pushed through /v2/ by a
// standard client (UC3) — unless the ledger already knows better. It is
// called after every manifest a client pushes, so it must be cheap and
// must never downgrade: an existing unit-import or seed entry stays, a
// recipe entry stays while a recorded recipe still manages the
// repository. Only a missing entry, or the stale entry of a recipe no
// longer recorded, is written. Reports whether it wrote.
func (s *Store) RecordSeed(repo string) (bool, error) {
	return s.recordIfUnknown(repo, &Provenance{Class: ProvenanceSeed})
}

// RecordUnitImport records repo as a unit import of network origin
// (FR-023) under the same never-downgrade rule as RecordSeed: a unit
// import into a repository already holding seeded content keeps it
// seeded, so the pushed tags do not become removable along with it.
func (s *Store) RecordUnitImport(repo string) (bool, error) {
	return s.recordIfUnknown(repo, &Provenance{Class: ProvenanceUnitImport})
}

// RecordUnitImportFrom records repo as a unit import of the given local
// origin (OriginOCILayout for an OCI layout import), under the same
// never-downgrade rule as RecordUnitImport.
func (s *Store) RecordUnitImportFrom(repo string, origin ProvenanceOrigin) (bool, error) {
	return s.recordIfUnknown(repo, &Provenance{Class: ProvenanceUnitImport, Origin: origin})
}

func (s *Store) recordIfUnknown(repo string, p *Provenance) (bool, error) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	c, err := s.classifierLocked()
	if err != nil {
		return false, err
	}
	if prev, ok := c.ledger[repo]; ok {
		stale := prev.Class == ProvenanceRecipe && len(c.managed[repo]) == 0
		if !stale {
			return false, nil
		}
	}
	if len(c.managed[repo]) > 0 {
		// A recorded recipe manages it: the recipe sync records the
		// class itself (engine), and the live graph wins anyway.
		return false, nil
	}
	f := provenanceFile{Repositories: c.ledger}
	if f.Repositories == nil {
		f.Repositories = map[string]Provenance{}
	}
	entry := *p
	entry.RecordedAt = time.Now().UTC()
	f.Repositories[repo] = entry
	return true, writeJSON(filepath.Join(s.root, "meta", "provenance.json"), f)
}

// UnmanagedRepositories counts the repositories no recipe manages, by
// class (unit-import, seed): the FR-098 unmanaged-content metric. Both
// classes are always present, at zero when the store holds none.
func (s *Store) UnmanagedRepositories(ctx context.Context) (map[ProvenanceClass]int, error) {
	repos, err := s.Repositories(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.Classifier()
	if err != nil {
		return nil, err
	}
	out := map[ProvenanceClass]int{ProvenanceUnitImport: 0, ProvenanceSeed: 0}
	for _, r := range repos {
		if cl := c.Classify(r); cl.Unmanaged() {
			out[cl.Class]++
		}
	}
	return out, nil
}
