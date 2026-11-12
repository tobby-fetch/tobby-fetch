// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package slip builds the transfer slip (roadmap R-07): the printable,
// bilingual account of one physical transfer, derived from what the Media
// screen already shows — the medium's inventory summary, the verification
// report, the vulnerability-scan reports per recipe and element, and the
// reduced-assurance markers.
//
// The slip decides nothing and adds no engine behaviour. Every fact in it
// is read from a record some other part of Tobby already holds: the
// manifest (internal/media), the serving gate's last report
// (internal/mediagate), the per-item scan reports of the task documents
// (internal/tasks, internal/scan) and the assurance markers
// (internal/assurance). Build gathers them into one language-neutral
// document (Slip, which is also what the API serves as JSON); HTML and
// Text render it in a language, both from the same intermediate view so
// the two formats cannot say different things.
//
// What the slip is NOT. It is an aid for the people carrying a disk from
// one zone to another, and it is unsigned: Tobby holds no private key and
// signs nothing (ADR-0006, ADR-0007, decision 10). It therefore proves
// nothing about authenticity — authenticity is the recipes' signatures
// verified against the destination instance's own trust roots, and the
// manifest the slip summarizes is an unsigned claim (ADR-0016). The
// notice saying so opens and closes every rendering, in both formats and
// both languages, and cannot be switched off.
package slip

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/assurance"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Sides of a transfer.
const (
	SideSource      = "source"
	SideDestination = "destination"
)

// The three stages of destination-side verification, in the order the
// Media screen renders them (R-02).
const (
	StageManifest   = "manifest"
	StageDigests    = "digests"
	StageSignatures = "signatures"
)

// Stage and delivery states.
const (
	StatePassed     = "passed"
	StateFailed     = "failed"
	StatePartial    = "partial"
	StateNotReached = "not-reached"
)

// maxFindings bounds the non-blocking observations a slip lists: a medium
// carrying an old delivery can produce thousands, and a slip that runs to
// forty pages is a slip nobody reads. The remainder is counted.
const maxFindings = 25

// maxScanFindings bounds the findings listed under one scanned element:
// the worst first, the rest counted. The complete list is the task
// document's, served by the API.
const maxScanFindings = 10

// ScanContext is the scanning state of the instance at the time the slip
// is drawn: the policy in force and the database in use. It is what the
// interface's banners read.
type ScanContext struct {
	// Blocking is false when the policy in force is advisory.
	Blocking bool
	// Cause is the degraded mode — no usable scanner, or no database —
	// nil when both are usable.
	Cause *taxonomy.Error
	// Database is the database in use, nil when there is none.
	Database *scan.Database
	// MaxAge is scan.maxDbAge.
	MaxAge time.Duration
	// Unknown says the caller holds no live scanner state — a command run
	// on the host without a serving instance, an API wired without a
	// scanner. The slip then states that, rather than claim that no
	// database is in use.
	Unknown bool
}

// Input is everything Build needs. Every field is optional: a slip can be
// drawn for a source-side store that was never made into a medium, and says
// so.
type Input struct {
	// Version is the Tobby build drawing the slip.
	Version string
	// Now is the drawing time; zero is time.Now.
	Now time.Time
	// Zone is the identity this instance serves; empty on a source side.
	Zone string
	// Root is the store directory the medium is.
	Root string
	// Manifest is what the medium claims, nil when there is none.
	// NoMedium distinguishes "never made into a medium" from MediumErr,
	// the manifest's own refusal.
	Manifest  *media.Manifest
	NoMedium  bool
	MediumErr *taxonomy.Error
	// LastImport is the freshness record held for the zone (R-28).
	LastImport *media.ImportRecord
	// Report is the last verification verdict, nil when none was reached.
	Report *media.Report
	// Tasks are the task documents scan reports are read from: for each
	// element, the report of the most recent task that scanned it.
	Tasks []*tasks.Task
	// Scan is the scanning state of the instance.
	Scan ScanContext
}

// Problem is a taxonomy entry carried as data: a code and its parameters,
// never a sentence. Rendering localizes it.
type Problem struct {
	Code   taxonomy.Code     `json:"code"`
	Params map[string]string `json:"params,omitempty"`
	Path   string            `json:"path,omitempty"`
}

// Slip is the language-neutral transfer slip. It carries codes, states and
// numbers; the sentences are produced by the renderers, in the reader's
// language (FR-063).
type Slip struct {
	GeneratedAt time.Time `json:"generatedAt"`
	Version     string    `json:"version"`
	// Signed is always false. It is part of the document so that a
	// machine reading the JSON cannot mistake it for evidence either.
	Signed bool `json:"signed"`
	// Side is SideSource or SideDestination.
	Side string `json:"side"`
	// Zone is the identity this instance serves; empty on a source side.
	Zone string `json:"zone,omitempty"`
	Root string `json:"root,omitempty"`

	Medium     *Medium             `json:"medium,omitempty"`
	NoMedium   bool                `json:"noMedium,omitempty"`
	MediumErr  *Problem            `json:"mediumError,omitempty"`
	LastImport *media.ImportRecord `json:"lastImport,omitempty"`

	Verification *Verification `json:"verification,omitempty"`
	Scan         Scan          `json:"scan"`
	Assurance    []Assurance   `json:"assurance,omitempty"`
}

// Medium is the inventory summary: what the manifest claims, with the
// per-file inventory dropped (thousands of entries that no surface
// renders).
type Medium struct {
	MediaID     string         `json:"mediaId"`
	Zone        string         `json:"zone"`
	MediaFormat int            `json:"mediaFormat"`
	StoreFormat int            `json:"storeFormat"`
	ProducedBy  media.Producer `json:"producedBy"`
	ResolvedAt  time.Time      `json:"resolvedAt"`
	WrittenAt   time.Time      `json:"writtenAt"`
	Totals      media.Totals   `json:"totals"`
	Recipes     []Recipe       `json:"recipes"`
}

// Recipe is one delivery of the medium.
type Recipe struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Version     string             `json:"version"`
	Digest      string             `json:"digest"`
	ResolvedAt  time.Time          `json:"resolvedAt"`
	Ingredients []media.Ingredient `json:"ingredients"`
	// Assurance is the manifest's marker for the delivery: the source
	// zone's claim on a destination side, the instance's own record on a
	// source side. Nil is full assurance.
	Assurance *assurance.Marker `json:"assurance,omitempty"`
}

// Verification is the destination-side verdict (FR-054, R-02).
type Verification struct {
	Verdict    media.Verdict         `json:"verdict"`
	StartedAt  time.Time             `json:"startedAt"`
	FinishedAt time.Time             `json:"finishedAt"`
	Checked    media.Totals          `json:"checked"`
	Zone       media.ZoneCheck       `json:"zone"`
	Freshness  *media.FreshnessCheck `json:"freshness,omitempty"`
	Blocks     []Block               `json:"blocks,omitempty"`
	Stages     []Stage               `json:"stages"`
	Recipes    []Delivery            `json:"recipes,omitempty"`
	Findings   []Problem             `json:"findings,omitempty"`
	// FindingsMore counts the observations beyond the ones listed.
	FindingsMore int `json:"findingsMore,omitempty"`
}

// Block is one medium-wide refusal.
type Block struct {
	Problem
	Overridable bool `json:"overridable"`
	Overridden  bool `json:"overridden,omitempty"`
}

// Stage is one of the three R-02 stage verdicts.
type Stage struct {
	Key    string `json:"key"`
	State  string `json:"state"`
	Passed int    `json:"passed,omitempty"`
	Failed int    `json:"failed,omitempty"`
}

// Delivery is one recipe's verification verdict.
type Delivery struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	Pushable  bool     `json:"pushable"`
	Signature string   `json:"signature"`
	Content   string   `json:"content"`
	Reason    *Problem `json:"reason,omitempty"`
	// KeyFingerprint is the trust root that verified the signature — the
	// only thing in the slip that establishes authenticity, and only
	// because the destination instance checked it.
	KeyFingerprint  string            `json:"keyFingerprint,omitempty"`
	TrustScope      string            `json:"trustScope,omitempty"`
	Unsigned        bool              `json:"unsigned,omitempty"`
	Assurance       *assurance.Marker `json:"assurance,omitempty"`
	SourceAssurance *assurance.Marker `json:"sourceAssurance,omitempty"`
	Files           int               `json:"files"`
	Bytes           int64             `json:"bytes"`
}

// Scan is the vulnerability-scan section: per recipe, per element.
type Scan struct {
	// Blocking is whether the policy in force refuses what violates it;
	// false is the advisory mode.
	Blocking bool `json:"blocking"`
	// StateUnknown says the scanner and database state were not read
	// when the slip was drawn; the per-element reports below stand on
	// their own.
	StateUnknown bool `json:"stateUnknown,omitempty"`
	// Degraded names why content is not being scanned right now, if so.
	Degraded *Problem `json:"degraded,omitempty"`
	// Database is the database in use now, nil when there is none.
	Database *scan.Database `json:"database,omitempty"`
	// DatabaseAgeSeconds is its age at drawing time.
	DatabaseAgeSeconds int64 `json:"databaseAgeSeconds,omitempty"`
	// MaxAgeSeconds is scan.maxDbAge.
	MaxAgeSeconds int64 `json:"maxAgeSeconds,omitempty"`
	// Recipes are the deliveries, each with the elements that carry a
	// report. A delivery without any says so rather than disappearing.
	Recipes []ScanRecipe `json:"recipes,omitempty"`
}

// ScanRecipe groups the scanned elements of one recipe.
type ScanRecipe struct {
	ID        string            `json:"id"`
	Assurance *assurance.Marker `json:"assurance,omitempty"`
	Items     []ScanItem        `json:"items,omitempty"`
}

// ScanItem is one scanned element: a report as the task held it, reduced to
// what a slip needs.
type ScanItem struct {
	Element string `json:"element"`
	TaskID  string `json:"taskId"`
	Verdict string `json:"verdict"`
	// Mode is the mode the report was evaluated under: "warn"
	// (advisory) or "block".
	Mode           string         `json:"mode"`
	Threshold      float64        `json:"threshold"`
	ThresholdLevel string         `json:"thresholdLevel,omitempty"`
	ScannedAt      time.Time      `json:"scannedAt,omitzero"`
	Scanner        *scan.Info     `json:"scanner,omitempty"`
	Database       *scan.Database `json:"database,omitempty"`
	Counts         scan.Counts    `json:"counts"`
	Violations     []string       `json:"violations,omitempty"`
	Reasons        []Problem      `json:"reasons,omitempty"`
	// ExceptionsApplied and ExceptionsExpired count the tolerated and
	// the lapsed exceptions.
	ExceptionsApplied int `json:"exceptionsApplied,omitempty"`
	ExceptionsExpired int `json:"exceptionsExpired,omitempty"`
	// Findings are the worst findings, at most ten; FindingsMore counts
	// the rest.
	Findings     []scan.Finding    `json:"findings,omitempty"`
	FindingsMore int               `json:"findingsMore,omitempty"`
	Assurance    *assurance.Marker `json:"assurance,omitempty"`
}

// Origins of an assurance marker.
const (
	// OriginInstance is the marker this instance computed from its own
	// trust verdict.
	OriginInstance = "instance"
	// OriginSourceClaim is the marker the source zone wrote into the
	// manifest — an unsigned claim, displayed beside the instance's own.
	OriginSourceClaim = "source-claim"
	// OriginManifest is, on a source side, the marker this instance
	// recorded in the manifest it wrote.
	OriginManifest = "manifest"
	// OriginScan is the marker carried by a scanned element's task item.
	OriginScan = "scan"
)

// Assurance is one visible reduced-assurance marker (FR-037, R-22).
type Assurance struct {
	Recipe  string             `json:"recipe"`
	Origin  string             `json:"origin"`
	Scope   string             `json:"scope"`
	Reasons []assurance.Reason `json:"reasons"`
}

// Build gathers the slip. It reads no file and hashes nothing: the
// expensive walk is the Verify step, never a rendering.
func Build(in *Input) *Slip {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	s := &Slip{
		GeneratedAt: now.UTC(), Version: in.Version, Side: SideSource,
		Zone: in.Zone, Root: in.Root, NoMedium: in.NoMedium, LastImport: in.LastImport,
	}
	if in.Zone != "" {
		s.Side = SideDestination
	}
	if in.MediumErr != nil {
		s.MediumErr = problemOfError(in.MediumErr)
	}
	if m := in.Manifest; m != nil {
		s.Medium = mediumOf(m)
	}
	if in.Report != nil {
		s.Verification = verificationOf(in.Report)
	}
	s.Scan = scanOf(in, s.Medium, now)
	s.Assurance = assurancesOf(s)
	return s
}

func mediumOf(m *media.Manifest) *Medium {
	out := &Medium{
		MediaID: m.MediaID, Zone: m.Zone, MediaFormat: m.MediaFormat, StoreFormat: m.StoreFormat,
		ProducedBy: m.ProducedBy, ResolvedAt: m.ResolvedAt, WrittenAt: m.WrittenAt, Totals: m.Totals,
	}
	for i := range m.Recipes {
		rc := &m.Recipes[i]
		out.Recipes = append(out.Recipes, Recipe{
			ID: rc.Name + "@" + rc.Version, Name: rc.Name, Version: rc.Version,
			Digest: rc.Digest, ResolvedAt: rc.ResolvedAt,
			Ingredients: append([]media.Ingredient(nil), rc.Ingredients...),
			Assurance:   rc.Assurance,
		})
	}
	sort.SliceStable(out.Recipes, func(a, b int) bool { return out.Recipes[a].ID < out.Recipes[b].ID })
	return out
}

func problemOfError(e *taxonomy.Error) *Problem {
	p := &Problem{Code: e.Code()}
	if len(e.ParamsMap()) > 0 {
		p.Params = map[string]string{}
		for k, v := range e.ParamsMap() {
			p.Params[k] = fmt.Sprint(v)
		}
	}
	return p
}

func verificationOf(rep *media.Report) *Verification {
	v := &Verification{
		Verdict: rep.Verdict, StartedAt: rep.StartedAt, FinishedAt: rep.FinishedAt,
		Checked: rep.Checked, Zone: rep.Zone, Freshness: rep.Freshness,
	}
	manifest := Stage{Key: StageManifest, State: StatePassed}
	for i := range rep.Blocks {
		b := &rep.Blocks[i]
		v.Blocks = append(v.Blocks, Block{
			Problem:     Problem{Code: b.Code, Params: b.Params},
			Overridable: b.Overridable, Overridden: b.Overridden,
		})
		if !b.Overridden {
			manifest.State = StateFailed
		}
	}
	for i := range rep.Findings {
		if len(v.Findings) == maxFindings {
			v.FindingsMore = len(rep.Findings) - maxFindings
			break
		}
		f := &rep.Findings[i]
		v.Findings = append(v.Findings, Problem{Code: f.Code, Params: f.Params, Path: f.Path})
	}

	sig, dig := Stage{Key: StageSignatures}, Stage{Key: StageDigests}
	for i := range rep.Recipes {
		r := &rep.Recipes[i]
		d := Delivery{
			ID: r.Name + "@" + r.Version, Name: r.Name, Version: r.Version, Pushable: r.Pushable,
			KeyFingerprint: r.KeyFingerprint, TrustScope: r.TrustScope, Unsigned: r.Unsigned,
			Assurance: r.Assurance, SourceAssurance: r.SourceAssurance, Files: r.Files, Bytes: r.Bytes,
		}
		switch {
		case r.Pushable:
			d.Signature, d.Content = StatePassed, StatePassed
		case r.Reason == nil:
			d.Signature, d.Content = StateFailed, StateFailed
		case r.Reason.Code == taxonomy.CodeSignature:
			// The content walk ran first and cleared: only the signature
			// failed, and the remedy is a trust root, not a re-copy.
			d.Content, d.Signature = StatePassed, StateFailed
		default:
			// A delivery whose content failed never reached its
			// signature check.
			d.Content, d.Signature = StateFailed, StateNotReached
		}
		if r.Reason != nil {
			d.Reason = &Problem{Code: r.Reason.Code, Params: r.Reason.Params, Path: r.Reason.Path}
		}
		tally(&sig, d.Signature)
		tally(&dig, d.Content)
		v.Recipes = append(v.Recipes, d)
	}
	sort.SliceStable(v.Recipes, func(a, b int) bool { return v.Recipes[a].ID < v.Recipes[b].ID })
	sig.State, dig.State = settle(&sig), settle(&dig)
	if manifest.State == StateFailed {
		// A medium blocked as a whole was never walked: the two
		// per-recipe stages have nothing to report rather than a pass.
		if sig.Passed+sig.Failed == 0 {
			sig.State = StateNotReached
		}
		if dig.Passed+dig.Failed == 0 {
			dig.State = StateNotReached
		}
	}
	v.Stages = []Stage{manifest, dig, sig}
	return v
}

func tally(s *Stage, state string) {
	switch state {
	case StatePassed:
		s.Passed++
	case StateFailed:
		s.Failed++
	}
}

func settle(s *Stage) string {
	switch {
	case s.Passed+s.Failed == 0:
		return StateNotReached
	case s.Failed == 0:
		return StatePassed
	case s.Passed == 0:
		return StateFailed
	default:
		return StatePartial
	}
}

// scanEntry is the freshest report held for one element.
type scanEntry struct {
	item tasks.Item
	task string
}

// scanOf reads the scan section: per element, the report of the most
// recent task that scanned it. An element's name is "name@version/ingredient"
// in the task documents (internal/engine/scan.go); anything else is not a
// scanned element.
func scanOf(in *Input, m *Medium, now time.Time) Scan {
	out := Scan{
		Blocking: in.Scan.Blocking, StateUnknown: in.Scan.Unknown,
		Database: in.Scan.Database, MaxAgeSeconds: int64(in.Scan.MaxAge.Seconds()),
	}
	if in.Scan.Cause != nil {
		out.Degraded = problemOfError(in.Scan.Cause)
	}
	if in.Scan.Database != nil {
		out.DatabaseAgeSeconds = int64(now.Sub(in.Scan.Database.BuiltAt).Seconds())
	}

	ts := append([]*tasks.Task(nil), in.Tasks...)
	sort.SliceStable(ts, func(a, b int) bool { return ts[a].Created.After(ts[b].Created) })
	latest := map[string]scanEntry{}
	for _, t := range ts {
		if t == nil {
			continue
		}
		for i := range t.Items {
			it := &t.Items[i]
			if it.Scan == nil {
				continue
			}
			if _, seen := latest[it.Name]; !seen {
				latest[it.Name] = scanEntry{item: *it, task: t.ID}
			}
		}
	}

	byRecipe := map[string]*ScanRecipe{}
	names := make([]string, 0, len(latest))
	for name := range latest {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rid, element, ok := strings.Cut(name, "/")
		if !ok || !strings.Contains(rid, "@") {
			continue
		}
		e := latest[name]
		rc := byRecipe[rid]
		if rc == nil {
			rc = &ScanRecipe{ID: rid}
			byRecipe[rid] = rc
		}
		if e.item.Assurance != nil {
			rc.Assurance = e.item.Assurance
		}
		rc.Items = append(rc.Items, scanItemOf(element, e.task, &e.item))
	}

	// With a manifest, the slip is about THIS medium: its deliveries, each
	// listed even when nothing was scanned (that absence is information),
	// and nobody else's.
	if m != nil {
		for i := range m.Recipes {
			id := m.Recipes[i].ID
			rc := byRecipe[id]
			if rc == nil {
				rc = &ScanRecipe{ID: id}
			}
			if rc.Assurance == nil {
				rc.Assurance = m.Recipes[i].Assurance
			}
			out.Recipes = append(out.Recipes, *rc)
		}
		return out
	}
	ids := make([]string, 0, len(byRecipe))
	for id := range byRecipe {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out.Recipes = append(out.Recipes, *byRecipe[id])
	}
	return out
}

func scanItemOf(element, taskID string, it *tasks.Item) ScanItem {
	r := it.Scan
	ev := &r.Policy
	si := ScanItem{
		Element: element, TaskID: taskID, Verdict: string(ev.Verdict),
		Mode: string(ev.Policy.Mode), Threshold: ev.Policy.Threshold,
		ThresholdLevel: string(ev.Policy.ThresholdLevel),
		ScannedAt:      r.ScannedAt, Scanner: r.Scanner, Database: r.Database, Counts: r.Counts,
		Violations:        append([]string(nil), ev.Violations...),
		ExceptionsApplied: len(ev.ExceptionsApplied), ExceptionsExpired: len(ev.ExceptionsExpired),
		Assurance: it.Assurance,
	}
	for _, rs := range ev.Reasons {
		si.Reasons = append(si.Reasons, Problem{Code: rs.Code, Params: rs.Params})
	}
	// Worst first: the CVSS score when there is one, else the lower
	// bound of the severity band (the policy's own comparison).
	scores := make([]float64, len(r.Findings))
	idx := make([]int, len(r.Findings))
	for i := range r.Findings {
		idx[i] = i
		scores[i] = -1
		if f := &r.Findings[i]; f.CVSS != nil {
			scores[i] = *f.CVSS
		} else if lb, ok := f.Severity.LowerBound(); ok {
			scores[i] = lb
		}
	}
	sort.SliceStable(idx, func(a, b int) bool { return scores[idx[a]] > scores[idx[b]] })
	for _, i := range idx {
		if len(si.Findings) == maxScanFindings {
			si.FindingsMore = len(r.Findings) - maxScanFindings
			break
		}
		si.Findings = append(si.Findings, r.Findings[i])
	}
	return si
}

// assurancesOf collects every reduced-assurance marker the slip's own
// sections carry, once each, so the reader finds them in one place as
// well as beside the content they qualify (R-22).
func assurancesOf(s *Slip) []Assurance {
	seen := map[string]bool{}
	var out []Assurance
	add := func(recipe, origin string, m *assurance.Marker) {
		if !m.Reduced() {
			return
		}
		key := recipe + "\x00" + origin + "\x00" + m.Scope
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Assurance{Recipe: recipe, Origin: origin, Scope: m.Scope, Reasons: m.Reasons})
	}
	if s.Medium != nil {
		origin := OriginSourceClaim
		if s.Side == SideSource {
			origin = OriginManifest
		}
		for i := range s.Medium.Recipes {
			add(s.Medium.Recipes[i].ID, origin, s.Medium.Recipes[i].Assurance)
		}
	}
	if s.Verification != nil {
		for i := range s.Verification.Recipes {
			d := &s.Verification.Recipes[i]
			add(d.ID, OriginInstance, d.Assurance)
			add(d.ID, OriginSourceClaim, d.SourceAssurance)
		}
	}
	for i := range s.Scan.Recipes {
		rc := &s.Scan.Recipes[i]
		add(rc.ID, OriginScan, rc.Assurance)
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Recipe != out[b].Recipe {
			return out[a].Recipe < out[b].Recipe
		}
		return out[a].Origin < out[b].Origin
	})
	return out
}

// LoadMedium reads the medium's own claims from a store root, hashing
// nothing, and classifies the outcome the way the Media screen does: a
// missing manifest is the ordinary state of a store no synchronization
// has finished on (an explanation, not an alarm) except on a destination
// side, where a medium was expected and its absence is a refusal.
func LoadMedium(root string, destination bool) (m *media.Manifest, noMedium bool, problem *taxonomy.Error) {
	if root == "" {
		return nil, true, nil
	}
	m, block, err := media.ReadManifest(root)
	switch {
	case err != nil:
		return nil, true, taxonomy.New(taxonomy.CodeStoreRead, taxonomy.Params{"detail": err.Error()})
	case block != nil:
		noMedium = block.Code == taxonomy.CodeMediaManifestMissing
		if !noMedium || destination {
			problem = block.Error()
		}
		return nil, noMedium, problem
	}
	// The per-file inventory is thousands of entries no slip renders.
	m.Inventory = nil
	return m, false, nil
}

// Instance is what a running instance holds that a slip is drawn from. The
// API, the interface and the command line each fill the parts they have
// and draw through the same Slip method, so the three surfaces cannot tell
// different stories (FR-061).
type Instance struct {
	Version string
	// Zone and Root are the zone this instance serves (empty on a source
	// side) and the store directory.
	Zone, Root string
	// LastImport is the freshness record for the zone, nil when none.
	LastImport *media.ImportRecord
	// Report is the last verification verdict, nil when none.
	Report *media.Report
	// Queue is where the scan reports are read from; nil is none.
	Queue *tasks.Queue
	// Tasks are task documents already in hand (the command line reads
	// them from the store without opening a queue); added to Queue's.
	Tasks []*tasks.Task
	// Scan reads the scanning state; nil is "unknown, advisory".
	Scan func() ScanContext
	// Now is the drawing time; zero is time.Now.
	Now time.Time
}

// Slip draws the slip: it reads the manifest (hashing nothing), the tasks
// and the scanning state, and gathers them with what the caller holds.
func (i *Instance) Slip() *Slip {
	in := &Input{
		Version: i.Version, Now: i.Now, Zone: i.Zone, Root: i.Root,
		LastImport: i.LastImport, Report: i.Report, Tasks: i.Tasks,
	}
	in.Manifest, in.NoMedium, in.MediumErr = LoadMedium(i.Root, i.Zone != "")
	if i.Queue != nil {
		in.Tasks = append(in.Tasks, i.Queue.List("", "", "")...)
	}
	if i.Scan != nil {
		in.Scan = i.Scan()
	} else {
		in.Scan.Unknown = true
	}
	return Build(in)
}

// Formats a slip can be served in.
const (
	FormatHTML = "html"
	FormatText = "text"
	FormatJSON = "json"
)

// Render renders the slip in a format and a language, returning the bytes
// and their content type. The format is checked here, once, for the
// surfaces that take it from a request.
func (s *Slip) Render(format, lang string) (body []byte, contentType string, err error) {
	switch format {
	case FormatHTML, "":
		body, err = s.HTML(lang)
		return body, ContentTypeHTML, err
	case FormatText, "txt":
		return s.Text(lang), ContentTypeText, nil
	case FormatJSON:
		body, err = json.MarshalIndent(s, "", "  ")
		return body, "application/json", err
	}
	return nil, "", fmt.Errorf("slip: unknown format %q", format)
}

// Filename is the name a downloaded slip is saved under: the medium's
// identity when there is one, so slips of two transfers never collide in a
// folder.
func (s *Slip) Filename(format string) string {
	name := "transfer-slip"
	if s.Medium != nil && s.Medium.MediaID != "" {
		// The identity comes from a manifest, which is another zone's
		// claim: only filename-safe characters survive into a header.
		safe := strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
				return r
			}
			return -1
		}, s.Medium.MediaID)
		if safe != "" {
			name += "-" + safe
		}
	}
	switch format {
	case FormatText, "txt":
		return name + ".txt"
	case FormatJSON:
		return name + ".json"
	}
	return name + ".html"
}
