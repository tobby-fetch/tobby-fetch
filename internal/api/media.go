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
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/mediagate"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// maxMediaRequest bounds the request body: two booleans and nothing else.
const maxMediaRequest = 4 << 10

// MediaVerifier is the destination-side engine as this surface needs it.
type MediaVerifier interface {
	Zone() string
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
	if m.opts.Engine == nil {
		m.api.Problem(w, r, notADestination())
		return
	}
	req, err := decodeMediaRequest(r)
	if err != nil {
		m.api.Problem(w, r, err)
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
	req, err := decodeMediaRequest(r)
	if err != nil {
		m.api.Problem(w, r, err)
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	if err := m.authorizeOverrides(r, id, req); err != nil {
		m.api.Problem(w, r, err)
		return
	}

	var overrides []string
	if req.AllowZoneMismatch {
		overrides = append(overrides, tasks.OverrideZone)
	}
	if req.AllowStale {
		overrides = append(overrides, tasks.OverrideFreshness)
	}
	// FR-094: the waiver is recorded with the actor and the real network
	// origin BEFORE the task exists, so a queue that refuses the task
	// still leaves the attempt in the trail.
	for _, guard := range overrides {
		audit.Log(r.Context(), m.api.logger, &audit.Event{
			Actor: id.Name, Action: audit.ActionMediaOverride, Target: guard,
			Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r),
		})
	}

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

// decodeMediaRequest reads the optional body. An absent one is the
// ordinary case — no waiver — and not an error.
func decodeMediaRequest(r *http.Request) (*mediaRequest, *taxonomy.Error) {
	req := &mediaRequest{}
	err := json.NewDecoder(io.LimitReader(r.Body, maxMediaRequest)).Decode(req)
	if err == nil || errors.Is(err, io.EOF) {
		return req, nil
	}
	return nil, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
		"file": "the request body", "path": "-", "constraint": err.Error(),
	})
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

// auditImport emits the FR-094 record of the operation itself.
func (m *mediaAPI) auditImport(r *http.Request, actor, outcome string) {
	audit.Log(r.Context(), m.api.logger, &audit.Event{
		Actor: actor, Action: audit.ActionMediaImport, Target: m.opts.StorageRoot,
		Outcome: outcome, Origin: auth.ClientOrigin(r),
	})
}
