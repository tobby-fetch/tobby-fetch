// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package mediagate closes the serving half of FR-054.
//
// FR-054 is one sentence with three verbs: "destination-side verification
// … SHALL precede any push, any serving, and any local write". Two of the
// three were enforced — the engine verifies before it pushes and before it
// writes (internal/engine/mediaimport.go) — and SERVING was not. An
// instance pointed at a transported store mounted /v2/ and /files/ at
// startup and handed out its blobs to anyone who asked, before a single
// digest had been recomputed. This package is that gap, closed.
//
// # What it guards, and what it deliberately does not
//
// The gate applies to exactly one shape of instance: a DESTINATION side
// (an instance told which zone it serves, FR-052 — the configuration field
// a source-side instance never sets) whose store carries a media manifest
// (meta/media.json — the mark of a store a mirror synchronization
// finished and handed over). Everything else serves normally and pays
// nothing:
//
//   - a passthrough instance: its store is a transit cache, not a
//     delivery, and it has no zone identity of its own;
//   - a mirror instance on the SOURCE side: its store carries a manifest
//     too — it wrote it — but the medium is its own output, not something
//     that changed hands. It has no zone configured, which is precisely
//     how the requirement distinguishes the two sides;
//   - a destination instance whose store is not a medium at all: nothing
//     arrived, so there is nothing unverified to withhold.
//
// # What opens it
//
// One thing: a verification, in this process, whose verdict is
// VerdictPushable. Not "partial".
//
// R-19 made the PUSH decision per recipe, so a partially damaged medium
// still delivers its intact recipes into the zone registry — that is the
// whole point of carrying several deliveries on one medium. Serving is
// not that decision. /v2/ and /files/ hand out blobs, blobs are shared
// between deliveries, and a blob a blocked recipe reaches is exactly the
// byte range that failed its digest. There is no honest way to serve half
// a store, so a medium that did not come out whole serves nothing, and the
// operator is told to push the intact recipes into the zone registry —
// which then serves them, verified, from content that arrived by the
// checked path.
//
// The gate holds no persistent record and re-closes on every restart. That
// is not an oversight: a cached verdict says "these bytes were right once",
// and the question the gate asks is whether they are right now. Re-hashing
// a disk on restart is minutes; serving unverified content because a file
// in the state directory said it was fine once is unbounded.
//
// # There is no opt-out
//
// FR-075 requires every security-reducing setting to be an explicit,
// visible, logged opt-in — and, first, requires asking whether it should
// exist at all. This one should not. The overrides FR-054 sanctions are
// the two anti-accident guards over an UNSIGNED claim (a medium addressed
// to another zone, a medium older than the last import): an administrator
// may waive those, audited, and doing so lets the verification run to a
// real verdict, which is what opens the gate. An integrity or signature
// verdict has no override anywhere in this product (R-19), and a
// "serve-anyway" switch would be exactly that override, wearing a
// different name. An operator who needs the content of a damaged medium
// re-copies the medium.
package mediagate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"golang.org/x/text/language"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Screen is where an operator goes to open the gate. It appears verbatim
// in every refusal (FR-054/R-02: a refusal states the course of action,
// not a code alone), so it lives here rather than being spelled out at
// each of the four places that name it.
const Screen = "/media"

// Options parameterizes one verification the gate runs. It restates the
// two waivers of FR-054 rather than importing the engine's option struct:
// the gate is wiring, and a package that can be tested without an engine
// is a package whose refusals can be tested at all.
type Options struct {
	// AllowZoneMismatch and AllowStale waive the two admin-overridable
	// guards. The CALLER audits them (FR-094): the actor and the network
	// origin belong to whoever authenticated them.
	AllowZoneMismatch bool
	AllowStale        bool
}

// Verify runs one verification of the medium, reporting progress. It is
// engine.VerifyMedia, injected — see internal/cli/serve.go.
type Verify func(ctx context.Context, opts Options, progress func(media.Progress)) (*media.Report, error)

// Gate is the destination instance's media session: whether the content
// surfaces answer, the verification currently walking the medium, and the
// verdict the last one reached.
type Gate struct {
	// base outlives any request: a verification takes minutes and the
	// browser tab that started it may be closed a second later. It is the
	// instance's own context, so a shutdown cancels the walk (FR-093).
	base   context.Context
	root   string
	zone   string
	verify Verify
	logger *slog.Logger

	// guarded is fixed at startup: this instance is a destination side and
	// its store is a medium. False means every method below is inert and
	// Guard returns the handler it was given.
	guarded bool
	// mediaID identifies the medium in every refusal, so an operator
	// holding three disks knows which one is being talked about. Empty
	// when the manifest could not be read — which is itself a refusal.
	mediaID string

	mu        sync.RWMutex
	running   bool
	started   time.Time
	progress  media.Progress
	report    *media.Report
	verdictAt time.Time
	// failure is why the last run produced no report at all — a store
	// that cannot be opened, a missing zone identity. Distinct from a
	// blocked verdict, which IS a report.
	failure *Failure
}

// Failure is a run that could not reach a verdict, in re-renderable form
// (ADR-0015 §4: code and parameters, never a sentence).
type Failure struct {
	Code   taxonomy.Code     `json:"code"`
	Params map[string]string `json:"params,omitempty"`
	At     time.Time         `json:"at"`
}

// Error rebuilds the taxonomy error a surface renders.
func (f *Failure) Error() *taxonomy.Error {
	if _, known := taxonomy.Lookup(f.Code); !known {
		return taxonomy.New(taxonomy.CodeInternal, nil)
	}
	p := make(taxonomy.Params, len(f.Params))
	for k, v := range f.Params {
		p[k] = v
	}
	return taxonomy.New(f.Code, p)
}

// Open builds the gate for one instance. root is the store directory and
// zone the identity this instance serves — empty on anything that is not
// a destination side (FR-052).
//
// ctx is the instance's lifetime, not a request's: a verification takes
// minutes and outlives the browser tab that asked for it, while a
// shutdown must still cancel it (FR-093).
//
// The gate is built BEFORE the engine that verifies for it, because the
// listener mounts /v2/ before the engine exists and a surface that is
// mounted unguarded for the length of a startup is a surface that is
// unguarded. SetVerify closes the loop once the engine is there; until it
// is, the gate refuses to serve and cannot start a run, which is the
// correct posture in both halves.
func Open(ctx context.Context, root, zone string, logger *slog.Logger) *Gate {
	g := &Gate{base: ctx, root: root, zone: zone, logger: logger}
	g.guarded = zone != "" && media.IsMedium(root)
	if !g.guarded {
		return g
	}
	// The medium's own identity, read once: it names the physical object
	// in every refusal, and an unreadable manifest is itself the answer.
	if m, _, err := media.ReadManifest(root); err == nil && m != nil {
		g.mediaID = m.MediaID
	}
	return g
}

// SetVerify installs the verification the gate runs — engine.VerifyMedia,
// which exists later in the startup sequence than the gate does.
func (g *Gate) SetVerify(verify Verify) { g.verify = verify }

// Guarded reports whether this instance withholds its content surfaces
// until the medium is verified.
func (g *Gate) Guarded() bool { return g.guarded }

// Serving reports whether /v2/ and /files/ answer. Always true on an
// instance that guards nothing.
func (g *Gate) Serving() bool {
	if !g.guarded {
		return true
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.cleared()
}

// cleared says the last verdict opened the gate. Callers hold the lock.
func (g *Gate) cleared() bool {
	return g.report != nil && g.report.Verdict == media.VerdictPushable
}

// Observe records the verdict of a verification — whichever surface ran
// it, and including the one inside a media import.
//
// It is the single funnel: engine.VerifyMedia calls it at the end of
// every verification it performs, so no path can reach a verdict without
// the gate hearing about it. The transition is logged at Info in both
// directions, because an instance that starts or stops serving a medium's
// content is an operational event and not a detail (FR-090).
func (g *Gate) Observe(rep *media.Report) {
	if rep == nil {
		return
	}
	// Recorded whatever this instance is: the gate is two things — the
	// media SESSION every destination-side surface reads, and the serving
	// gate a destination holding a medium enforces. A verdict is session
	// state, so an instance that guards nothing still remembers what it
	// last found; only the transition below is a serving event.
	g.mu.Lock()
	was := g.cleared()
	g.report, g.verdictAt, g.failure = rep, time.Now().UTC(), nil
	now := g.cleared()
	g.mu.Unlock()

	if !g.guarded || was == now {
		return
	}
	msg := "medium verified: the registry and the file surfaces are open"
	if !now {
		msg = "medium no longer cleared: the registry and the file surfaces are closed"
	}
	g.logger.LogAttrs(g.base, slog.LevelInfo, msg,
		slog.String("media_id", g.mediaID), slog.String("zone", g.zone),
		slog.String("verdict", string(rep.Verdict)),
		slog.String("requirement", "FR-054"))
}

// Start launches a verification in the background and returns immediately.
//
// Background because a full medium is minutes of I/O: a screen that
// blocked on it would be a frozen page, and FR-054 asks for progress to be
// displayed. The refusal is a taxonomy error the caller renders — one run
// at a time, because two walks over the same disk halve each other and
// answer nothing new.
func (g *Gate) Start(opts Options) *taxonomy.Error {
	// A destination instance may verify whatever its storage root holds,
	// medium or not: a store carrying no manifest comes back as the
	// TBY-MED-001 refusal, which is an answer an operator can act on.
	// Only WITHHOLDING is conditioned on the store being a medium.
	if g.zone == "" || g.verify == nil {
		return taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "this instance holds no transported medium to verify (FR-052): " +
				"set \"zone:\" to the identity of the zone it serves, and point storage.root at the medium",
		})
	}
	g.mu.Lock()
	if g.running {
		stage := string(g.progress.Stage)
		g.mu.Unlock()
		return taxonomy.New(taxonomy.CodeMediaVerificationRunning, taxonomy.Params{"stage": stage})
	}
	g.running, g.started = true, time.Now().UTC()
	g.progress = media.Progress{Stage: media.StageManifest}
	g.mu.Unlock()

	go g.run(opts)
	return nil
}

// run performs one verification and records its outcome.
//
// The verdict normally reaches the gate through Observe, which the engine
// calls at the end of EVERY verification — including the ones this gate
// did not start, such as the one inside a media import. run observes its
// own result anyway: Observe is idempotent on an unchanged verdict, and a
// gate that depended on someone else reporting the answer to a question
// it asked itself would be a gate with a wiring step to forget.
func (g *Gate) run(opts Options) {
	rep, err := g.verify(g.base, opts, g.onProgress)

	g.mu.Lock()
	g.running = false
	if err != nil {
		g.failure = failureOf(err)
	}
	g.mu.Unlock()

	if err == nil {
		g.Observe(rep)
	}

	if err != nil {
		g.logger.LogAttrs(g.base, slog.LevelError, "media verification did not complete",
			slog.String("media_id", g.mediaID), slog.String("error", err.Error()),
			slog.String("requirement", "FR-054"))
	}
}

// failureOf turns a run error into its re-renderable form. A taxonomized
// error keeps its code and parameters; anything else becomes the internal
// code, whose detail belongs in the log line run() writes beside it and
// not in a screen.
func failureOf(err error) *Failure {
	f := &Failure{Code: taxonomy.CodeInternal, At: time.Now().UTC()}
	var te *taxonomy.Error
	if errors.As(err, &te) {
		f.Code = te.Code()
		f.Params = make(map[string]string, len(te.ParamsMap()))
		for k, v := range te.ParamsMap() {
			f.Params[k] = fmt.Sprintf("%v", v)
		}
	}
	return f
}

// onProgress records one progress notification for the polled screen.
func (g *Gate) onProgress(p media.Progress) {
	g.mu.Lock()
	g.progress = p
	g.mu.Unlock()
}

// Status is the gate as every surface reads it: the screen, its API
// mirror, and the readiness detail.
type Status struct {
	// Guarded says this instance withholds its content surfaces until the
	// medium is verified (FR-054).
	Guarded bool `json:"guarded"`
	// Serving says /v2/ and /files/ currently answer.
	Serving bool `json:"serving"`
	// MediaID identifies the medium the verdict is about.
	MediaID string `json:"mediaId,omitempty"`
	// Running says a verification is walking the medium right now.
	Running bool `json:"running"`
	// StartedAt bounds that run.
	StartedAt time.Time `json:"startedAt,omitempty"`
	// Progress is the last notification of the run in progress.
	Progress *media.Progress `json:"progress,omitempty"`
	// Report is the last verdict reached, whichever surface produced it.
	Report *media.Report `json:"report,omitempty"`
	// VerdictAt dates that verdict.
	VerdictAt time.Time `json:"verdictAt,omitempty"`
	// Failure is why the last run produced no report at all.
	Failure *Failure `json:"failure,omitempty"`
}

// Percent is the progress of the run in flight, 0 to 100.
//
// Computed on BYTES rather than files, and computed here rather than in
// each surface: a medium is a handful of enormous blobs and a great many
// tiny link files, so a file counter sits at 98 % for most of the wait —
// and a progress bar that disagrees between the screen and the API is
// worse than none. Zero when nothing is running or the medium declares no
// volumetry.
func (s *Status) Percent() int {
	if s.Progress == nil || s.Progress.TotalBytes <= 0 {
		return 0
	}
	pct := int(s.Progress.Bytes * 100 / s.Progress.TotalBytes)
	switch {
	case pct < 0:
		return 0
	case pct > 100:
		// The totals are the medium's own declaration (see Progress), so
		// they can be wrong. A bar past its end is a rendering artefact
		// of trusting a claim, and it is clamped rather than believed.
		return 100
	}
	return pct
}

// Status snapshots the gate.
func (g *Gate) Status() Status {
	g.mu.RLock()
	defer g.mu.RUnlock()
	s := Status{
		Guarded: g.guarded, Serving: !g.guarded || g.cleared(),
		MediaID: g.mediaID, Running: g.running,
		Report: g.report, VerdictAt: g.verdictAt, Failure: g.failure,
	}
	if g.running {
		p := g.progress
		s.StartedAt, s.Progress = g.started, &p
	}
	return s
}

// Refusal is the taxonomy error a client asking a closed surface gets,
// or nil when the surface is open. surface is the path prefix refused —
// it is in the message because "/v2/ is closed" and "/files/ is closed"
// send an operator to two different places to check.
func (g *Gate) Refusal(surface string) *taxonomy.Error {
	if !g.guarded {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.cleared() {
		return nil
	}
	id := g.mediaID
	if id == "" {
		id = g.root
	}
	if g.report != nil {
		return taxonomy.New(taxonomy.CodeMediaNotCleared, taxonomy.Params{
			"surface": surface, "media": id,
			"verdict": string(g.report.Verdict), "screen": Screen,
		})
	}
	return taxonomy.New(taxonomy.CodeMediaUnverified, taxonomy.Params{
		"surface": surface, "media": id, "screen": Screen,
	})
}

// ReadyDetail is what /readyz says beyond "ok" (FR-092, ADR-0012).
//
// The instance stays READY, and that is the whole point: it is alive, its
// storage is writable, its configuration is valid, and its UI and API are
// serving — the operator needs every one of those to press Verify. What
// is not ready is the content of this particular medium, and a probe that
// answered 503 would take the instance out of rotation and remove the very
// screen that fixes it. So the status stays 200 and the body says which
// surfaces are closed and why: a probe an operator can read is worth more
// than a probe that lies in either direction.
func (g *Gate) ReadyDetail() string {
	if !g.guarded {
		return ""
	}
	if g.Serving() {
		return ""
	}
	return "media " + g.mediaID + " is not verified: /v2/ and /files/ are closed until it is (FR-054) — see " + Screen
}

// Guard withholds h until the medium is verified.
//
// surface names the prefix for the refusal message. render writes the
// refusal in the shape that surface's clients understand: an OCI error
// envelope for /v2/, plain text for /files/ — neither of which is a 404
// or a silent 503, because an operator who plugs in a disk and gets a
// blank page calls support instead of pressing Verify.
func (g *Gate) Guard(surface string, render func(http.ResponseWriter, *http.Request, *taxonomy.Error), h http.Handler) http.Handler {
	if !g.guarded {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refusal := g.Refusal(surface)
		if refusal == nil {
			h.ServeHTTP(w, r)
			return
		}
		g.logger.LogAttrs(r.Context(), slog.LevelWarn, "refused: the medium is not verified",
			slog.String("surface", surface), slog.String("path", r.URL.Path),
			slog.String("media_id", g.mediaID), slog.String("code", string(refusal.Code())),
			slog.String("requirement", "FR-054"))
		render(w, r, refusal)
	})
}

// RegistryRefusal writes a closed-gate refusal in the OCI Distribution
// error shape (FR-040): docker, helm, oras and skopeo all print the
// message of the first entry, so the operator running `docker pull`
// against a medium nobody verified reads the same sentence the screen
// shows — and the taxonomy code they can search for.
func RegistryRefusal(w http.ResponseWriter, r *http.Request, e *taxonomy.Error) {
	m := taxonomy.Localize(langOf(r), e)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]any{{
			// DENIED is the OCI code for "the operation is unauthorized
			// for this resource" — the closest standard entry to a
			// deliberate refusal, and the one clients render verbatim.
			"code":    "DENIED",
			"message": string(m.Code) + " — " + m.What,
			"detail":  map[string]string{"cause": m.Cause, "action": m.Action},
		}},
	})
}

// FilesRefusal writes a closed-gate refusal as plain text (FR-047): the
// clients of /files/ are apt, dnf and curl, which show a body and know
// nothing of problem documents.
func FilesRefusal(w http.ResponseWriter, r *http.Request, e *taxonomy.Error) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(taxonomy.Text(langOf(r), e)))
}

// matcher resolves Accept-Language onto the shipped catalogs.
var matcher = language.NewMatcher([]language.Tag{language.English, language.French})

// langOf negotiates the refusal's language on Accept-Language, the only
// preference a registry client or a package manager ever sends. The web
// interface has its own cookie-based negotiation (internal/ui); machine
// surfaces have this.
func langOf(r *http.Request) string {
	if r == nil {
		return "en"
	}
	tags, _, err := language.ParseAcceptLanguage(r.Header.Get("Accept-Language"))
	if err != nil || len(tags) == 0 {
		return "en"
	}
	if _, idx, _ := matcher.Match(tags...); idx == 1 {
		return "fr"
	}
	return "en"
}
