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
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/schedule"
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
	// The prune decision is the form's, not the instance's (FR-045): the
	// operator has just been shown the list and the total size, and an
	// unchecked box is an instruction, not an omission. The form always
	// posts the field — checked or not — through the hidden companion
	// input, so "absent" never has to be guessed at.
	prune := u.prunesByDefault
	if r.Form.Has("prune") {
		prune = r.FormValue("prune") == "on"
	}
	id, _ := auth.IdentityFrom(r.Context())
	t, err := u.queue.Create(tasks.TypeSync, u.retrieverSource, id.Name, nil, tasks.WithPrune(prune))
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
	if prune {
		// A run that removes content is its own trail entry (FR-094): one
		// record says a cycle started, this one says it was allowed to
		// delete, and after the fact those are different questions.
		audit.Log(r.Context(), u.logger, &audit.Event{
			Actor:   id.Name,
			Action:  audit.ActionPruneActive,
			Target:  t.ID,
			Outcome: audit.OutcomeSuccess,
			Origin:  auth.ClientOrigin(r),
		})
	}
	u.redirectTo(w, r, "/tasks/"+t.ID)
}

// PruneProjector computes what a prune would remove right now. The engine
// implements it; the interface keeps this package testable without one.
type PruneProjector interface {
	ProjectPrune(ctx context.Context) (engine.PruneProjection, error)
}

// prunePreviewData feeds the FR-045 trigger-time confirmation fragment:
// what the next synchronization would remove, and how much disk that is.
type prunePreviewData struct {
	Items      []engine.PruneItem
	Count      int
	TotalBytes int64
	// Default is the state the checkbox opens in — on in mirror mode,
	// where the store IS the delivery unit and its operator is standing
	// right here.
	Default bool
}

// prunePreview serves GET /recipes/prune-preview: the swap target the
// trigger loads before an operator confirms (UI-SPEC §5.2 async tile).
//
// It is a fragment rather than part of the page because it costs a
// Retriever resolution: the recipe list must render at once even when the
// cookbook is slow, and a projection that cannot be computed has to be
// able to say so without taking the screen down with it.
func (u *UI) prunePreview(w http.ResponseWriter, r *http.Request) {
	if u.projector == nil || u.retrieverSource == "" {
		u.render.Fragment(w, r, "recipes", "prune-preview-unavailable", nil)
		return
	}
	projection, err := u.projector.ProjectPrune(r.Context())
	if err != nil {
		u.logger.LogAttrs(r.Context(), slog.LevelWarn, "prune projection failed",
			slog.String("error", err.Error()), slog.String("requirement", "FR-045"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		u.render.Fragment(w, r, "recipes", "prune-preview-error", nil)
		return
	}
	u.render.Fragment(w, r, "recipes", "prune-preview", &prunePreviewData{
		Items:      projection.Items,
		Count:      len(projection.Items),
		TotalBytes: projection.TotalBytes,
		Default:    u.prunesByDefault,
	})
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
	// Destination and Cookbook report where this instance promotes to
	// (FR-013, FR-034); empty means it promotes nothing.
	Destination string
	Cookbook    string
	// Interval carries the FR-013 cadence — the one editable control on
	// this screen. Nil in mirror mode (FR-014).
	Interval *intervalData
	// FormError is a localization key for a rejected interval, Value the
	// rejected input preserved in the field.
	FormError string
	Value     string
	// LastSync is the most recent sync-type task, nil when none ran yet.
	LastSync    *taskRow
	HasLastSync bool
	// Prune reports that reconciliation removes the content the Retriever
	// no longer references (FR-045 amendment, R-33). An instance that
	// deletes on a timer says so where its posture is read.
	Prune bool
	// Occupancy is the latest store-footprint sample against the
	// configured threshold (R-33). Zero-valued Threshold means none is
	// configured, which the screen states as such.
	Occupancy store.Occupancy
}

// intervalData renders the reconciliation cadence. Configured and
// Effective are both shown: on an overridden instance they differ, and an
// operator reading only one of them would either mistake the file for the
// truth or the truth for the file.
type intervalData struct {
	Effective  string
	Configured string
	Overridden bool
	Enabled    bool
	Minimum    string
	// Persistent is false without a state directory: the control is then
	// rendered inert rather than accepting a change that could not
	// survive a restart.
	Persistent bool
}

// adminRetriever serves GET /admin/retriever.
func (u *UI) adminRetriever(w http.ResponseWriter, r *http.Request) {
	u.render.Page(w, r, "admin-retriever", u.retrieverScreenData())
}

// retrieverScreenData snapshots the instance for the screen.
func (u *UI) retrieverScreenData() *retrieverData {
	data := &retrieverData{
		Source:            u.retrieverSource,
		RelaxedScopes:     u.relaxedScopes,
		AnonymousFileSets: u.anonymousFileSets,
		AllowlistDeclared: u.allowlist.Declared(),
		Allowlist:         u.allowlist.Patterns(),
		Destination:       u.destination,
		Cookbook:          u.cookbook,
		Prune:             u.prunesByDefault,
	}
	if u.occupancy != nil {
		data.Occupancy = u.occupancy.Current()
	}
	if u.interval != nil {
		data.Interval = &intervalData{
			Effective:  u.interval.Effective().String(),
			Configured: u.interval.Configured().String(),
			Overridden: u.interval.Overridden(),
			Enabled:    u.interval.Effective() > 0,
			Minimum:    schedule.MinOverride.String(),
			Persistent: u.interval.Persistent(),
		}
		data.Value = data.Interval.Effective
	}
	if u.queue != nil {
		if list := u.queue.List("", tasks.TypeSync, ""); len(list) > 0 {
			row := newTaskRow(list[0], u.now())
			data.LastSync = &row
			data.HasLastSync = true
		}
	}
	return data
}

// adminInterval serves POST /admin/retriever/interval (FR-013): change
// the promotion cadence of a running instance. An empty field clears the
// override and returns to the configured value — the same two operations
// the API exposes as PUT and DELETE (FR-061). Audited either way
// (FR-094): the record exists to answer who changed how often this
// instance reaches into the next zone, and a refused attempt is as much
// part of that answer as an accepted one.
func (u *UI) adminInterval(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.IdentityFrom(r.Context())
	raw := strings.TrimSpace(r.PostFormValue("interval"))
	reject := func(key string) {
		audit.Log(r.Context(), u.logger, &audit.Event{
			Actor: id.Name, Action: audit.ActionIntervalChange, Target: raw,
			Outcome: audit.OutcomeFailure, Origin: auth.ClientOrigin(r),
		})
		d := u.retrieverScreenData()
		d.FormError, d.Value = key, raw
		u.render.render(w, r, "admin-retriever", http.StatusBadRequest, u.render.view(r, d))
	}
	if u.interval == nil {
		reject("retriever.interval_unavailable")
		return
	}

	var err error
	if raw == "" {
		err = u.interval.Clear()
	} else {
		var d time.Duration
		if d, err = time.ParseDuration(raw); err != nil {
			reject("retriever.interval_invalid")
			return
		}
		err = u.interval.Set(d, id.Name, u.now())
	}
	if err != nil {
		switch {
		case errors.Is(err, schedule.ErrTooShort):
			reject("retriever.interval_too_short")
		case errors.Is(err, schedule.ErrNoStateDir):
			reject("retriever.interval_no_state")
		default:
			u.render.Error(w, r, taxonomy.New(taxonomy.CodeConfigInvalid,
				taxonomy.Params{"detail": err.Error()}).WithCause(err))
		}
		return
	}
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: id.Name, Action: audit.ActionIntervalChange, Target: u.interval.Effective().String(),
		Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r),
	})
	v := u.render.view(r, u.retrieverScreenData())
	v.Toasts = append(v.Toasts, v.T("retriever.interval_saved", "Interval", u.interval.Effective().String()))
	u.render.render(w, r, "admin-retriever", http.StatusOK, v)
}
