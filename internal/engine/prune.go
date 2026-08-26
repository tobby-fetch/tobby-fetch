// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// Prune to the Retriever (FR-045, amendment 2026-08-11 R-33).
//
// The desired state is the resolved Retriever. Content this instance holds
// because a PREVIOUS Retriever asked for it, and which the current one no
// longer references, is what prune removes. Everything else is protected,
// and the protection is a positive test rather than a list of exclusions:
// only content whose recorded provenance is "recipe" is ever eligible.
//
// That one rule covers all three protected roots the requirement names.
// Unit imports (FR-023) carry provenance "unit-import". Content pushed
// through /v2/ by a standard client (UC3 seeding) carries no ledger entry
// at all, which the ledger reads as "seed". The offline vulnerability
// database (FR-032) arrives through one of those two doors and never
// through a recipe, so it is protected by construction rather than by a
// name this code would have to keep in step with milestone 6.
//
// In passthrough mode the whole thing is opt-in and off by default
// (sync.prune). A transit store is not a delivery unit: an operator asking
// for fresher content has not asked for older content to be deleted, and a
// reconciliation loop that quietly shrinks the store an air-gapped zone
// pulls from is the failure this default exists to prevent.

// Pruner is the store surface the prune needs on top of the write
// surface: the recipe graph it computes reachability from, the provenance
// ledger that decides eligibility, and the unlink-and-sweep mechanism.
type Pruner interface {
	RecipeRecords() ([]store.RecipeRecord, error)
	DeleteRecipeRecord(name, version string) error
	ProvenanceOf(repo string) (store.Provenance, bool)
	PruneTags(ctx context.Context, refs []store.TagRef, logger *slog.Logger) (store.PruneResult, error)
	// ManifestInfo sizes one candidate for the trigger-time confirmation
	// (FR-045: the list AND the total size of what would be removed). A
	// candidate that cannot be sized is listed with a zero size rather
	// than dropped — an item missing from the confirmation is worse than
	// one whose size is unknown.
	ManifestInfo(ctx context.Context, repo, tagOrDigest string) (*store.ManifestInfo, error)
}

// PruneItem is one row of the FR-045 trigger-time confirmation: what
// would go, which recipe brought it, and how much disk it accounts for.
type PruneItem struct {
	Repo   string
	Tag    string
	Digest string
	Recipe string
	Bytes  int64
}

// PruneProjection is what a prune would remove if it ran now — the list
// and the total size FR-045 requires the operator to see BEFORE the run,
// not after it.
//
// It is a projection, not a promise: it is computed against the Retriever
// as it resolves at this instant, and a cookbook that publishes a new
// version between the confirmation and the run changes the answer. That
// is why it is recomputed on every display rather than cached.
type PruneProjection struct {
	Items      []PruneItem
	TotalBytes int64
}

// SetPruneDefault states what a synchronization prunes when its trigger
// says nothing (FR-045).
//
// The two modes disagree on purpose. Mirror mode prunes by default and
// confirms at trigger time, because a mirror store IS the delivery unit
// and an operator standing in front of the trigger can see what would go.
// Passthrough mode defaults to whatever sync.prune says, which is off:
// nobody is standing in front of a reconciliation loop, and a transit
// store that quietly shrinks between two cycles is a store the zone below
// discovers has lost content.
//
// A setter for the same reason SetMeters is one: an engine that prunes
// nothing is a complete engine.
func (e *Engine) SetPruneDefault(enabled bool) { e.prune = enabled }

// PrunesByDefault reports what an unqualified trigger would do —
// surfaced by the retriever screen and its API mirror (FR-061), because a
// store that shrinks on a timer is a posture, not a detail.
func (e *Engine) PrunesByDefault() bool { return e.prune }

// ProjectPrune computes what a prune would remove right now: the FR-045
// trigger-time confirmation, with the list and the total size.
//
// It resolves the Retriever the way a run does — version expressions
// against their cookbooks — but fetches nothing. A Retriever that cannot
// be resolved yields an error rather than an empty projection: "nothing
// would be removed" and "I could not work out what would be removed" are
// opposite statements, and confirming a removal on the second one is how
// an operator deletes a zone's content by pressing a button that looked
// harmless.
func (e *Engine) ProjectPrune(ctx context.Context) (PruneProjection, error) {
	var out PruneProjection
	p, ok := e.store.(Pruner)
	if !ok {
		return out, nil
	}
	keys, err := e.resolveRetrieverKeys(ctx)
	if err != nil {
		return out, err
	}
	records, err := p.RecipeRecords()
	if err != nil {
		return out, err
	}
	stale, keep := splitRecords(records, keys)
	_, pruned := e.pruneCandidates(ctx, p, nil, stale, referencedTags(keep, e.base))
	for _, row := range pruned {
		item := PruneItem{Repo: row.Repo, Tag: row.Tag, Digest: row.Digest, Recipe: row.Recipe}
		if info, ierr := p.ManifestInfo(ctx, row.Repo, row.Tag); ierr == nil {
			item.Bytes = info.Size
		}
		out.TotalBytes += item.Bytes
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// resolveRetrieverKeys resolves every Retriever entry to the recipe graph
// key it would record, without fetching anything: the cookbook tag list
// and the FR-021 version expression are enough to know WHICH recipe a run
// would keep.
//
// Any entry that does not resolve fails the whole projection. A partial
// key set would mark live content as unreferenced, which is precisely the
// mistake the run itself refuses to make.
func (e *Engine) resolveRetrieverKeys(ctx context.Context) (map[string]bool, error) {
	retr, err := LoadRetriever(ctx, e.remotes, e.source)
	if err != nil {
		return nil, e.mapError(err, e.source)
	}
	keys := map[string]bool{}
	for i := range retr.Spec.Recipes {
		entry := &retr.Spec.Recipes[i]
		cookbookRef := entry.Cookbook
		if cookbookRef == "" {
			cookbookRef = retr.Spec.Cookbook
		}
		cb := NewCookbook(e.remotes, cookbookRef)
		versions, verr := cb.Versions(ctx, entry.Name)
		if verr != nil {
			return nil, e.mapError(verr, entry.Name)
		}
		tag, terr := ResolveVersion(entry.Version, versions)
		if terr != nil {
			return nil, e.mapError(terr, entry.Name)
		}
		keys[recipeKey(entry.Name, tag)] = true
	}
	return keys, nil
}

// resolvedRecipes accumulates what one run actually resolved: the recipe
// graph keys that survived, and whether every entry of the Retriever got
// that far.
//
// Completeness is load-bearing. A recipe whose cookbook was unreachable
// contributes no fresh graph entry, and its content would then look
// exactly like content dropped from the Retriever. Pruning on a partial
// run would delete, on the strength of a network failure, the very content
// the next zone is waiting for — so a run with any failed entry prunes
// nothing and says so.
// It is written from the ingredient goroutines as well as from the run
// loop, so it carries its own lock: the engine transfers ingredients in
// parallel (NFR-008), and a failure flag raced on is a failure flag that
// can be lost.
type resolvedRecipes struct {
	mu       sync.Mutex
	keys     map[string]bool
	complete bool
}

func newResolvedRecipes() *resolvedRecipes {
	return &resolvedRecipes{keys: map[string]bool{}, complete: true}
}

func (r *resolvedRecipes) keep(name, version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[recipeKey(name, version)] = true
}

// failed marks the run partial. Any failure counts, down to a single
// ingredient: an ingredient that did not arrive is absent from the graph
// entry this run writes, and content a live recipe still wants would then
// read as content nothing references.
func (r *resolvedRecipes) failed() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.complete = false
}

func (r *resolvedRecipes) snapshot() (keys map[string]bool, complete bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys = make(map[string]bool, len(r.keys))
	for k := range r.keys {
		keys[k] = true
	}
	return keys, r.complete
}

// recipeKey mirrors the store's graph key ("name@version"). Duplicated
// rather than exported from the store because it is a join key between two
// packages, and a join key that only one side can spell is a join key that
// drifts silently.
func recipeKey(name, version string) string { return name + "@" + version }

// pruneToRetriever removes the recipe-managed content no longer referenced
// by the resolved Retriever, records it on the task and in the run logs
// (FR-053), and returns what went.
func (e *Engine) pruneToRetriever(ctx context.Context, sink *taskSink, logger *slog.Logger, resolved *resolvedRecipes) {
	p, ok := e.store.(Pruner)
	if !ok {
		return
	}
	keys, complete := resolved.snapshot()
	if !complete {
		logger.LogAttrs(ctx, slog.LevelWarn, "prune skipped: the run did not resolve every Retriever entry",
			slog.String("reason", "content of an unresolved recipe is indistinguishable from content the Retriever dropped"),
			slog.String("requirement", "FR-045"))
		return
	}

	records, err := p.RecipeRecords()
	if err != nil {
		logger.LogAttrs(ctx, slog.LevelWarn, "prune skipped: the recipe graph could not be read",
			slog.String("error", err.Error()), slog.String("requirement", "FR-045"))
		return
	}

	stale, keep := splitRecords(records, keys)
	if len(stale) == 0 {
		return
	}
	refs, pruned := e.pruneCandidates(ctx, p, logger, stale, referencedTags(keep, e.base))

	// The graph entry goes whether or not it had exclusive content: it
	// describes a recipe the Retriever no longer asks for, and a stale
	// entry would make the next run compute reachability against a state
	// that no longer exists.
	for i := range stale {
		if err := p.DeleteRecipeRecord(stale[i].Name, stale[i].Version); err != nil {
			logger.LogAttrs(ctx, slog.LevelWarn, "pruned recipe record could not be removed",
				slog.String("recipe", recipeKey(stale[i].Name, stale[i].Version)),
				slog.String("error", err.Error()), slog.String("requirement", "FR-045"))
		}
	}

	if len(refs) == 0 {
		return
	}
	// Every removed item is named in the run log — the media log the store
	// carries with it (FR-053). A prune that reports only a count is a
	// prune nobody can audit after the medium has travelled.
	for i := range pruned {
		logger.LogAttrs(ctx, slog.LevelInfo, "content pruned",
			slog.String("repository", pruned[i].Repo), slog.String("tag", pruned[i].Tag),
			slog.String("digest", pruned[i].Digest), slog.String("recipe", pruned[i].Recipe),
			slog.String("requirement", "FR-045"))
	}
	res, err := p.PruneTags(ctx, refs, logger)
	if err != nil {
		logger.LogAttrs(ctx, slog.LevelWarn, "prune failed",
			slog.String("error", err.Error()), slog.String("requirement", "FR-045"))
		return
	}
	sink.update(func(t *tasks.Task) bool {
		// Rebuilt from scratch, like the resolution report: the rows
		// describe THIS run's removals.
		t.Pruned = pruned
		return true
	})
	logger.LogAttrs(ctx, slog.LevelInfo, "prune complete",
		slog.Int("items", len(pruned)),
		slog.Int("repositories_removed", res.Repositories),
		slog.Int("content_swept", res.ContentSwept),
		slog.Int("content_deferred", res.ContentDeferred),
		slog.String("requirement", "FR-045"))
}

// pruneCandidates turns the stale graph entries into the tags a prune
// would unlink and the rows it would report. One function, used by the run
// and by the trigger-time confirmation, because a confirmation computed by
// a second implementation is a confirmation that can disagree with what
// happens next.
//
// A nil logger means the projection path: it decides nothing differently,
// it only says nothing.
func (e *Engine) pruneCandidates(ctx context.Context, p Pruner, logger *slog.Logger, stale []store.RecipeRecord, kept map[store.TagRef]bool) (refs []store.TagRef, pruned []tasks.Pruned) {
	for i := range stale {
		rec := &stale[i]
		for _, cand := range candidatesOf(rec, e.base) {
			if kept[cand.ref] {
				// Still referenced by a recipe the Retriever asks for:
				// relocated repositories are shared, and reachability —
				// not the record that happened to be walked — decides.
				continue
			}
			if prov, _ := p.ProvenanceOf(cand.ref.Repo); prov.Class != store.ProvenanceRecipe {
				// A protected root: a unit import (FR-023), the offline
				// vulnerability database (FR-032), or content seeded
				// through /v2/ (UC3). Logged, because content that
				// survives a prune for a reason is not the same as
				// content nobody considered.
				if logger != nil {
					logger.LogAttrs(ctx, slog.LevelDebug, "prune candidate protected by its provenance",
						slog.String("repository", cand.ref.Repo), slog.String("tag", cand.ref.Tag),
						slog.String("provenance", string(prov.Class)),
						slog.String("requirement", "FR-045"))
				}
				continue
			}
			refs = append(refs, cand.ref)
			// The signature artifacts travel with the content (§12.2), so
			// they go with it: leaving them tagged would keep the manifest
			// reachable and the prune would remove nothing at all.
			refs = append(refs, signatureRefs(cand.ref.Repo, cand.digest)...)
			pruned = append(pruned, tasks.Pruned{
				Repo: cand.ref.Repo, Tag: cand.ref.Tag,
				Digest: cand.digest, Recipe: recipeKey(rec.Name, rec.Version),
			})
		}
	}
	return refs, pruned
}

// splitRecords separates the graph entries the run resolved from the ones
// it did not.
func splitRecords(records []store.RecipeRecord, keys map[string]bool) (stale, keep []store.RecipeRecord) {
	for i := range records {
		if keys[recipeKey(records[i].Name, records[i].Version)] {
			keep = append(keep, records[i])
			continue
		}
		stale = append(stale, records[i])
	}
	// Deterministic order: the run log of two identical prunes must read
	// identically, or a diff of two media logs is noise.
	sort.Slice(stale, func(i, j int) bool {
		return recipeKey(stale[i].Name, stale[i].Version) < recipeKey(stale[j].Name, stale[j].Version)
	})
	return stale, keep
}

// candidate is one (repository, tag) a stale record brought, with the
// digest whose signature artifacts travel with it.
type candidate struct {
	ref    store.TagRef
	digest string
}

// candidatesOf lists what one stale record put in the store: its
// ingredient trees (FR-035 relocated) and its own recipe artifact.
func candidatesOf(rec *store.RecipeRecord, base string) []candidate {
	out := make([]candidate, 0, len(rec.Ingredients)+1)
	for _, ing := range rec.Ingredients {
		if ing.Repo == "" || ing.Tag == "" {
			continue
		}
		out = append(out, candidate{ref: store.TagRef{Repo: ing.Repo, Tag: ing.Tag}, digest: ing.Digest})
	}
	if repo, err := relocate.PathWithBase(base, rec.CookbookRepo); err == nil && rec.Version != "" {
		out = append(out, candidate{ref: store.TagRef{Repo: repo, Tag: rec.Version}, digest: rec.Digest})
	}
	return out
}

// referencedTags is the set of tags the retained records still ask for.
func referencedTags(keep []store.RecipeRecord, base string) map[store.TagRef]bool {
	out := map[store.TagRef]bool{}
	for i := range keep {
		for _, cand := range candidatesOf(&keep[i], base) {
			out[cand.ref] = true
		}
	}
	return out
}

// signatureRefs names the tags under which a manifest's signature
// artifacts are stored: the tag-attached cosign layout of §12.2 and the
// referrers fallback index the bundle layout is found through (B-015).
// Both are ordinary tags, so both keep the manifest reachable, and a
// prune that left them behind would free nothing.
func signatureRefs(repo, digest string) []store.TagRef {
	if !strings.HasPrefix(digest, "sha256:") {
		return nil
	}
	return []store.TagRef{
		// The tag-attached layout, spelled by the one helper that spells
		// it everywhere else in this package.
		{Repo: repo, Tag: SignatureTag(digest)},
		// The referrers fallback index the bundle layout is found through.
		{Repo: repo, Tag: strings.Replace(digest, "sha256:", "sha256-", 1)},
	}
}
