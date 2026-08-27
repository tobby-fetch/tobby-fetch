// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The Media screen (FR-062 amendment R-02): the one screen that walks a
// non-expert operator through a physical transfer, present on both sides
// of it.
//
// On the SOURCE side there is nothing to verify — the medium was written
// here, minutes ago — so the screen is the inventory summary and the
// handover instructions: what this store is, which zone it is addressed
// to, when it was resolved, what it delivers, how big it is.
//
// On the DESTINATION side the same summary opens a guided sequence,
// Verify → Report → Push, where each step unlocks the next and the Push
// control does not exist until a verdict does. Nothing here decides
// anything: the blocking order is normative in FR-054 and implemented once
// in internal/engine/mediaimport.go, the verdicts come from
// internal/media, and the serving gate lives in internal/mediagate. This
// file renders them. Every route it adds is the mirror of one under
// /api/v1/media (FR-061) and adds no engine behaviour of its own.
//
// Two things the screen must never let an operator believe.
//
//   - That it establishes authenticity. It does not, and neither does the
//     manifest: the manifest is unsigned and Tobby signs nothing
//     (ADR-0006, ADR-0007). Authenticity is the recipes' signatures
//     verified against THIS instance's trust roots, and the screen names
//     the fingerprint that verified each one so the claim is checkable
//     rather than decorative.
//   - That a verdict is a moment. Verifying a full medium is minutes of
//     I/O, so the step is a background run with live progress
//     (FR-054: "verification progress is displayed"), polled on the
//     canonical URL and auto-terminating the way every other polled zone
//     in this interface does (UI-SPEC §8.3, B-002/B-012).

package ui

import (
	"net/http"
	"sort"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/mediagate"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// MediaZone is the destination-side context of the screen: the zone this
// instance serves and the freshness record it holds for it (FR-052,
// R-28). *engine.Engine implements it; nil leaves the screen in its
// source-side shape, which is the correct rendering for an instance that
// imports no medium.
type MediaZone interface {
	Zone() string
	LastMediaImport() (media.ImportRecord, bool)
}

// The three stages R-02 asks to be rendered separately, in the order the
// requirement writes them. They are a VIEW of one report — verification
// takes them together — and naming them is the point: an operator who is
// told "the medium is blocked" learns nothing, and an operator told "the
// inventory checks out, the signatures check out, one ingredient digest
// does not" knows whether to re-copy the disk or call the source zone.
const (
	stageManifest   = "manifest"
	stageSignatures = "signatures"
	stageDigests    = "digests"
)

// Stage and verdict states, as the template compares them.
const (
	stateWaiting    = "waiting"
	statePassed     = "passed"
	stateFailed     = "failed"
	statePartial    = "partial"
	stateNotReached = "not-reached"
)

// mediaData feeds /media.
type mediaData struct {
	// Zone is the identity THIS instance serves; empty on a source side.
	Zone string
	// Destination says this instance is the receiving side (FR-052): it
	// has a zone identity, so it can tell whether a medium is addressed
	// to it.
	Destination bool
	// Root is the store directory the medium is.
	Root string

	// Medium is what the manifest claims, read without hashing anything.
	// Nil when the store carries no readable manifest — on the source
	// side that means "no synchronization has produced a medium yet", on
	// the destination side it is a refusal, carried in MediumErr.
	Medium *media.Manifest
	// MediumErr is the manifest's own refusal (R-19), localized.
	MediumErr *ErrView
	// NoMedium distinguishes "this store was never made into a medium"
	// from "the manifest is damaged": the first is the ordinary state of
	// a fresh mirror instance and gets an explanation, not an alarm.
	NoMedium bool

	// LastImport is the freshness record for the zone (R-28).
	LastImport *media.ImportRecord
	HasLast    bool

	// Guarded says this instance withholds /v2/ and /files/ until the
	// medium is verified (FR-054); Serving says they currently answer.
	Guarded bool
	Serving bool

	// Running, Progress and Percent drive the live progress of the
	// verification in flight.
	Running  bool
	Progress *media.Progress
	Percent  int

	// Report is the last verdict reached, whichever surface produced it.
	Report *media.Report
	// Verdict is Report.Verdict as a plain string for the template.
	Verdict string
	// Stages are the three R-02 stage verdicts.
	Stages []mediaStageView
	// Blocks are the medium-wide refusals, localized, each carrying
	// whether an administrator may waive it.
	Blocks []mediaBlockView
	// Recipes are the per-delivery rows: the manifest's list before any
	// verification, merged with the verdicts once there are some.
	Recipes []mediaRecipeView
	// Findings are the non-blocking observations, localized.
	Findings []mediaFindingView
	// FindingsShown bounds what is rendered; Findings can run to
	// thousands on a medium carrying an old delivery.
	FindingsMore int
	// Failure is why the last run reached no verdict at all.
	Failure *ErrView

	// Guided sequence. Step is the furthest step reached: 1 verify,
	// 2 report, 3 push.
	Step int
	// CanVerify and CanPush are the role gates, pre-computed because a
	// template cannot compare roles.
	CanVerify bool
	// CanOverride says the viewer may waive the two admin-overridable
	// guards (FR-054): the checkboxes exist only for them.
	CanOverride bool
	// PushReady says a verdict exists and cleared at least one delivery,
	// so the Push control is rendered AT ALL. Before that the control is
	// absent from the document — not disabled, absent: R-02's acceptance
	// is that it is unreachable until verification has completed.
	PushReady bool
	// ZoneWaived and StaleWaived keep the submitted waivers checked
	// across the verify → push sequence, so an administrator who waived a
	// guard to obtain a verdict does not have to remember to waive it
	// again to act on it.
	ZoneWaived, StaleWaived bool

	// ReportHref is the raw verification report, off the machine surface.
	ReportHref string
}

// mediaStageView is one of the three R-02 stage verdicts.
type mediaStageView struct {
	Key   string
	State string
	// Passed and Failed count deliveries, for the two per-recipe stages.
	Passed, Failed int
}

// mediaBlockView is one medium-wide refusal, localized.
type mediaBlockView struct {
	Err *ErrView
	// Overridable and Overridden are the FR-054 waiver state. A waived
	// block stays in the list: the report is also the evidence of what an
	// administrator let through (FR-094).
	Overridable bool
	Overridden  bool
	// Guard names the checkbox that waives it, when one does.
	Guard string
}

// mediaFindingView is one non-blocking observation.
type mediaFindingView struct {
	Err  *ErrView
	Path string
}

// mediaRecipeView is one delivery, before or after a verdict.
type mediaRecipeView struct {
	Name, Version string
	ID            string
	CookbookRepo  string
	ArtifactRepo  string
	Digest        string
	ResolvedAt    time.Time
	Ingredients   int
	Files         int
	Bytes         int64

	// Verified says a verdict exists for this delivery.
	Verified bool
	Pushable bool
	// Signature and Content are this delivery's halves of stages 2 and 3.
	Signature string
	Content   string
	// KeyFingerprint is the trust root that verified the signature —
	// the only thing on this screen that establishes authenticity.
	KeyFingerprint string
	// TrustScope and Unsigned make a relaxed posture visible (FR-033):
	// a delivery admitted without a signature says so, in words.
	TrustScope string
	Unsigned   bool
	// Err is the refusal, localized, and Path the file it names.
	Err  *ErrView
	Path string
}

// mediaScreen serves GET /media: the summary on both sides, and the
// guided sequence on the destination side. The polled body swaps on the
// same canonical URL, told apart by the HX-Target header (ADR-0015 §1),
// exactly like the task detail.
func (u *UI) mediaScreen(w http.ResponseWriter, r *http.Request) {
	d := u.mediaScreenData(r)
	if isFragment(r) && r.Header.Get("HX-Target") == "media-body" {
		u.render.Fragment(w, r, "media", "media-body", d)
		return
	}
	u.render.Page(w, r, "media", d)
}

// mediaScreenData assembles the screen. It reads the manifest and the gate
// and hashes nothing: the expensive walk is the Verify step, never a page
// render.
func (u *UI) mediaScreenData(r *http.Request) *mediaData {
	lang := requestLang(r)
	d := &mediaData{ReportHref: "/api/v1/media/verification"}
	if u.store != nil {
		d.Root = u.store.Root()
	}
	if u.mediaZone != nil {
		d.Zone = u.mediaZone.Zone()
		d.Destination = d.Zone != ""
		if rec, ok := u.mediaZone.LastMediaImport(); ok {
			d.LastImport, d.HasLast = &rec, true
		}
	}
	if id, ok := auth.IdentityFrom(r.Context()); ok {
		d.CanVerify = id.Role.AtLeast(auth.RoleOperator)
		d.CanOverride = id.Role.AtLeast(auth.RoleAdmin)
	}
	d.ZoneWaived = r.URL.Query().Get("allowZoneMismatch") == "1"
	d.StaleWaived = r.URL.Query().Get("allowStale") == "1"

	d.readManifest(lang, d.Root)
	d.readGate(lang, u.mediaGate)
	d.mergeVerdicts(lang)
	d.settleStep()
	return d
}

// readManifest fills the medium's own claims.
func (d *mediaData) readManifest(lang, root string) {
	if root == "" {
		d.NoMedium = true
		return
	}
	m, block, err := media.ReadManifest(root)
	switch {
	case err != nil:
		d.NoMedium = true
		d.MediumErr = errView(lang, taxonomy.New(taxonomy.CodeStoreRead,
			taxonomy.Params{"detail": err.Error()}))
		return
	case block != nil:
		// A missing manifest is the ordinary state of a store no
		// synchronization has finished on. It is only a refusal on the
		// destination side, where a medium was expected.
		d.NoMedium = block.Code == taxonomy.CodeMediaManifestMissing
		if !d.NoMedium || d.Destination {
			d.MediumErr = errView(lang, block.Error())
		}
		return
	}
	// The inventory is thousands of entries and no surface renders it;
	// dropping it here keeps the page's working set proportional to what
	// it shows rather than to the size of the disk.
	m.Inventory = nil
	d.Medium = m
	for i := range m.Recipes {
		rc := &m.Recipes[i]
		d.Recipes = append(d.Recipes, mediaRecipeView{
			Name: rc.Name, Version: rc.Version, ID: rc.Name + "@" + rc.Version,
			CookbookRepo: rc.CookbookRepo, ArtifactRepo: rc.ArtifactRepo,
			Digest: rc.Digest, ResolvedAt: rc.ResolvedAt,
			Ingredients: len(rc.Ingredients),
			Signature:   stateWaiting, Content: stateWaiting,
		})
	}
	sort.SliceStable(d.Recipes, func(a, b int) bool { return d.Recipes[a].ID < d.Recipes[b].ID })
}

// readGate copies the serving gate's state onto the screen.
func (d *mediaData) readGate(lang string, g *mediagate.Gate) {
	if g == nil {
		return
	}
	s := g.Status()
	d.Guarded, d.Serving, d.Running = s.Guarded, s.Serving, s.Running
	d.Progress, d.Report = s.Progress, s.Report
	if s.Failure != nil {
		d.Failure = errView(lang, s.Failure.Error())
	}
	// The percentage is the gate's, not the screen's: the API mirror
	// serves the same number, and a progress bar that disagrees between
	// the two surfaces is worse than none (FR-061).
	d.Percent = s.Percent()
}

// mergeVerdicts folds the report onto the manifest's recipe list and
// derives the three stage verdicts.
func (d *mediaData) mergeVerdicts(lang string) {
	if d.Report == nil {
		return
	}
	rep := d.Report
	d.Verdict = string(rep.Verdict)

	for i := range rep.Blocks {
		b := &rep.Blocks[i]
		d.Blocks = append(d.Blocks, mediaBlockView{
			Err: errView(lang, b.Error()), Overridable: b.Overridable,
			Overridden: b.Overridden, Guard: guardOf(b.Code),
		})
	}
	const maxFindings = 25
	for i := range rep.Findings {
		if len(d.Findings) == maxFindings {
			d.FindingsMore = len(rep.Findings) - maxFindings
			break
		}
		f := &rep.Findings[i]
		d.Findings = append(d.Findings, mediaFindingView{Err: errView(lang, f.Error()), Path: f.Path})
	}

	byID := make(map[string]*media.RecipeVerdict, len(rep.Recipes))
	for i := range rep.Recipes {
		byID[rep.Recipes[i].Name+"@"+rep.Recipes[i].Version] = &rep.Recipes[i]
	}
	// A report can name a delivery the manifest read on THIS render does
	// not (the medium was swapped between the two). Keeping the report's
	// row is the honest rendering: the verdict is about something, and
	// hiding it would leave a verdict count that does not add up.
	seen := make(map[string]bool, len(d.Recipes))
	for i := range d.Recipes {
		seen[d.Recipes[i].ID] = true
		if v, ok := byID[d.Recipes[i].ID]; ok {
			applyVerdict(lang, &d.Recipes[i], v)
		}
	}
	for i := range rep.Recipes {
		v := &rep.Recipes[i]
		id := v.Name + "@" + v.Version
		if seen[id] {
			continue
		}
		row := mediaRecipeView{
			Name: v.Name, Version: v.Version, ID: id,
			CookbookRepo: v.CookbookRepo, ArtifactRepo: v.ArtifactRepo,
			Digest: v.Digest, ResolvedAt: v.ResolvedAt,
		}
		applyVerdict(lang, &row, v)
		d.Recipes = append(d.Recipes, row)
	}
	sort.SliceStable(d.Recipes, func(a, b int) bool { return d.Recipes[a].ID < d.Recipes[b].ID })

	d.Stages = stageViews(d)
}

// applyVerdict folds one delivery's verdict onto its row and splits the
// refusal between the two per-recipe stages.
func applyVerdict(lang string, row *mediaRecipeView, v *media.RecipeVerdict) {
	row.Verified, row.Pushable = true, v.Pushable
	row.KeyFingerprint, row.TrustScope, row.Unsigned = v.KeyFingerprint, v.TrustScope, v.Unsigned
	row.Files, row.Bytes = v.Files, v.Bytes
	if v.Pushable {
		row.Signature, row.Content = statePassed, statePassed
		return
	}
	if v.Reason == nil {
		row.Signature, row.Content = stateFailed, stateFailed
		return
	}
	row.Err, row.Path = errView(lang, v.Reason.Error()), v.Reason.Path
	if v.Reason.Code == taxonomy.CodeSignature {
		// The content walk ran first and cleared: only the signature
		// failed, and the remedy is a trust root, not a re-copy.
		row.Content, row.Signature = statePassed, stateFailed
		return
	}
	// A delivery whose content failed never reached its signature check
	// (internal/media/verify.go). Saying "not reached" rather than
	// "failed" is what keeps an operator from hunting a key problem that
	// does not exist.
	row.Content, row.Signature = stateFailed, stateNotReached
}

// stageViews derives the three R-02 stage verdicts from the report.
func stageViews(d *mediaData) []mediaStageView {
	manifest := mediaStageView{Key: stageManifest, State: statePassed}
	for i := range d.Blocks {
		if !d.Blocks[i].Overridden {
			manifest.State = stateFailed
		}
	}
	sig := mediaStageView{Key: stageSignatures}
	dig := mediaStageView{Key: stageDigests}
	for i := range d.Recipes {
		row := &d.Recipes[i]
		if !row.Verified {
			continue
		}
		count(&sig, row.Signature)
		count(&dig, row.Content)
	}
	sig.State, dig.State = settleStage(&sig), settleStage(&dig)
	if manifest.State == stateFailed {
		// A medium blocked as a whole was never walked, so the two
		// per-recipe stages have nothing to report rather than a pass.
		if sig.Passed+sig.Failed == 0 {
			sig.State = stateNotReached
		}
		if dig.Passed+dig.Failed == 0 {
			dig.State = stateNotReached
		}
	}
	return []mediaStageView{manifest, dig, sig}
}

// count tallies one delivery into a stage.
func count(s *mediaStageView, state string) {
	switch state {
	case statePassed:
		s.Passed++
	case stateFailed:
		s.Failed++
	}
}

// settleStage turns a stage's tally into its verdict.
func settleStage(s *mediaStageView) string {
	switch {
	case s.Passed+s.Failed == 0:
		return stateNotReached
	case s.Failed == 0:
		return statePassed
	case s.Passed == 0:
		return stateFailed
	default:
		return statePartial
	}
}

// settleStep decides how far the guided sequence has got, and therefore
// what the screen offers.
//
// The Push control is gated here, once, on one condition: a verdict
// exists and it cleared at least one delivery. The template never
// re-derives it — R-02's acceptance is that the control is unreachable
// until verification has completed, and a condition spelled out in two
// places is a condition that will disagree with itself.
func (d *mediaData) settleStep() {
	d.Step = 1
	if !d.Destination {
		d.Step = 0
		return
	}
	if d.Report == nil {
		return
	}
	d.Step = 2
	for i := range d.Recipes {
		if d.Recipes[i].Verified && d.Recipes[i].Pushable {
			d.PushReady = true
		}
	}
	if d.Report.Verdict == media.VerdictBlocked {
		// Nothing is pushed off a blocked medium, not even a delivery
		// that would have verified (R-19, engine/mediaimport.go).
		d.PushReady = false
	}
	if d.PushReady {
		d.Step = 3
	}
}

// guardOf names the waiver checkbox that reopens one blocking code. Only
// the two anti-accident guards over an unsigned claim ever have one
// (FR-054): an integrity verdict has no override anywhere.
func guardOf(code taxonomy.Code) string {
	switch code {
	case taxonomy.CodeMediaZoneMismatch:
		return "allowZoneMismatch"
	case taxonomy.CodeMediaStale:
		return "allowStale"
	default:
		return ""
	}
}

// mediaVerify serves POST /media/verify: start the FR-054 verification in
// the background and answer the screen in its running state.
//
// Background, not synchronous: a full medium is minutes of I/O, and a
// request that held the browser for that long would time out somewhere
// between the two. The screen polls the canonical URL from there, and the
// polling attributes stop being emitted the moment the run settles
// (UI-SPEC §8.3).
func (u *UI) mediaVerify(w http.ResponseWriter, r *http.Request) {
	opts := mediagate.Options{
		AllowZoneMismatch: r.PostFormValue("allowZoneMismatch") != "",
		AllowStale:        r.PostFormValue("allowStale") != "",
	}
	id, _ := auth.IdentityFrom(r.Context())
	if e := u.authorizeMediaWaivers(r, id, opts); e != nil {
		u.mediaRefusal(w, r, opts, e)
		return
	}
	if u.mediaGate == nil {
		u.mediaRefusal(w, r, opts, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "this instance holds no transported medium to verify (FR-052)",
		}))
		return
	}
	if e := u.mediaGate.Start(opts); e != nil {
		u.mediaRefusal(w, r, opts, e)
		return
	}
	d := u.mediaScreenData(r)
	d.ZoneWaived, d.StaleWaived = opts.AllowZoneMismatch, opts.AllowStale
	if isFragment(r) {
		u.render.Fragment(w, r, "media", "media-body", d)
		return
	}
	u.render.Page(w, r, "media", d)
}

// mediaImport serves POST /media/import: enqueue the FR-052 journey and
// send the operator to the task that carries it.
//
// It enqueues and nothing more. The engine re-verifies before it pushes
// anything, unconditionally (FR-054), so this handler deliberately does
// not re-check the verdict it just rendered: a screen that decided
// whether a push is safe would be a second implementation of the rule,
// and the second one is the one that will be wrong.
func (u *UI) mediaImport(w http.ResponseWriter, r *http.Request) {
	opts := mediagate.Options{
		AllowZoneMismatch: r.PostFormValue("allowZoneMismatch") != "",
		AllowStale:        r.PostFormValue("allowStale") != "",
	}
	id, _ := auth.IdentityFrom(r.Context())
	if e := u.authorizeMediaWaivers(r, id, opts); e != nil {
		u.mediaRefusal(w, r, opts, e)
		return
	}
	if u.queue == nil || u.mediaZone == nil || u.mediaZone.Zone() == "" {
		u.mediaRefusal(w, r, opts, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "this instance is not configured as the destination side of a physical transfer " +
				"(FR-052): set \"zone:\" to the identity of the zone it serves",
		}))
		return
	}

	var overrides []string
	if opts.AllowZoneMismatch {
		overrides = append(overrides, tasks.OverrideZone)
	}
	if opts.AllowStale {
		overrides = append(overrides, tasks.OverrideFreshness)
	}
	// FR-094: the waiver is recorded with the actor and the real network
	// origin BEFORE the task exists, so a queue that refuses the task
	// still leaves the attempt in the trail — the same order the API
	// mirror uses (internal/api/media.go).
	for _, guard := range overrides {
		audit.Log(r.Context(), u.logger, &audit.Event{
			Actor: id.Name, Action: audit.ActionMediaOverride, Target: guard,
			Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r),
		})
	}

	root := ""
	if u.store != nil {
		root = u.store.Root()
	}
	t, err := u.queue.Create(tasks.TypeMediaImport, root, id.Name, nil,
		tasks.WithMediaOverrides(overrides...))
	if err != nil {
		u.auditMediaImport(r, id.Name, root, audit.OutcomeFailure)
		u.mediaRefusal(w, r, opts, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	u.auditMediaImport(r, id.Name, root, audit.OutcomeSuccess)
	u.redirectTo(w, r, "/tasks/"+t.ID)
}

// authorizeMediaWaivers enforces the half of FR-054 a role floor on the
// route cannot express: verifying and importing are operator actions, but
// WAIVING one of the two guards is an administrator's. Same rule, same
// taxonomy entry and same audit record as the API mirror — stated twice
// because the two surfaces are two doors, not because the rule is two
// rules.
func (u *UI) authorizeMediaWaivers(r *http.Request, id auth.Identity, opts mediagate.Options) *taxonomy.Error {
	if !opts.AllowZoneMismatch && !opts.AllowStale {
		return nil
	}
	if id.Role.AtLeast(auth.RoleAdmin) {
		return nil
	}
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: id.Name, Action: audit.ActionMediaOverride, Target: "denied",
		Outcome: audit.OutcomeDenied, Origin: auth.ClientOrigin(r),
	})
	return taxonomy.New(taxonomy.CodeRoleDenied, taxonomy.Params{"role": string(auth.RoleAdmin)})
}

func (u *UI) auditMediaImport(r *http.Request, actor, target, outcome string) {
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: actor, Action: audit.ActionMediaImport, Target: target,
		Outcome: outcome, Origin: auth.ClientOrigin(r),
	})
}

// mediaRefusal re-renders the screen with the taxonomized block and the
// entry's real HTTP status (ADR-0015 §6), the submitted waivers preserved.
func (u *UI) mediaRefusal(w http.ResponseWriter, r *http.Request, opts mediagate.Options, e *taxonomy.Error) {
	d := u.mediaScreenData(r)
	d.ZoneWaived, d.StaleWaived = opts.AllowZoneMismatch, opts.AllowStale
	v := u.render.view(r, d)
	v.Err = errView(v.Lang, e)
	if isFragment(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(v.Err.Status)
		u.render.Fragment(w, r, "media", "media-body", d)
		return
	}
	u.render.render(w, r, "media", v.Err.Status, v)
}
