// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Destination-side media endpoints (FR-060, FR-052): what an operator
// standing in an isolated zone does to a transported store, available to
// a machine exactly as it is to a person (FR-061). The Media screen
// (R-02) is built on these three and adds no engine logic of its own —
// the FR-054 order lives in the engine, once.
//
// The verification endpoint answers the report itself rather than a
// summary. It carries codes and parameters, never sentences, so the
// screen, the CLI and a caller's own tooling all re-render it in the
// reader's language (FR-063) from the same document.

package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/buildinfo"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/mediagate"
	"github.com/tobby-fetch/tobby-fetch/internal/slip"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// maxMediaRequest bounds the request body: two booleans and nothing else.
const maxMediaRequest = 4 << 10

// MediaVerifier is the destination-side engine as this surface needs it.
type MediaVerifier interface {
	Zone() string
	// Destination is the zone registry an import pushes into; nil when
	// destination.registry is not configured. Verification needs none.
	Destination() *engine.Destination
	MediaSummary() engine.MediaSummary
	VerifyMedia(ctx context.Context, logger *slog.Logger, opts engine.MediaOptions) (*media.Report, error)
}

// MediaOptions is what RegisterMedia needs wired.
type MediaOptions struct {
	// Engine verifies the medium. Nil on an instance with no engine.
	Engine MediaVerifier
	// Queue is where an import is enqueued.
	Queue *tasks.Queue
	// StorageRoot is the transported store — the import's subject, and
	// the target of its audit record.
	StorageRoot string
	// Gate is the FR-054 serving gate and the session behind it: whether
	// /v2/ and /files/ answer, the verification currently walking the
	// medium, and the verdict the last one reached. It is the API mirror
	// of what the Media screen polls (FR-061). Nil on an instance that
	// guards nothing.
	Gate *mediagate.Gate
	// Scan reads the scanning state the transfer slip reports (R-07):
	// the policy in force and the database in use. Nil leaves the slip
	// saying the policy is advisory and no database is known.
	Scan func() slip.ScanContext
}

// RegisterMedia mounts the media endpoints.
//
// Reading the medium's identity is a viewer's business; verifying it
// re-hashes the whole disk and importing it writes into the zone
// registry, so both are operator actions (ADR-0009) — with the FR-054
// waivers reserved to an administrator, which is what "admin-overridable"
// means when it is enforced rather than merely written down.
func RegisterMedia(a *API, opts *MediaOptions) {
	m := &mediaAPI{api: a, opts: opts}
	a.Handle("GET /api/v1/media", a.RequireRole(auth.RoleViewer, m.summary))
	// The state the Media screen polls, mirrored for a machine (FR-061):
	// whether the content surfaces answer, what the verification in
	// progress is doing, and the last verdict — read-only, hence viewer.
	a.Handle("GET /api/v1/media/verification", a.RequireRole(auth.RoleViewer, m.verification))
	// The transfer slip (R-07): the Media screen's content as a printable
	// document, HTML or text. A read of records the instance already
	// holds — it hashes nothing — hence viewer.
	a.Handle("GET /api/v1/media/slip", a.RequireRole(auth.RoleViewer, m.slip))
	a.Handle("POST /api/v1/media/verify", a.RequireRole(auth.RoleOperator, m.verify))
	a.Handle("POST /api/v1/media/import", a.RequireRole(auth.RoleOperator, m.importMedia))
}

type mediaAPI struct {
	api  *API
	opts *MediaOptions
}

// mediaRequest is the body of both write endpoints: the two FR-054 guards
// an administrator may waive, named one by one.
type mediaRequest struct {
	AllowZoneMismatch bool `json:"allowZoneMismatch"`
	AllowStale        bool `json:"allowStale"`
}

// summary serves GET /api/v1/media: what this instance knows about the
// medium without reading a byte of its content — the identity it claims
// and the freshness record held for the zone (R-28).
func (m *mediaAPI) summary(w http.ResponseWriter, r *http.Request) {
	if m.opts.Engine == nil {
		m.api.Problem(w, r, notADestination())
		return
	}
	m.api.JSON(w, http.StatusOK, m.opts.Engine.MediaSummary())
}

// verification serves GET /api/v1/media/verification: the serving gate
// and the media session behind it (FR-054, FR-061).
//
// It reads state and re-hashes nothing, which is what makes it pollable —
// the screen asks it every two seconds while a verification walks the
// medium, and a caller's own tooling can do the same to know when /v2/
// opened.
func (m *mediaAPI) verification(w http.ResponseWriter, r *http.Request) {
	if m.opts.Gate == nil {
		m.api.Problem(w, r, notADestination())
		return
	}
	m.api.JSON(w, http.StatusOK, m.opts.Gate.Status())
}

// verify serves POST /api/v1/media/verify: re-verify and report, writing
// nothing and pushing nothing (FR-054).
//
// It is a POST although it changes nothing, because it is not a read: it
// re-hashes every covered file on the medium, which on a full disk is
// minutes of I/O. A GET would invite caches and retries to repeat it.
func (m *mediaAPI) verify(w http.ResponseWriter, r *http.Request) {
	if m.opts.Engine == nil || m.opts.Engine.Zone() == "" {
		m.api.Problem(w, r, notADestination())
		return
	}
	req, ok := m.decodeMediaRequest(w, r)
	if !ok {
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	if err := m.authorizeOverrides(r, id, req); err != nil {
		m.api.Problem(w, r, err)
		return
	}
	// One walk of the disk at a time. A second run started while the
	// screen's own is in flight would halve both and answer nothing new
	// (TBY-MED-031), so the caller is told to read the verdict of the one
	// already running rather than being silently queued behind it.
	if m.opts.Gate != nil {
		if s := m.opts.Gate.Status(); s.Running {
			m.api.Problem(w, r, taxonomy.New(taxonomy.CodeMediaVerificationRunning,
				taxonomy.Params{"stage": stageOf(&s)}))
			return
		}
	}
	// FR-094 (B-073): a waived verification can end pushable and open the
	// serving gate, so the waiver is recorded with the actor and the real
	// network origin here, as on an import — after the refusals above,
	// which apply nothing, and before the verification it lowers a guard
	// for.
	m.auditOverrides(r, id.Name, req, audit.MediaOperationVerify)
	rep, verr := m.opts.Engine.VerifyMedia(r.Context(), m.api.logger, engine.MediaOptions{
		AllowZoneMismatch: req.AllowZoneMismatch,
		AllowStale:        req.AllowStale,
	})
	if verr != nil {
		var te *taxonomy.Error
		if errors.As(verr, &te) {
			m.api.Problem(w, r, te)
			return
		}
		m.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(verr))
		return
	}
	// 200 with the report whatever the verdict: a blocked medium is a
	// successful verification of a bad medium, and answering an error
	// status would deny the caller the very document that says why.
	m.api.JSON(w, http.StatusOK, rep)
}

// importMedia serves POST /api/v1/media/import: enqueue the FR-052
// journey and answer 201 with the task.
func (m *mediaAPI) importMedia(w http.ResponseWriter, r *http.Request) {
	if m.opts.Engine == nil || m.opts.Queue == nil {
		m.api.Problem(w, r, notADestination())
		return
	}
	// B-077: an instance with no zone identity cannot run the import it
	// would enqueue (the engine refuses it, TBY-CFG-001), so it refuses at
	// the door — before any waiver is recorded, any task created or any
	// media.import success written — exactly as the verification does.
	if m.opts.Engine.Zone() == "" {
		m.api.Problem(w, r, notADestination())
		return
	}
	// B-079: likewise without destination.registry — verification needs
	// none, the push does, and the task would only fail with this code
	// after a 201 and a media.import success record.
	if m.opts.Engine.Destination() == nil {
		m.api.Problem(w, r, noDestinationRegistry())
		return
	}
	req, ok := m.decodeMediaRequest(w, r)
	if !ok {
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	if err := m.authorizeOverrides(r, id, req); err != nil {
		m.api.Problem(w, r, err)
		return
	}

	// FR-094: the waiver is recorded with the actor and the real network
	// origin BEFORE the task exists, so a queue that refuses the task
	// still leaves the attempt in the trail.
	overrides := m.auditOverrides(r, id.Name, req, audit.MediaOperationImport)

	task, cerr := m.opts.Queue.Create(tasks.TypeMediaImport, m.opts.StorageRoot, id.Name, nil,
		tasks.WithMediaOverrides(overrides...))
	if cerr != nil {
		m.auditImport(r, id.Name, audit.OutcomeFailure)
		m.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(cerr))
		return
	}
	m.auditImport(r, id.Name, audit.OutcomeSuccess)
	if snap, ok := m.opts.Queue.Get(task.ID); ok {
		task = snap
	}
	m.api.JSON(w, http.StatusCreated, map[string]any{"task": newTaskJSON(task)})
}

// authorizeOverrides enforces the half of FR-054 that a role floor on the
// endpoint cannot express: verifying and importing are operator actions,
// but WAIVING a guard is an administrator's.
//
// The refusal is the taxonomized role refusal, the same one the middleware
// would have produced, so a caller sees one shape of "you may not" rather
// than two.
func (m *mediaAPI) authorizeOverrides(r *http.Request, id auth.Identity, req *mediaRequest) *taxonomy.Error {
	if !req.AllowZoneMismatch && !req.AllowStale {
		return nil
	}
	if id.Role.AtLeast(auth.RoleAdmin) {
		return nil
	}
	audit.Log(r.Context(), m.api.logger, &audit.Event{
		Actor: id.Name, Action: audit.ActionMediaOverride, Target: "denied",
		Outcome: audit.OutcomeDenied, Origin: auth.ClientOrigin(r),
	})
	return taxonomy.New(taxonomy.CodeRoleDenied, taxonomy.Params{"role": string(auth.RoleAdmin)})
}

// auditOverrides writes the FR-094 record of each waiver req applies to
// operation — one media.override success per guard, with the actor, the
// network origin, and a detail naming the operation, the zone and the
// medium — and returns the guards it recorded, in the form a task
// carries them. It is called once the waiver has been authorized and the
// operation is about to run under it, never for a refused request.
func (m *mediaAPI) auditOverrides(r *http.Request, actor string, req *mediaRequest, operation string) []string {
	var overrides []string
	if req.AllowZoneMismatch {
		overrides = append(overrides, tasks.OverrideZone)
	}
	if req.AllowStale {
		overrides = append(overrides, tasks.OverrideFreshness)
	}
	if len(overrides) == 0 {
		return nil
	}
	detail := audit.MediaOverrideDetail(operation, m.opts.Engine.Zone(), m.opts.Engine.MediaSummary().MediaID)
	for _, guard := range overrides {
		audit.Log(r.Context(), m.api.logger, &audit.Event{
			Actor: actor, Action: audit.ActionMediaOverride, Target: guard,
			Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r), Detail: detail,
		})
	}
	return overrides
}

// decodeMediaRequest reads the optional body. An absent one is the
// ordinary case — no waiver — and not an error. On failure the request
// has been answered.
func (m *mediaAPI) decodeMediaRequest(w http.ResponseWriter, r *http.Request) (*mediaRequest, bool) {
	req := &mediaRequest{}
	if !m.api.decodeJSON(w, r, maxMediaRequest, req, true, bodyInvalid) {
		return nil, false
	}
	return req, true
}

// stageOf names the phase a running verification is in, for the refusal
// that tells a second caller to wait for it.
func stageOf(s *mediagate.Status) string {
	if s.Progress == nil {
		return string(media.StageManifest)
	}
	return string(s.Progress.Stage)
}

// notADestination is the answer of an instance that has no medium to
// operate on: no zone identity, no engine, or no queue.
func notADestination() *taxonomy.Error {
	return taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
		"detail": "this instance is not configured as the destination side of a physical transfer " +
			"(FR-052): set \"zone:\" to the identity of the zone it serves",
	})
}

// noDestinationRegistry is the answer of a destination instance asked to
// import with nowhere to push: the same refusal the engine gives
// (internal/engine/mediaimport.go), given at the door.
func noDestinationRegistry() *taxonomy.Error {
	return taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
		"detail": "no destination registry is configured (destination.registry, FR-052): " +
			"a medium can be verified without one, but not imported",
	})
}

// auditImport emits the FR-094 record of the operation itself.
func (m *mediaAPI) auditImport(r *http.Request, actor, outcome string) {
	audit.Log(r.Context(), m.api.logger, &audit.Event{
		Actor: actor, Action: audit.ActionMediaImport, Target: m.opts.StorageRoot,
		Outcome: outcome, Origin: auth.ClientOrigin(r),
	})
}

// slip serves GET /api/v1/media/slip: the transfer slip (R-07), in a
// format (html, the default; text; json) and a language (en or fr; the
// Accept-Language header when absent). It works on both sides of a
// transfer — on a source it summarizes what the store delivers — so it
// does not require a destination engine.
//
// It hashes nothing: it reads the manifest, the last verdict the gate
// holds, and the scan reports of the task documents. A verdict is
// therefore the last one reached, whichever surface produced it; a slip
// drawn before any verification says no verification was run rather than
// running one.
//
// The document is unsigned and says so at its head and its foot (ADR-0006,
// decision 10): the JSON form states "signed": false as well.
func (m *mediaAPI) slip(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	switch format {
	case "":
		format = slip.FormatHTML
	case slip.FormatHTML, slip.FormatText, slip.FormatJSON:
	default:
		m.api.Problem(w, r, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
			"file": "the query", "path": "format", "constraint": "must be html, text or json",
		}))
		return
	}
	lang := q.Get("lang")
	switch lang {
	case "":
		lang = slip.NormalizeLang(r.Header.Get("Accept-Language"))
	case "en", "fr":
	default:
		m.api.Problem(w, r, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
			"file": "the query", "path": "lang", "constraint": "must be en or fr",
		}))
		return
	}

	inst := &slip.Instance{Version: buildinfo.Version(), Root: m.opts.StorageRoot, Queue: m.opts.Queue}
	if m.opts.Engine != nil {
		sum := m.opts.Engine.MediaSummary()
		inst.Zone, inst.LastImport = m.opts.Engine.Zone(), sum.LastImport
	}
	if m.opts.Gate != nil {
		inst.Report = m.opts.Gate.Status().Report
	}
	inst.Scan = m.opts.Scan
	sl := inst.Slip()

	body, contentType, err := sl.Render(format, lang)
	if err != nil {
		m.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if format == slip.FormatHTML {
		// The page loads nothing: its own inline stylesheet is the only
		// source its policy allows.
		h.Set("Content-Security-Policy", slip.CSP())
	}
	if q.Get("download") == "1" {
		h.Set("Content-Disposition", `attachment; filename="`+sl.Filename(format)+`"`)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body) //nolint:gosec // G705: body is the slip package's rendering, escaped by html/template
}
