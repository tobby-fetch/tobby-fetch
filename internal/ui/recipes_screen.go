// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Recipe screens (UI-SPEC §6): /recipes lists the recorded recipe graph
// with the configured Retriever source (FR-010) and the manual sync
// trigger (FR-014); /recipes/{recipe}/mapping is the per-recipe
// source→destination table (FR-035, FR-065); /admin/retriever is the
// read-only configuration screen — the configuration itself comes from
// file, environment, and flags only (FR-003). URLs carry exactly the
// parameters of the /api/v1 mirrors (FR-061, ADR-0015 §1).

package ui

import (
	"net/http"
	"net/url"
	"sort"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// recipeRow decorates one recorded recipe for the listing.
type recipeRow struct {
	store.RecipeRecord
	MappingHref string
}

// recipesData feeds the /recipes page.
type recipesData struct {
	Rows []recipeRow
	// Source is the configured Retriever source (FR-010); empty means not
	// configured, and the sync trigger stays disabled with an explanation.
	Source string
}

// sortRecipeRecords orders records by name, then most recently resolved
// first, then version — a stable, readable listing over the map-backed
// store accessor.
func sortRecipeRecords(records []store.RecipeRecord) {
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

// recipesList serves GET /recipes: every recorded recipe of the store
// (store.RecipeRecords), newest resolution first per name, with the
// verified state always explicit (FR-033: an admitted-unsigned recipe
// names its declared scope, never silently).
func (u *UI) recipesList(w http.ResponseWriter, r *http.Request) {
	records, err := u.store.RecipeRecords()
	if err != nil {
		u.contentError(w, r, "recipes", &recipesData{Source: u.retrieverSource}, storeErr(err))
		return
	}
	sortRecipeRecords(records)
	data := &recipesData{Source: u.retrieverSource}
	for i := range records {
		data.Rows = append(data.Rows, recipeRow{
			RecipeRecord: records[i],
			MappingHref:  "/recipes/" + url.PathEscape(records[i].Name) + "/mapping",
		})
	}
	u.render.Page(w, r, "recipes", data)
}

// recipesSync serves POST /recipes/sync (FR-014): enqueue one sync task —
// Reference is the configured Retriever source, items are discovered by
// the runner — audit (FR-094), and redirect to the task like the import
// flow. Without a configured source the trigger is refused with the
// taxonomized configuration error.
func (u *UI) recipesSync(w http.ResponseWriter, r *http.Request) {
	if u.retrieverSource == "" {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "no Retriever source is configured (retriever.source, FR-010): a synchronization has nothing to fetch",
		}))
		return
	}
	if u.queue == nil {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeInternal, nil))
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	t, err := u.queue.Create(tasks.TypeSync, u.retrieverSource, id.Name, nil)
	if err != nil {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor:   id.Name,
		Action:  audit.ActionSyncCreate,
		Target:  u.retrieverSource,
		Outcome: audit.OutcomeSuccess,
		Origin:  auth.ClientOrigin(r),
	})
	u.redirectTo(w, r, "/tasks/"+t.ID)
}

// recipeMappingData feeds the /recipes/{recipe}/mapping page: every
// recorded version of the recipe, most recently resolved first, with its
// ingredient→destination rows (FR-035, FR-065).
type recipeMappingData struct {
	Name     string
	Versions []store.RecipeRecord
}

// recipeMapping serves GET /recipes/{recipe}/mapping. An unknown recipe
// answers the taxonomized 404.
func (u *UI) recipeMapping(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("recipe")
	records, err := u.store.RecipeRecords()
	if err != nil {
		u.contentError(w, r, "recipe-mapping", &recipeMappingData{Name: name}, storeErr(err))
		return
	}
	data := &recipeMappingData{Name: name}
	for i := range records {
		if records[i].Name == name {
			data.Versions = append(data.Versions, records[i])
		}
	}
	if len(data.Versions) == 0 {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeNotFound, nil))
		return
	}
	sortRecipeRecords(data.Versions)
	u.render.Page(w, r, "recipe-mapping", data)
}

// retrieverData feeds the /admin/retriever page: the configured source
// (FR-010), the declared relaxed trust scopes (FR-033), the anonymous
// FileSets (FR-047), and the latest sync task. Everything is read-only —
// the configuration comes from file, environment, and flags (FR-003).
type retrieverData struct {
	Source            string
	RelaxedScopes     []string
	AnonymousFileSets []string
	// Allowlist reports the registry policy (FR-030). An undeclared
	// policy is shown as undeclared, never as an empty list of allowed
	// registries: "nothing configured" and "nothing allowed" are opposite
	// statements and must never render the same.
	AllowlistDeclared bool
	Allowlist         []string
	// LastSync is the most recent sync-type task, nil when none ran yet.
	LastSync    *taskRow
	HasLastSync bool
}

// adminRetriever serves GET /admin/retriever.
func (u *UI) adminRetriever(w http.ResponseWriter, r *http.Request) {
	data := &retrieverData{
		Source:            u.retrieverSource,
		RelaxedScopes:     u.relaxedScopes,
		AnonymousFileSets: u.anonymousFileSets,
		AllowlistDeclared: u.allowlist.Declared(),
		Allowlist:         u.allowlist.Patterns(),
	}
	if u.queue != nil {
		if list := u.queue.List("", tasks.TypeSync, ""); len(list) > 0 {
			row := newTaskRow(list[0], u.now())
			data.LastSync = &row
			data.HasLastSync = true
		}
	}
	u.render.Page(w, r, "admin-retriever", data)
}
