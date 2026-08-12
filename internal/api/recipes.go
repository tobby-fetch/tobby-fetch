// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Recipe endpoints (FR-060): the strict mirror of the /recipes screens
// (FR-061). GET /api/v1/recipes lists the recorded recipe graph,
// GET /api/v1/recipes/{recipe}/mapping is the per-recipe destination
// table (FR-035, FR-065), POST /api/v1/sync is the FR-014 manual trigger,
// DELETE /api/v1/content/{repo...} is the FR-044 amendment removal, and
// GET /api/v1/retriever mirrors the /admin/retriever screen. Payloads
// carry raw values and RFC 3339 timestamps exclusively (ADR-0015 §7).

package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// RegisterRecipes mounts the recipe, sync, removal, and retriever
// endpoints on the API surface. Reading needs viewer, triggering a sync
// needs operator, removal and the retriever screen mirror need admin
// (ADR-0009 — same gates as the screens, FR-061).
func RegisterRecipes(a *API, st *store.Store, q *tasks.Queue, retrieverSource string, relaxedScopes, anonymousFileSets []string) {
	rc := &recipesAPI{
		api: a, store: st, queue: q,
		source:            retrieverSource,
		relaxedScopes:     relaxedScopes,
		anonymousFileSets: anonymousFileSets,
	}
	a.Handle("GET /api/v1/recipes", a.RequireRole(auth.RoleViewer, rc.list))
	a.Handle("GET /api/v1/recipes/{recipe}/mapping", a.RequireRole(auth.RoleViewer, rc.mapping))
	a.Handle("POST /api/v1/sync", a.RequireRole(auth.RoleOperator, rc.sync))
	a.Handle("DELETE /api/v1/content/{repo...}", a.RequireRole(auth.RoleAdmin, rc.deleteRepo))
	a.Handle("GET /api/v1/retriever", a.RequireRole(auth.RoleAdmin, rc.retriever))
}

type recipesAPI struct {
	api               *API
	store             *store.Store
	queue             *tasks.Queue
	source            string
	relaxedScopes     []string
	anonymousFileSets []string
}

// sortRecords orders records by name, then most recently resolved first,
// then version — the exact order of the /recipes screen (FR-061).
func sortRecords(records []store.RecipeRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].Name != records[j].Name {
			return records[i].Name < records[j].Name
		}
		if !records[i].ResolvedAt.Equal(records[j].ResolvedAt) {
			return records[i].ResolvedAt.After(records[j].ResolvedAt)
		}
		return records[i].Version > records[j].Version
	})
}

// list serves GET /api/v1/recipes: every recorded recipe graph entry, the
// stored schema as-is — Verified and TrustScope travel together, so an
// admitted-unsigned recipe is never silent (FR-033).
func (rc *recipesAPI) list(w http.ResponseWriter, r *http.Request) {
	records, err := rc.store.RecipeRecords()
	if err != nil {
		rc.storeProblem(w, r, err)
		return
	}
	sortRecords(records)
	if records == nil {
		records = []store.RecipeRecord{}
	}
	rc.api.JSON(w, http.StatusOK, map[string]any{"recipes": records})
}

// mapping serves GET /api/v1/recipes/{recipe}/mapping: every recorded
// version of one recipe, most recently resolved first, ingredients
// included (FR-035, FR-065). Unknown recipes answer the shared 404.
func (rc *recipesAPI) mapping(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("recipe")
	records, err := rc.store.RecipeRecords()
	if err != nil {
		rc.storeProblem(w, r, err)
		return
	}
	var versions []store.RecipeRecord
	for i := range records {
		if records[i].Name == name {
			versions = append(versions, records[i])
		}
	}
	if len(versions) == 0 {
		rc.api.Problem(w, r, taxonomy.New(taxonomy.CodeNotFound, nil))
		return
	}
	sortRecords(versions)
	rc.api.JSON(w, http.StatusOK, map[string]any{"recipe": name, "versions": versions})
}

// sync serves POST /api/v1/sync (FR-014): enqueue one sync task — the
// Reference is the configured Retriever source, the runner discovers the
// items — audit (FR-094), 201 with the task. Without a configured source
// the trigger is refused with the taxonomized configuration error.
func (rc *recipesAPI) sync(w http.ResponseWriter, r *http.Request) {
	if rc.source == "" {
		rc.api.Problem(w, r, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "no Retriever source is configured (retriever.source, FR-010): a synchronization has nothing to fetch",
		}))
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	task, err := rc.queue.Create(tasks.TypeSync, rc.source, id.Name, nil)
	if err != nil {
		rc.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	audit.Log(r.Context(), rc.api.logger, &audit.Event{
		Actor:   id.Name,
		Action:  audit.ActionSyncCreate,
		Target:  rc.source,
		Outcome: audit.OutcomeSuccess,
		Origin:  auth.ClientOrigin(r),
	})
	// Answer from a snapshot: the worker may already be mutating the
	// queue's own copy of the task.
	if snap, ok := rc.queue.Get(task.ID); ok {
		task = snap
	}
	rc.api.JSON(w, http.StatusCreated, map[string]any{"task": newTaskJSON(task)})
}

// deleteRepo serves DELETE /api/v1/content/{repo...} (FR-044 amendment):
// remove one unit-imported repository and garbage-collect, 204 on
// success. Same policy answers as the screen (FR-061): recipe-managed
// content answers TBY-POL-002 naming the managing recipes, seeded
// content TBY-POL-003, unknown repositories the shared 404. Audited
// (FR-094) with the authenticated identity and the network origin.
func (rc *recipesAPI) deleteRepo(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	id, _ := auth.IdentityFrom(r.Context())
	denied := func(e *taxonomy.Error) {
		audit.Log(r.Context(), rc.api.logger, &audit.Event{
			Actor: id.Name, Action: audit.ActionContentDelete, Target: repo,
			Outcome: audit.OutcomeDenied, Origin: auth.ClientOrigin(r),
		})
		rc.api.Problem(w, r, e)
	}
	class, managed := rc.provenance(repo)
	if class != store.ProvenanceUnitImport {
		// A policy answer must not leak on an unknown path: 404 first.
		if _, err := rc.store.RepoInfo(r.Context(), repo); errors.Is(err, store.ErrNotFound) {
			rc.api.Problem(w, r, taxonomy.New(taxonomy.CodeNotFound, nil))
			return
		}
		if class == store.ProvenanceRecipe {
			denied(taxonomy.New(taxonomy.CodeRecipeManaged,
				taxonomy.Params{"repository": repo, "recipes": strings.Join(managed, ", ")}))
			return
		}
		denied(taxonomy.New(taxonomy.CodeSeedContent, taxonomy.Params{"repository": repo}))
		return
	}
	if err := rc.store.DeleteRepository(r.Context(), repo, rc.api.logger); err != nil {
		var rme *store.RecipeManagedError
		if errors.As(err, &rme) {
			denied(taxonomy.New(taxonomy.CodeRecipeManaged,
				taxonomy.Params{"repository": repo, "recipes": strings.Join(rme.Recipes, ", ")}))
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			rc.api.Problem(w, r, taxonomy.New(taxonomy.CodeNotFound, nil))
			return
		}
		audit.Log(r.Context(), rc.api.logger, &audit.Event{
			Actor: id.Name, Action: audit.ActionContentDelete, Target: repo,
			Outcome: audit.OutcomeFailure, Origin: auth.ClientOrigin(r),
		})
		rc.api.Problem(w, r, taxonomy.New(taxonomy.CodeStoreWrite,
			taxonomy.Params{"detail": err.Error()}).WithCause(err))
		return
	}
	audit.Log(r.Context(), rc.api.logger, &audit.Event{
		Actor: id.Name, Action: audit.ActionContentDelete, Target: repo,
		Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

// provenance resolves the effective provenance of one repository: the
// live recipe graph wins, then the recorded ledger class, then seed —
// the same resolution as the screen (FR-061).
func (rc *recipesAPI) provenance(repo string) (class store.ProvenanceClass, managedBy []string) {
	managed := rc.store.ManagingRecipes(repo)
	if len(managed) > 0 {
		return store.ProvenanceRecipe, managed
	}
	if p, ok := rc.store.ProvenanceOf(repo); ok {
		return p.Class, nil
	}
	return store.ProvenanceSeed, nil
}

// retrieverResponse mirrors the /admin/retriever screen (FR-010, FR-033,
// FR-047).
type retrieverResponse struct {
	Source             string    `json:"source"`
	RelaxedTrustScopes []string  `json:"relaxed_trust_scopes"`
	AnonymousFileSets  []string  `json:"anonymous_filesets"`
	LastSync           *taskJSON `json:"last_sync,omitempty"`
}

// retriever serves GET /api/v1/retriever.
func (rc *recipesAPI) retriever(w http.ResponseWriter, _ *http.Request) {
	resp := retrieverResponse{
		Source:             rc.source,
		RelaxedTrustScopes: rc.relaxedScopes,
		AnonymousFileSets:  rc.anonymousFileSets,
	}
	if resp.RelaxedTrustScopes == nil {
		resp.RelaxedTrustScopes = []string{}
	}
	if resp.AnonymousFileSets == nil {
		resp.AnonymousFileSets = []string{}
	}
	if rc.queue != nil {
		if list := rc.queue.List("", tasks.TypeSync, ""); len(list) > 0 {
			last := newTaskJSON(list[0])
			resp.LastSync = &last
		}
	}
	rc.api.JSON(w, http.StatusOK, resp)
}

// storeProblem maps a failed store read onto its taxonomy entry (R-03).
func (rc *recipesAPI) storeProblem(w http.ResponseWriter, r *http.Request, err error) {
	rc.api.Problem(w, r, taxonomy.New(taxonomy.CodeStoreRead,
		taxonomy.Params{"detail": err.Error()}).WithCause(err))
}
