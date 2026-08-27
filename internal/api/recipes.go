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
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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

// RecipeOptions is what the recipe endpoints read from the instance.
// A struct rather than a parameter list: the set grows one entry per
// milestone, and a positional call of eight strings and slices is one
// transposition away from an instance reporting somebody else's
// configuration.
type RecipeOptions struct {
	Store *store.Store
	Queue *tasks.Queue
	// Source is the configured Retriever source (FR-010).
	Source string
	// RelaxedScopes names the declared allowUnsigned trust scopes (FR-033).
	RelaxedScopes []string
	// AnonymousFileSets names the FileSets served unauthenticated (FR-047).
	AnonymousFileSets []string
	// Destination is the configured promotion target host, empty when this
	// instance promotes nothing (FR-013).
	Destination string
	// Cookbook is the destination cookbook path recipes are propagated to
	// (FR-034).
	Cookbook string
	// Interval paces the periodic reconciliation (FR-013). Nil on an
	// instance with no scheduler — mirror mode, where FR-014 forbids
	// unattended runs — and the interval endpoints then answer that the
	// setting does not apply rather than pretending to change it.
	Interval *schedule.Interval
	// Prune is what an unqualified trigger does about content the
	// Retriever no longer references (FR-045): on in mirror mode, where
	// the operator confirms it against a projected list, and off in
	// passthrough unless sync.prune says otherwise.
	Prune bool
	// Projector computes the FR-045 trigger-time confirmation — the list
	// and the total size of what a prune would remove. Nil on an
	// instance wired without an engine; the endpoint then answers that
	// it cannot project rather than an empty, reassuring list.
	Projector PruneProjector
	// Occupancy is the latest store-footprint sample against the
	// configured threshold (R-33); nil on an instance with no threshold.
	Occupancy *store.OccupancyMonitor
	// Now injects the clock for the override's recorded timestamp.
	Now func() time.Time
}

// RegisterRecipes mounts the recipe, sync, removal, retriever, and
// promotion-interval endpoints on the API surface. Reading needs viewer,
// triggering a sync needs operator, removal, the retriever mirror and the
// interval change need admin (ADR-0009 — same gates as the screens,
// FR-061).
func RegisterRecipes(a *API, o *RecipeOptions) {
	rc := &recipesAPI{api: a, opts: *o}
	if rc.opts.Now == nil {
		rc.opts.Now = time.Now
	}
	a.Handle("GET /api/v1/recipes", a.RequireRole(auth.RoleViewer, rc.list))
	a.Handle("GET /api/v1/recipes/{recipe}/mapping", a.RequireRole(auth.RoleViewer, rc.mapping))
	a.Handle("POST /api/v1/sync", a.RequireRole(auth.RoleOperator, rc.sync))
	a.Handle("GET /api/v1/sync/prune-preview", a.RequireRole(auth.RoleOperator, rc.prunePreview))
	a.Handle("DELETE /api/v1/content/{repo...}", a.RequireRole(auth.RoleAdmin, rc.deleteRepo))
	a.Handle("GET /api/v1/retriever", a.RequireRole(auth.RoleAdmin, rc.retriever))
	a.Handle("PUT /api/v1/retriever/interval", a.RequireRole(auth.RoleAdmin, rc.setInterval))
	a.Handle("DELETE /api/v1/retriever/interval", a.RequireRole(auth.RoleAdmin, rc.clearInterval))
}

type recipesAPI struct {
	api  *API
	opts RecipeOptions
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
	records, err := rc.opts.Store.RecipeRecords()
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
	records, err := rc.opts.Store.RecipeRecords()
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
	if rc.opts.Source == "" {
		rc.api.Problem(w, r, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "no Retriever source is configured (retriever.source, FR-010): a synchronization has nothing to fetch",
		}))
		return
	}
	prune := rc.opts.Prune
	if body, rerr := io.ReadAll(io.LimitReader(r.Body, maxSyncBody)); rerr == nil && len(body) > 0 {
		var req syncRequest
		if uerr := json.Unmarshal(body, &req); uerr != nil {
			rc.api.Problem(w, r, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
				"file": "request body", "path": "prune",
				"constraint": "the body must be a JSON object with an optional boolean \"prune\"",
			}))
			return
		}
		if req.Prune != nil {
			prune = *req.Prune
		}
	}
	id, _ := auth.IdentityFrom(r.Context())
	task, err := rc.opts.Queue.Create(tasks.TypeSync, rc.opts.Source, id.Name, nil, tasks.WithPrune(prune))
	if err != nil {
		rc.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	audit.Log(r.Context(), rc.api.logger, &audit.Event{
		Actor:   id.Name,
		Action:  audit.ActionSyncCreate,
		Target:  rc.opts.Source,
		Outcome: audit.OutcomeSuccess,
		Origin:  auth.ClientOrigin(r),
	})
	if prune {
		// A run that removes content is its own trail entry (FR-094): the
		// sync record says a cycle started, this one says it was allowed
		// to delete, and after the fact those are different questions.
		audit.Log(r.Context(), rc.api.logger, &audit.Event{
			Actor:   id.Name,
			Action:  audit.ActionPruneActive,
			Target:  task.ID,
			Outcome: audit.OutcomeSuccess,
			Origin:  auth.ClientOrigin(r),
		})
	}
	// Answer from a snapshot: the worker may already be mutating the
	// queue's own copy of the task.
	if snap, ok := rc.opts.Queue.Get(task.ID); ok {
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
		if _, err := rc.opts.Store.RepoInfo(r.Context(), repo); errors.Is(err, store.ErrNotFound) {
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
	if err := rc.opts.Store.DeleteRepository(r.Context(), repo, rc.api.logger); err != nil {
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
	managed := rc.opts.Store.ManagingRecipes(repo)
	if len(managed) > 0 {
		return store.ProvenanceRecipe, managed
	}
	if p, ok := rc.opts.Store.ProvenanceOf(repo); ok {
		return p.Class, nil
	}
	return store.ProvenanceSeed, nil
}

// maxSyncBody bounds the optional trigger body: it carries one boolean,
// and an unbounded read on an authenticated endpoint is a lever anyway.
const maxSyncBody = 4 << 10

// PruneProjector computes what a prune would remove right now. The engine
// implements it; the interface exists so this package stays testable
// without one and so the endpoint cannot reach an engine internal.
type PruneProjector interface {
	ProjectPrune(ctx context.Context) (engine.PruneProjection, error)
}

// syncRequest is the optional body of POST /api/v1/sync.
//
// Prune is a POINTER because absent and false are different instructions:
// absent means "do what this instance does by default", false means "this
// run must not remove anything". A plain bool would silently turn the
// first into the second, which on a mirror instance is a prune the caller
// never asked to skip.
type syncRequest struct {
	Prune *bool `json:"prune,omitempty"`
}

// prunePreviewResponse is the FR-045 trigger-time confirmation: the list
// and the total size of what a prune would remove, so an operator decides
// against facts rather than against a promise.
type prunePreviewResponse struct {
	Items      []prunePreviewItem `json:"items"`
	Count      int                `json:"count"`
	TotalBytes int64              `json:"total_bytes"`
}

type prunePreviewItem struct {
	Repo   string `json:"repo"`
	Tag    string `json:"tag"`
	Digest string `json:"digest,omitempty"`
	Recipe string `json:"recipe,omitempty"`
	Bytes  int64  `json:"bytes"`
}

// prunePreview serves GET /api/v1/sync/prune-preview (FR-045): what the
// next synchronization would remove if it pruned. Role operator, like the
// trigger it informs. A Retriever that cannot be resolved is an error, not
// an empty list: "nothing would go" and "I could not work out what would
// go" must never read the same to something about to confirm a removal.
func (rc *recipesAPI) prunePreview(w http.ResponseWriter, r *http.Request) {
	if rc.opts.Projector == nil {
		rc.api.Problem(w, r, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "this instance cannot project a prune: no Retriever engine is wired (retriever.source, FR-010)",
		}))
		return
	}
	projection, err := rc.opts.Projector.ProjectPrune(r.Context())
	if err != nil {
		// The engine already taxonomizes what it could not resolve; a bare
		// error means an unexpected one, which is the internal code.
		var te *taxonomy.Error
		if !errors.As(err, &te) {
			te = taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
		}
		rc.api.Problem(w, r, te)
		return
	}
	resp := prunePreviewResponse{
		Items:      make([]prunePreviewItem, 0, len(projection.Items)),
		Count:      len(projection.Items),
		TotalBytes: projection.TotalBytes,
	}
	for _, it := range projection.Items {
		resp.Items = append(resp.Items, prunePreviewItem{
			Repo: it.Repo, Tag: it.Tag, Digest: it.Digest, Recipe: it.Recipe, Bytes: it.Bytes,
		})
	}
	rc.api.JSON(w, http.StatusOK, resp)
}

// retrieverResponse mirrors the /admin/retriever screen (FR-010, FR-033,
// FR-047, FR-013).
type retrieverResponse struct {
	Source             string    `json:"source"`
	RelaxedTrustScopes []string  `json:"relaxed_trust_scopes"`
	AnonymousFileSets  []string  `json:"anonymous_filesets"`
	Destination        string    `json:"destination"`
	Cookbook           string    `json:"cookbook"`
	Interval           *interval `json:"interval,omitempty"`
	// Prune is the FR-045 opt-in: reconciliation removes the
	// recipe-managed content this Retriever no longer references.
	Prune bool `json:"prune"`
	// StoreOccupancy is the footprint against the configured threshold
	// (R-33) — the API side of the persistent banner (FR-061).
	StoreOccupancy occupancyJSON `json:"store_occupancy"`
	LastSync       *taskJSON     `json:"last_sync,omitempty"`
}

// occupancyJSON reports the store footprint against its threshold.
// Monitored is a field of its own rather than "threshold is zero"
// because "no threshold configured" and "within the threshold" are
// opposite statements and must never decode the same.
type occupancyJSON struct {
	Bytes          int64  `json:"bytes"`
	ThresholdBytes int64  `json:"threshold_bytes"`
	Monitored      bool   `json:"monitored"`
	Exceeded       bool   `json:"exceeded"`
	SampledAt      string `json:"sampled_at,omitempty"`
}

// interval reports the FR-013 reconciliation cadence.
//
// Configured and effective are both present because they answer
// different questions and an operator needs both at once: one is what
// redeploying this instance would restore, the other is what it is doing
// right now. Durations are Go duration strings ("15m") rather than
// seconds — the same spelling the configuration file and the API body
// use, so nothing has to be converted between reading a value and
// writing it back.
type interval struct {
	Effective  string `json:"effective"`
	Configured string `json:"configured"`
	Overridden bool   `json:"overridden"`
	// Enabled is false when the effective interval is zero: periodic
	// reconciliation off, manual triggers unaffected.
	Enabled bool `json:"enabled"`
	// Minimum is the floor an override may not go below.
	Minimum string `json:"minimum"`
}

// retriever serves GET /api/v1/retriever.
func (rc *recipesAPI) retriever(w http.ResponseWriter, _ *http.Request) {
	resp := retrieverResponse{
		Source:             rc.opts.Source,
		RelaxedTrustScopes: rc.opts.RelaxedScopes,
		AnonymousFileSets:  rc.opts.AnonymousFileSets,
		Destination:        rc.opts.Destination,
		Cookbook:           rc.opts.Cookbook,
		Interval:           rc.intervalJSON(),
		Prune:              rc.opts.Prune,
		StoreOccupancy:     rc.occupancyJSON(),
	}
	if resp.RelaxedTrustScopes == nil {
		resp.RelaxedTrustScopes = []string{}
	}
	if resp.AnonymousFileSets == nil {
		resp.AnonymousFileSets = []string{}
	}
	if rc.opts.Queue != nil {
		if list := rc.opts.Queue.List("", tasks.TypeSync, ""); len(list) > 0 {
			last := newTaskJSON(list[0])
			resp.LastSync = &last
		}
	}
	rc.api.JSON(w, http.StatusOK, resp)
}

// occupancyJSON renders the latest store-footprint sample. An instance
// with no monitor reports Monitored false rather than a zeroed threshold
// that would read like a satisfied one.
func (rc *recipesAPI) occupancyJSON() occupancyJSON {
	if rc.opts.Occupancy == nil {
		return occupancyJSON{}
	}
	o := rc.opts.Occupancy.Current()
	out := occupancyJSON{
		Bytes:          o.Bytes,
		ThresholdBytes: o.Threshold,
		Monitored:      o.Monitored(),
		Exceeded:       o.Exceeded,
	}
	if !o.SampledAt.IsZero() {
		out.SampledAt = o.SampledAt.UTC().Format(time.RFC3339)
	}
	return out
}

// intervalJSON renders the cadence, nil when this instance has no
// scheduler at all (mirror mode, FR-014).
func (rc *recipesAPI) intervalJSON() *interval {
	iv := rc.opts.Interval
	if iv == nil {
		return nil
	}
	return &interval{
		Effective:  iv.Effective().String(),
		Configured: iv.Configured().String(),
		Overridden: iv.Overridden(),
		Enabled:    iv.Effective() > 0,
		Minimum:    schedule.MinOverride.String(),
	}
}

// setInterval serves PUT /api/v1/retriever/interval (FR-013): change the
// reconciliation cadence of a running instance, persisted so it survives
// a restart. Admin only, and audited as a sensitive configuration change
// (FR-094) — how often this instance reaches into another zone is exactly
// the kind of setting a trail has to be able to answer for.
func (rc *recipesAPI) setInterval(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Interval string `json:"interval"`
	}
	// A rejected request body is a validation verdict on what the client
	// sent, not a statement about how this instance is configured: the
	// two answer different codes, and therefore different HTTP statuses,
	// so a client can tell "fix your request" from "fix the instance".
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		rc.api.Problem(w, r, intervalInvalid("the body must be a JSON object with an \"interval\" duration string (\"30m\"): "+err.Error()))
		return
	}
	d, err := time.ParseDuration(strings.TrimSpace(body.Interval))
	if err != nil {
		rc.api.Problem(w, r, intervalInvalid(body.Interval+" is not a duration (\"30m\", \"2h\", or \"0\" to stop the periodic reconciliation)"))
		return
	}
	rc.applyInterval(w, r, func(iv *schedule.Interval, actor string) error {
		return iv.Set(d, actor, rc.opts.Now())
	}, body.Interval)
}

// clearInterval serves DELETE /api/v1/retriever/interval: drop the
// override and return to the configured value (FR-003 precedence).
func (rc *recipesAPI) clearInterval(w http.ResponseWriter, r *http.Request) {
	rc.applyInterval(w, r, func(iv *schedule.Interval, _ string) error {
		return iv.Clear()
	}, "")
}

// intervalInvalid taxonomizes a rejected interval: the request body is
// the document, "interval" the offending field, and the constraint says
// what would be accepted.
func intervalInvalid(constraint string) *taxonomy.Error {
	return taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
		"file": "the request body", "path": "interval", "constraint": constraint,
	})
}

// applyInterval runs one interval mutation with the shared refusals,
// audit record, and response body. Both endpoints go through it so the
// trail cannot record one of them and not the other.
func (rc *recipesAPI) applyInterval(w http.ResponseWriter, r *http.Request, apply func(*schedule.Interval, string) error, target string) {
	id, _ := auth.IdentityFrom(r.Context())
	iv := rc.opts.Interval
	if iv == nil {
		rc.api.Problem(w, r, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "this instance runs no reconciliation loop: FR-013 applies to passthrough mode, and FR-014 requires mirror-mode synchronizations to be triggered manually",
		}))
		return
	}
	err := apply(iv, id.Name)
	if err != nil {
		audit.Log(r.Context(), rc.api.logger, &audit.Event{
			Actor: id.Name, Action: audit.ActionIntervalChange, Target: target,
			Outcome: audit.OutcomeFailure, Origin: auth.ClientOrigin(r),
		})
		rc.api.Problem(w, r, intervalInvalid(err.Error()).WithCause(err))
		return
	}
	audit.Log(r.Context(), rc.api.logger, &audit.Event{
		Actor: id.Name, Action: audit.ActionIntervalChange, Target: iv.Effective().String(),
		Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r),
	})
	rc.api.JSON(w, http.StatusOK, map[string]any{"interval": rc.intervalJSON()})
}

// storeProblem maps a failed store read onto its taxonomy entry (R-03).
func (rc *recipesAPI) storeProblem(w http.ResponseWriter, r *http.Request, err error) {
	rc.api.Problem(w, r, taxonomy.New(taxonomy.CodeStoreRead,
		taxonomy.Params{"detail": err.Error()}).WithCause(err))
}
