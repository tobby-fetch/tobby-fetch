// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/remote"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/importer"
	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// MetaStore extends the write surface with the bookkeeping the engine
// maintains: provenance (FR-045 groundwork) and the recipe graph
// (FR-044 reachability), implemented by the embedded store.
type MetaStore interface {
	Store
	StoreReader
	SetProvenance(repo string, p *store.Provenance) error
	PutRecipeRecord(r *store.RecipeRecord) error
}

// Meters are the engine's observability hooks (NFR-008: the parallelism
// bound is visible in metrics; FR-091). Nil funcs are skipped — the
// engine never depends on the metrics library.
type Meters struct {
	TransferStarted func()
	TransferDone    func()
	BytesMoved      func(int64)

	// Promotion counters (FR-013, FR-028, FR-091). PushSkipped is the one
	// worth watching on a continuous promotion service: a healthy steady
	// state is almost entirely skips, and a sudden run of PushDone without
	// a matching change upstream is how a destination that keeps losing
	// content announces itself.
	PushDone    func()
	PushSkipped func()
	PushedBytes func(int64)
	PushRefused func(code string)
}

// Engine turns a Retriever into verified content in the local store: the
// milestone-3 core. One Engine serves the whole instance; each triggered
// synchronization is a tracked task (FR-014).
type Engine struct {
	store   MetaStore
	remotes *Remotes
	trust   *TrustPolicy
	dest    *Destination
	source  string
	base    string
	cfg     config.Sync
	meters  Meters
}

// New assembles the engine.
func New(st MetaStore, remotes *Remotes, trust *TrustPolicy, retrieverSource, basePrefix string, cfg config.Sync) *Engine {
	return &Engine{store: st, remotes: remotes, trust: trust, source: retrieverSource, base: basePrefix, cfg: cfg}
}

// SetMeters installs the observability hooks.
func (e *Engine) SetMeters(m Meters) { e.meters = m }

// SetDestination installs the promotion target (FR-013). A nil
// destination is the default and means this instance fetches into its own
// store and stops there — the mirror-mode behaviour, and the passthrough
// behaviour before a destination is configured.
//
// It is a setter rather than a constructor argument for the same reason
// SetMeters is: an engine without a destination is a complete engine, and
// every existing caller that never promotes should keep reading as one
// that never promotes.
func (e *Engine) SetDestination(d *Destination) { e.dest = d }

// Destination reports the configured promotion target, for the surfaces
// that show where this instance pushes to (FR-035 mapping, FR-065).
func (e *Engine) Destination() *Destination { return e.dest }

// Source reports the configured retriever source (FR-010: shown in the
// UI and the API).
func (e *Engine) Source() string { return e.source }

// RelaxedScopes surfaces the declared allowUnsigned scopes (banner).
func (e *Engine) RelaxedScopes() []string { return e.trust.RelaxedScopes() }

// Runner returns the task runner for tasks.TypeSync. The runner works on
// its own task clone; save() persists and publishes progress.
func (e *Engine) Runner() tasks.Runner {
	return func(ctx context.Context, t *tasks.Task, logger *slog.Logger, save func()) error {
		return e.run(ctx, t, logger, save)
	}
}

// run is one synchronization: load and validate the Retriever, resolve
// every entry from its cookbook, verify, fetch, record. Failures are
// isolated per item (§12.3 point 4: fail closed, per item, reported);
// only a Retriever that cannot be loaded fails the task as a whole.
func (e *Engine) run(ctx context.Context, t *tasks.Task, logger *slog.Logger, save func()) error {
	retr, err := LoadRetriever(ctx, e.remotes, e.source)
	if err != nil {
		return e.mapError(err, e.source)
	}
	zone := retr.Metadata.Name
	logger.LogAttrs(ctx, slog.LevelInfo, "retriever loaded",
		slog.String("source", e.source), slog.String("zone", zone),
		slog.Int("recipes", len(retr.Spec.Recipes)))
	t.Resolutions = nil

	for i := range retr.Spec.Recipes {
		entry := &retr.Spec.Recipes[i]
		cookbookRef := entry.Cookbook
		if cookbookRef == "" {
			cookbookRef = retr.Spec.Cookbook
		}
		e.syncRecipe(ctx, t, logger, save, cookbookRef, entry, zone)
		save()
	}
	return nil
}

// syncRecipe resolves, verifies and fetches one Retriever entry. Failures
// land on the entry's items; other entries continue.
func (e *Engine) syncRecipe(ctx context.Context, t *tasks.Task, logger *slog.Logger, save func(), cookbookRef string, entry *spec.RecipeSelector, zone string) {
	logger = logger.With(slog.String("recipe", entry.Name))
	cb := NewCookbook(e.remotes, cookbookRef)

	fail := func(itemName string, err error) {
		item := itemFor(t, itemName)
		item.Status = tasks.StatusFailed
		item.Error = tasks.FromTaxonomy(e.mapError(err, entry.Name))
		logger.LogAttrs(ctx, slog.LevelWarn, "recipe entry failed",
			slog.String("item", itemName), slog.String("error", err.Error()))
		save()
	}

	// FR-021: resolve the entry's version expression against the cookbook.
	versions, err := cb.Versions(ctx, entry.Name)
	if err != nil {
		fail(entry.Name, err)
		return
	}
	tag, err := ResolveVersion(entry.Version, versions)
	if err != nil {
		fail(entry.Name, err)
		return
	}
	rid := entry.Name + "@" + tag

	// §12.3 order: fetch the manifest, verify the signature over its
	// digest, only then read the document.
	fetched, err := cb.FetchArtifact(ctx, entry.Name, tag)
	if err != nil {
		fail(rid, err)
		return
	}
	nominalRepo := fetched.NominalRepo
	// Trust scopes match the CANONICAL nominal repository (Docker Hub
	// aliases folded, ports folded per ADR-0013) — the documented pattern
	// space, invariant across cascade hops (FR-036: trust follows the
	// nominal ref, never the endpoint).
	trustRepo, err := nominalRepoOf(cookbookRef, entry.Name)
	if err != nil {
		fail(rid, err)
		return
	}
	decision := e.trust.Decide(trustRepo)
	verified, scopeName, err := e.verifyRecipe(ctx, fetched, decision)
	if err != nil {
		fail(rid, err)
		return
	}
	if err := cb.LoadDocument(ctx, fetched, entry.Name); err != nil {
		fail(rid, err)
		return
	}
	recipe := fetched.Recipe

	t.Resolutions = append(t.Resolutions, tasks.Resolution{
		Recipe: rid, Requested: entry.Version, Resolved: tag,
		Digest: fetched.ManifestDigest, TrustScope: scopeName,
	})
	logger.LogAttrs(ctx, slog.LevelInfo, "recipe verified",
		slog.String("digest", fetched.ManifestDigest),
		slog.Bool("signature_verified", verified),
		slog.String("trust_scope", scopeName))

	// Ingredients: bounded parallelism (NFR-008), per-item isolation.
	var mu sync.Mutex
	sem := make(chan struct{}, max(1, e.cfg.Parallelism))
	var wg sync.WaitGroup
	records := make([]store.IngredientRecord, len(recipe.Spec.Ingredients))
	for i := range recipe.Spec.Ingredients {
		ing := &recipe.Spec.Ingredients[i]
		itemName := rid + "/" + ing.Name
		mu.Lock()
		item := itemFor(t, itemName)
		if item.Status == tasks.StatusDone || item.Status == tasks.StatusSkipped {
			// Resumed task: settled items stay settled (FR-029) — still
			// recompute the record row for the recipe graph.
			records[i] = e.ingredientRecord(ing)
			mu.Unlock()
			continue
		}
		item.Status = tasks.StatusRunning
		item.Digest = ing.Digest
		mu.Unlock()
		save()

		wg.Add(1)
		sem <- struct{}{}
		go func(i int, ing *spec.Ingredient, itemName string) {
			defer wg.Done()
			defer func() { <-sem }()
			rec, res, err := e.syncIngredient(ctx, t, &mu, logger, save, ing, rid)
			mu.Lock()
			item := itemFor(t, itemName)
			if err != nil {
				item.Status = tasks.StatusFailed
				item.Error = tasks.FromTaxonomy(e.mapError(err, ing.Ref))
			} else {
				records[i] = rec
				t.Resolutions = append(t.Resolutions, res)
			}
			mu.Unlock()
			save()
		}(i, ing, itemName)
	}
	wg.Wait()

	// The recipe artifact itself: stored with its signature so the local
	// store is a cookbook for the zone below (cascade; FR-034 groundwork).
	recipeItemName := rid + "/recipe"
	item := itemFor(t, recipeItemName)
	if item.Status != tasks.StatusDone && item.Status != tasks.StatusSkipped {
		item.Status = tasks.StatusRunning
		save()
		status, err := e.storeRecipeArtifact(ctx, fetched)
		if err != nil {
			item.Status = tasks.StatusFailed
			item.Error = tasks.FromTaxonomy(e.mapError(err, rid))
			save()
			return
		}
		item.Status = tasks.StatusDone
		if status == string(importer.StatusUpToDate) {
			item.Status = tasks.StatusSkipped
		}
		item.Digest = fetched.ManifestDigest
		save()
	}

	// Promotion (FR-013, FR-028, FR-034): the content is in the store,
	// verified and complete, so it can now cross into the destination
	// zone. It runs after the fetch and never during it — pushing an
	// ingredient the local store has not finished committing would put
	// content on the destination that this instance cannot prove it holds.
	e.promoteRecipe(ctx, t, &mu, logger, save, fetched, rid)

	// Record the graph: reachability for GC/prune (FR-044/FR-045), zone
	// identity and resolution timestamp for the milestone-5 media manifest
	// (FR-054), correlation for R-09.
	kept := records[:0]
	for _, r := range records {
		if r.Repo != "" {
			kept = append(kept, r)
		}
	}
	if err := e.store.PutRecipeRecord(&store.RecipeRecord{
		Name: recipe.Metadata.Name, Version: recipe.Metadata.Version,
		CookbookRepo: nominalRepo, Digest: fetched.ManifestDigest,
		RunID: t.RunID, Zone: zone, ResolvedAt: time.Now().UTC(),
		Verified: verified, TrustScope: scopeName, Ingredients: kept,
	}); err != nil {
		fail(rid, err)
	}
}

// verifyRecipe applies FR-033: verify against the decision's key set;
// an unsigned recipe passes only inside a declared allowUnsigned scope.
func (e *Engine) verifyRecipe(ctx context.Context, f *FetchedRecipe, d Decision) (verified bool, scope string, err error) {
	src := e.remotes.Manifests(f.NominalRepo)
	if d.Keys != nil {
		_, err := sigverify.Verify(ctx, src, f.NominalRepo, f.ManifestDigest, d.Keys)
		if err == nil {
			return true, d.Scope, nil
		}
		if errors.Is(err, sigverify.ErrNoSignature) && d.AllowUnsigned {
			return false, d.Scope, nil
		}
		return false, d.Scope, err
	}
	if d.AllowUnsigned {
		return false, d.Scope, nil
	}
	return false, d.Scope, taxonomy.New(taxonomy.CodeSignature, taxonomy.Params{
		"recipe":       f.NominalRepo + ":" + f.Tag,
		"fingerprints": "none — no trust root is configured",
	})
}

// syncIngredient fetches one ingredient by its pinned digest into its
// relocated repository (FR-035), with source substitution (FR-036),
// platform selection (FR-022), artifactType enforcement (§7.3), chart
// dependency verification (FR-024), and bounded retries (FR-029).
func (e *Engine) syncIngredient(ctx context.Context, t *tasks.Task, mu *sync.Mutex, logger *slog.Logger, save func(), ing *spec.Ingredient, rid string) (store.IngredientRecord, tasks.Resolution, error) {
	logger = logger.With(slog.String("ingredient", ing.Name), slog.String("digest", ing.Digest))
	rec := e.ingredientRecord(ing)
	res := tasks.Resolution{
		Recipe: rid, Ingredient: ing.Name,
		Requested: ing.Version, Resolved: ing.Version, Digest: ing.Digest,
	}
	repo := rec.Repo

	effective, err := e.remotes.Effective(ing.Ref)
	if err != nil {
		return rec, res, err
	}
	if effective != ing.Ref {
		res.Effective = effective
		logger.LogAttrs(ctx, slog.LevelInfo, "source substituted",
			slog.String("nominal", ing.Ref), slog.String("effective", effective))
	}

	// FR-026: per-digest status before any transfer.
	switch {
	case !e.store.HasManifest(ctx, repo, ing.Digest):
		res.Status = string(importer.StatusNew)
	default:
		if d, ok := e.store.ResolveTag(ctx, repo, ing.Version); ok && d == ing.Digest {
			res.Status = string(importer.StatusUpToDate)
			mu.Lock()
			itemFor(t, rid+"/"+ing.Name).Status = tasks.StatusSkipped
			mu.Unlock()
			e.recordProvenance(repo, rid, t.RunID)
			return rec, res, nil
		}
		res.Status = string(importer.StatusOutdated)
	}

	if e.meters.TransferStarted != nil {
		e.meters.TransferStarted()
		defer e.meters.TransferDone()
	}
	var transferred int64
	err = withRetries(ctx, e.cfg.Retries, func() error {
		n, err := e.transferIngredient(ctx, t, mu, save, ing, repo, rid)
		transferred += n
		return err
	})
	if e.meters.BytesMoved != nil && transferred > 0 {
		e.meters.BytesMoved(transferred)
	}
	if err != nil {
		return rec, res, err
	}
	// Best effort: an ingredient may carry its own attached signature —
	// transfer tools MUST copy signature artifacts along (§12.2).
	e.copyAttachedSignature(ctx, logger, ing.Ref, repo, ing.Digest)
	e.recordProvenance(repo, rid, t.RunID)

	mu.Lock()
	item := itemFor(t, rid+"/"+ing.Name)
	item.Status = tasks.StatusDone
	item.SizeBytes = transferred
	mu.Unlock()
	logger.LogAttrs(ctx, slog.LevelInfo, "ingredient synchronized",
		slog.String("repository", repo), slog.String("status", res.Status),
		slog.Int64("transferred_bytes", transferred))
	return rec, res, nil
}

// transferIngredient performs the bit-exact copy of one ingredient.
func (e *Engine) transferIngredient(ctx context.Context, t *tasks.Task, mu *sync.Mutex, save func(), ing *spec.Ingredient, repo, rid string) (int64, error) {
	desc, err := e.remotes.Get(ctx, ing.Ref, ing.Digest)
	if err != nil {
		return 0, err
	}
	bl, err := e.blobsFor(ing.Ref, e.itemTracker(t, mu, save, rid+"/"+ing.Name))
	if err != nil {
		return 0, err
	}
	if desc.Digest.String() != ing.Digest {
		return 0, taxonomy.New(taxonomy.CodeDigestMismatch, taxonomy.Params{
			"reference": ing.Ref, "expected": ing.Digest, "actual": desc.Digest.String()})
	}

	// §7.3: an OCIArtifact declaring an artifactType only accepts content
	// of that type (anti tag-reuse / repository confusion).
	if ing.Kind == spec.IngredientOCIArtifact && ing.ArtifactType != "" {
		if got := artifactTypeOf(desc.Manifest); got != ing.ArtifactType {
			return 0, taxonomy.New(taxonomy.CodeArtifactType, taxonomy.Params{
				"reference": ing.Ref, "expected": ing.ArtifactType, "actual": got})
		}
	}

	if desc.MediaType.IsIndex() {
		selected, err := selectPlatforms(desc, ing)
		if err != nil {
			return 0, err
		}
		return copyIndexChildren(ctx, e.store, repo, ing.Version, desc, selected, bl)
	}

	img, err := desc.Image()
	if err != nil {
		return 0, err
	}
	if ing.Kind == spec.IngredientHelmChart {
		rows, err := importer.VerifyChart(img)
		mu.Lock()
		t.ChartDependencies = append(t.ChartDependencies, rows...)
		mu.Unlock()
		if err != nil {
			return 0, err
		}
	}
	return copyImage(ctx, e.store, repo, ing.Version, img, bl)
}

// blobsFor builds the resumable-fetch context of one ingredient: the
// effective source repository (FR-036 substitution applied — the bytes
// come from the endpoint actually contacted, not from the nominal one)
// and the instance resumer.
func (e *Engine) blobsFor(nominalRef string, track func(string, int64, int64, bool, bool)) (blobs, error) {
	repo, _, err := e.remotes.Repository(nominalRef)
	if err != nil {
		return blobs{}, err
	}
	return blobs{src: repo, resumer: e.remotes.Resumer(), track: track}, nil
}

// itemTracker records per-blob progress on one task item and persists it.
// Ingredients are transferred concurrently (NFR-008), so every write goes
// through the task mutex the rest of the run already uses.
func (e *Engine) itemTracker(t *tasks.Task, mu *sync.Mutex, save func(), itemName string) func(string, int64, int64, bool, bool) {
	return func(dgst string, received, total int64, resumed, done bool) {
		mu.Lock()
		item := itemFor(t, itemName)
		if done {
			item.TrackBlobDone(dgst)
		} else {
			item.TrackBlob(dgst, received, total, resumed, false)
		}
		mu.Unlock()
		if save != nil {
			save()
		}
	}
}

// selectPlatforms maps the ingredient's platforms field (FR-022,
// RECIPE-SPEC §7.1) onto index child digests. nil means every child
// (platforms absent: transfer everything, attestations included).
func selectPlatforms(desc *remote.Descriptor, ing *spec.Ingredient) (map[string]bool, error) {
	if len(ing.Platforms) == 0 {
		return nil, nil
	}
	idx, err := desc.ImageIndex()
	if err != nil {
		return nil, err
	}
	man, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, p := range ing.Platforms {
		want[p] = true
	}
	selected := map[string]bool{}
	for i := range man.Manifests {
		child := &man.Manifests[i]
		if child.Platform == nil {
			continue
		}
		label := child.Platform.OS + "/" + child.Platform.Architecture
		if child.Platform.Variant != "" {
			label += "/" + child.Platform.Variant
		}
		if want[label] {
			selected[child.Digest.String()] = true
			delete(want, label)
		}
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for p := range want {
			missing = append(missing, p)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("platforms %s not present in the source index of %s", strings.Join(missing, ", "), ing.Ref)
	}
	return selected, nil
}

// storeRecipeArtifact copies the recipe artifact and its attached
// signature into the local store under the relocated cookbook path — the
// local store is the cookbook of the zone below (cascade). Recipes pulled
// through hops accumulate their provenance chain in the path; the
// zone-cookbook push of FR-034 (milestone 4) re-publishes them at the
// zone's own cookbook location.
func (e *Engine) storeRecipeArtifact(ctx context.Context, f *FetchedRecipe) (string, error) {
	local, err := relocate.PathWithBase(e.base, f.NominalRepo)
	if err != nil {
		return "", err
	}
	if d, ok := e.store.ResolveTag(ctx, local, f.Tag); ok && d == f.ManifestDigest {
		return string(importer.StatusUpToDate), nil
	}
	if err := copyArtifact(ctx, e.store, e.remotes, f.NominalRepo, local, f.Tag, f.ManifestBytes, f.MediaType); err != nil {
		return "", err
	}
	e.copyAttachedSignature(ctx, slog.Default(), f.NominalRepo, local, f.ManifestDigest)
	if err := e.store.SetProvenance(local, &store.Provenance{
		Class: store.ProvenanceRecipe, Recipe: f.Recipe.Metadata.Name,
		RecipeVersion: f.Recipe.Metadata.Version,
	}); err != nil {
		return "", err
	}
	return string(importer.StatusNew), nil
}

// copyAttachedSignature copies the cosign attached-signature artifact of a
// manifest when the source carries one (§12.2: signatures travel with the
// content). Its absence is not an error — the trust decision already ran.
func (e *Engine) copyAttachedSignature(ctx context.Context, logger *slog.Logger, nominalRepo, localRepo, manifestDigest string) {
	tag := SignatureTag(manifestDigest)
	desc, err := e.remotes.Get(ctx, nominalRepo, tag)
	if err != nil {
		return
	}
	if err := copyArtifact(ctx, e.store, e.remotes, nominalRepo, localRepo, tag, desc.Manifest, string(desc.MediaType)); err != nil {
		logger.LogAttrs(ctx, slog.LevelWarn, "signature artifact copy failed",
			slog.String("repository", nominalRepo), slog.String("error", err.Error()))
	}
}

// ingredientRecord derives the graph row of one ingredient.
func (e *Engine) ingredientRecord(ing *spec.Ingredient) store.IngredientRecord {
	repo, err := relocate.PathWithBase(e.base, ing.Ref)
	if err != nil {
		repo = ""
	}
	return store.IngredientRecord{
		Name: ing.Name, Kind: string(ing.Kind), Repo: repo,
		Tag: ing.Version, Digest: ing.Digest,
	}
}

// recordProvenance marks a repository as recipe-managed (FR-045 classes).
func (e *Engine) recordProvenance(repo, rid, runID string) {
	name, version, _ := strings.Cut(rid, "@")
	_ = e.store.SetProvenance(repo, &store.Provenance{
		Class: store.ProvenanceRecipe, Recipe: name, RecipeVersion: version, RunID: runID,
	})
}

// itemFor finds or creates the named task item. Callers hold the task
// mutation lock when running concurrently.
func itemFor(t *tasks.Task, name string) *tasks.Item {
	for i := range t.Items {
		if t.Items[i].Name == name {
			return &t.Items[i]
		}
	}
	t.Items = append(t.Items, tasks.Item{Name: name, Status: tasks.StatusPending})
	return &t.Items[len(t.Items)-1]
}

// withRetries runs fn with bounded backoff on failure (FR-029).
func withRetries(ctx context.Context, retries int, fn func() error) error {
	var err error
	for attempt := 0; ; attempt++ {
		err = fn()
		if err == nil || attempt >= retries {
			return err
		}
		var te *taxonomy.Error
		if errors.As(err, &te) {
			// Policy and verification refusals are deterministic: a retry
			// cannot change the verdict.
			if te.Entry().Class != taxonomy.ClassOperational {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(1<<attempt) * 500 * time.Millisecond):
		}
	}
}

// artifactTypeOf reads a manifest's artifactType, falling back to the
// config media type for older producers (§7.3 — artifactType wins when
// both are present).
func artifactTypeOf(manifest []byte) string {
	var man artifactManifest
	if err := json.Unmarshal(manifest, &man); err != nil {
		return ""
	}
	if man.ArtifactType != "" {
		return man.ArtifactType
	}
	return man.Config.MediaType
}
