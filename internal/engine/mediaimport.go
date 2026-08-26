// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/tobby-fetch/recipe-spec/cookbook"
	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The destination side of a physical transfer (FR-052, feature 5.4).
//
// The same application, pointed at the transported store. The medium is
// untrusted until proven otherwise, so the journey is fixed and its ORDER
// is normative (FR-054): re-verification — manifest completeness and
// checksums, then recipe signatures and ingredient digests — precedes any
// push, any serving and any local write. Then the differential push into
// the zone registry (FR-028) under the same policy checks passthrough
// applies (FR-030, FR-033), the signed recipes into the zone's own
// cookbook (FR-034), and finally the freshness record (R-28).
//
// Nothing here re-implements the push: promote.go already owns it, with
// the allowlist consulted before the socket, the signature re-verified
// over the exact bytes about to leave, and the recipe published last so
// the zone's cookbook never advertises content that has not arrived. What
// this file adds is the one thing that differs — the bytes come off a
// medium a foreign instance wrote, not out of a fetch this instance ran —
// and the one thing that must never be skipped, which is the order above.

// mediaItemPrefix names the verification verdict of one delivery in the
// task's item list. It is deliberately a separate item from the push
// items promote.go creates: "this recipe did not survive the trip" and
// "this recipe could not be pushed" are two different failures with two
// different remedies, and an operator reading a partially damaged medium
// needs to tell them apart at a glance (R-19).
const mediaItemPrefix = "media/"

// MediaOptions parameterizes one destination-side media operation.
type MediaOptions struct {
	// AllowZoneMismatch and AllowStale waive the two FR-054 guards an
	// administrator may waive. Both are anti-accident guards over an
	// unsigned manifest, never security controls; the caller is
	// responsible for the FR-094 audit record, because the actor and the
	// network origin belong to whoever authenticated them.
	AllowZoneMismatch bool
	AllowStale        bool
	// Progress receives verification progress (FR-054: progress is
	// displayed). Nil reports nothing.
	Progress func(media.Progress)
	// Report receives the verification report the moment it is complete
	// and BEFORE anything is pushed.
	//
	// It is the seam the guided journey is built on — Verify, then
	// Report, then Push — so the CLI can print the verdict an operator is
	// about to act on, and so the ordering FR-054 makes normative is
	// observable from outside this package rather than merely intended
	// inside it.
	Report func(*media.Report)
}

// SetMediaImport installs the destination-side half of FR-052: the
// identity of the zone this instance serves, and the per-zone freshness
// register (R-28).
//
// A setter rather than a constructor argument, like the destination and
// the media manifest writer: an engine that imports no medium is a
// complete engine — that is every source-side instance.
//
// The register lives in the INSTANCE state directory and never on the
// medium, which is the whole requirement: a register carried on the
// medium would be rewritten by whoever holds it, and that is exactly the
// accident the guard exists to catch.
func (e *Engine) SetMediaImport(zone string, imports *media.Imports) {
	e.zone, e.imports = zone, imports
}

// Zone reports the identity of the zone this instance serves, for the
// surfaces that show it (FR-052, FR-054).
func (e *Engine) Zone() string { return e.zone }

// MediaImportRunner returns the task runner for tasks.TypeMediaImport.
func (e *Engine) MediaImportRunner() tasks.Runner {
	return func(ctx context.Context, t *tasks.Task, logger *slog.Logger, save func()) error {
		return e.importMedia(ctx, newTaskSink(t, save), logger, MediaOptions{
			AllowZoneMismatch: hasOverride(t, tasks.OverrideZone),
			AllowStale:        hasOverride(t, tasks.OverrideFreshness),
		})
	}
}

// hasOverride reports whether the task carries one waived guard.
func hasOverride(t *tasks.Task, guard string) bool {
	for _, g := range t.MediaOverrides {
		if g == guard {
			return true
		}
	}
	return false
}

// VerifyMedia re-verifies the transported store and returns the report,
// having written nothing and pushed nothing (FR-054).
//
// It is the whole first step of the journey and it is also usable on its
// own: the CLI's `media verify`, the API's verification endpoint and the
// Media screen all call this and stop here. Every failure the medium
// itself is responsible for comes back INSIDE the report; the error
// return is for a store that cannot be opened at all.
func (e *Engine) VerifyMedia(ctx context.Context, logger *slog.Logger, opts MediaOptions) (*media.Report, error) {
	if e.zone == "" {
		return nil, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "no zone identity is configured (zone, FR-052): a destination instance " +
				"cannot tell whether a medium is addressed to it",
		})
	}
	var last *media.ImportRecord
	if e.imports != nil {
		if rec, ok := e.imports.Last(e.zone); ok {
			last = &rec
		}
	}
	return media.Verify(ctx, e.store, media.VerifyOptions{
		Zone: e.zone,
		// The DESTINATION instance's trust policy, and no other. Trust
		// roots present on the medium are ignored (FR-054): the media
		// package has no way to load key material at all, and this is the
		// only place a policy is handed to it.
		Trust:             MediaTrust{Policy: e.trust},
		LastImport:        last,
		AllowZoneMismatch: opts.AllowZoneMismatch,
		AllowStale:        opts.AllowStale,
		Progress:          opts.Progress,
		Logger:            logger,
	})
}

// importMedia is the destination-side operation of FR-052, in the order
// FR-054 fixes.
//
// The ordering is the guarantee of this whole feature, so it is written
// as three numbered stages with nothing between them: there is exactly
// one call to the verification, exactly one place the report is read, and
// no path from the top of this function to a push that does not go
// through both.
func (e *Engine) importMedia(ctx context.Context, sink *taskSink, logger *slog.Logger, opts MediaOptions) error {
	if e.dest == nil {
		return taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "no destination registry is configured (destination.registry, FR-052): " +
				"a medium can be verified without one, but not imported",
		})
	}
	logger = logger.With(slog.String("zone", e.zone), slog.String("medium", e.store.Root()))

	// ---------------------------------------------------------------
	// 1. VERIFY. Nothing before this line has contacted the destination
	//    or written anything, and nothing after it acts on the medium
	//    except through the report it produced (FR-054).
	// ---------------------------------------------------------------
	rep, err := e.VerifyMedia(ctx, logger, opts)
	if err != nil {
		return e.mapError(err, e.store.Root())
	}
	logVerification(ctx, logger, rep)
	recordVerdicts(sink, rep)
	if opts.Report != nil {
		opts.Report(rep)
	}
	if rep.Verdict == media.VerdictBlocked {
		// Nothing is pushed off a blocked medium — not even the
		// deliveries that would have verified, because when a global
		// block stands there is nothing a per-recipe verdict could mean
		// (R-19), and when none stands every recipe already failed.
		logger.LogAttrs(ctx, slog.LevelWarn, "medium blocked: nothing was pushed",
			slog.Int("recipes", len(rep.Recipes)),
			slog.String("requirement", "FR-054"))
		return blockingError(rep)
	}

	// ---------------------------------------------------------------
	// 2. PUSH what verification cleared, and only that.
	// ---------------------------------------------------------------
	graph, err := e.mediumGraph()
	if err != nil {
		return e.mapError(err, e.store.Root())
	}
	cleared := rep.Pushable()
	delivered := 0
	for i := range cleared {
		if e.importDelivery(ctx, sink, logger, graph, &cleared[i]) {
			delivered++
		}
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "medium imported",
		slog.String("verdict", string(rep.Verdict)),
		slog.Int("cleared", len(cleared)), slog.Int("delivered", delivered),
		slog.Int("blocked", len(rep.Recipes)-len(cleared)),
		slog.String("requirement", "FR-052"))

	// ---------------------------------------------------------------
	// 3. RECORD the import, and only a completed one (R-28).
	// ---------------------------------------------------------------
	if delivered == len(cleared) {
		e.recordImport(ctx, sink, logger, rep)
	}
	return nil
}

// recordVerdicts turns the per-recipe verification verdicts into task
// items, blocked ones carrying the code and the offending file (FR-054:
// the refusal names the file).
//
// A cleared delivery is left RUNNING rather than done: verification
// clearing it is not the same statement as the zone holding it, and the
// item that says "this delivery survived the trip" must not also claim it
// arrived. settleDelivery closes it once the push has had its say.
func recordVerdicts(sink *taskSink, rep *media.Report) {
	sink.update(func(t *tasks.Task) bool {
		for i := range rep.Recipes {
			v := &rep.Recipes[i]
			item := itemFor(t, mediaItemPrefix+v.Name+"@"+v.Version)
			item.Digest = v.Digest
			item.SizeBytes = v.Bytes
			if v.Pushable {
				item.Status = tasks.StatusRunning
				continue
			}
			item.Status = tasks.StatusFailed
			if v.Reason != nil {
				item.Error = tasks.FromTaxonomy(v.Reason.Error())
			}
		}
		return len(rep.Recipes) > 0
	})
}

// settleDelivery closes one delivery's verdict item once the push has
// finished, carrying up the reason the push failed.
//
// The reason is taken from the push items rather than invented: they
// already name the ingredient and the code, and a summary line stating a
// different cause than the detail below it is worse than no summary.
func settleDelivery(sink *taskSink, rid string, landed bool) {
	sink.update(func(t *tasks.Task) bool {
		item := itemFor(t, mediaItemPrefix+rid)
		if landed {
			item.Status = tasks.StatusDone
			return true
		}
		item.Status = tasks.StatusFailed
		for i := range t.Items {
			it := &t.Items[i]
			if it.Status == tasks.StatusFailed && it.Error != nil &&
				strings.HasPrefix(it.Name, pushItemPrefix+rid+"/") {
				item.Error = it.Error
				break
			}
		}
		return true
	})
}

// MediaRefusal is the principal refusal of a report, or nil when every
// delivery on the medium is pushable.
//
// It is what turns a report into an exit code (FR-066): the taxonomy
// class of the code decides it, so a zone mismatch exits on the policy
// class and a corrupted blob on the verification class, without the CLI
// re-deciding a severity the catalog already fixed.
func MediaRefusal(rep *media.Report) *taxonomy.Error {
	if rep.Verdict == media.VerdictPushable {
		return nil
	}
	var te *taxonomy.Error
	if errors.As(blockingError(rep), &te) {
		return te
	}
	return taxonomy.New(taxonomy.CodeInternal, nil)
}

// blockingError renders a blocked medium as the task-level failure.
//
// A standing global block is the answer whenever there is one: it is the
// reason nothing was looked at, and it carries the file or the two zone
// names an operator needs. Without one, every recipe was blocked
// individually and the per-recipe items already say why — the task-level
// code then aggregates them rather than inventing a verdict of its own.
func blockingError(rep *media.Report) error {
	for i := range rep.Blocks {
		if !rep.Blocks[i].Overridden {
			return rep.Blocks[i].Error()
		}
	}
	for i := range rep.Recipes {
		if r := rep.Recipes[i].Reason; r != nil {
			return r.Error()
		}
	}
	return taxonomy.New(taxonomy.CodeInternal, nil).
		WithCause(errors.New("engine: a medium was reported blocked without a reason"))
}

// logVerification narrates the report onto the operation log — the
// medium's own return channel (FR-053), where it is the record of what
// this zone was handed and what it made of it.
func logVerification(ctx context.Context, logger *slog.Logger, rep *media.Report) {
	attrs := []slog.Attr{
		slog.String("verdict", string(rep.Verdict)),
		slog.Int("recipes", len(rep.Recipes)),
		slog.Int("checked_files", rep.Checked.Files),
		slog.Int64("checked_bytes", rep.Checked.Bytes),
		slog.String("requirement", "FR-054"),
	}
	if rep.Media != nil {
		// R-28: the medium's identity travels in the logs of BOTH sides,
		// so an incident traces back to a physical object.
		attrs = append(attrs,
			slog.String("media_id", rep.Media.MediaID),
			slog.String("media_zone", rep.Media.Zone),
			slog.Time("resolved_at", rep.Media.ResolvedAt),
			slog.String("produced_by", rep.Media.ProducedBy.Version),
			slog.String("source_run_id", rep.Media.ProducedBy.RunID))
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "media verification complete", attrs...)

	for i := range rep.Blocks {
		b := &rep.Blocks[i]
		level := slog.LevelError
		if b.Overridden {
			// Already logged as an override by the media package; repeated
			// here at Warn so the medium's own log carries the full story
			// without the reader having to correlate two files.
			level = slog.LevelWarn
		}
		logger.LogAttrs(ctx, level, "media blocked",
			slog.String("code", string(b.Code)),
			slog.Bool("overridable", b.Overridable),
			slog.Bool("overridden", b.Overridden))
	}
	for i := range rep.Recipes {
		v := &rep.Recipes[i]
		if v.Pushable || v.Reason == nil {
			continue
		}
		logger.LogAttrs(ctx, slog.LevelWarn, "recipe blocked on the medium",
			slog.String("recipe", v.Name+"@"+v.Version),
			slog.String("code", string(v.Reason.Code)),
			slog.String("path", v.Reason.Path),
			slog.String("requirement", "R-19"))
	}
	// Extraneous content is reported and never pushed (FR-054). It blocks
	// nothing, so it is a count and a sample rather than thousands of
	// lines an operator would scroll past.
	if n := len(rep.Findings); n > 0 {
		logger.LogAttrs(ctx, slog.LevelInfo, "medium carries content no verified recipe reaches",
			slog.Int("findings", n),
			slog.String("first", rep.Findings[0].Path),
			slog.String("code", string(rep.Findings[0].Code)),
			slog.String("requirement", "FR-054"))
	}
}

// mediumGraph is the medium's recipe graph, indexed by "name@version".
//
// meta/recipes.json is the authority on WHERE each delivery sits on the
// medium, and verification has just proved it matches its inventory entry
// — an altered graph blocks the whole medium with no override (R-19), so
// past that point the graph can be believed. It is also the only place
// the relocated paths are written down: the base prefix belonged to the
// instance that produced the medium and no reader can recompute it
// (RECIPE-SPEC §11.5).
func (e *Engine) mediumGraph() (map[string]store.RecipeRecord, error) {
	records, err := e.store.RecipeRecords()
	if err != nil {
		return nil, err
	}
	sort.Slice(records, func(a, b int) bool {
		return records[a].Name+"@"+records[a].Version < records[b].Name+"@"+records[b].Version
	})
	out := make(map[string]store.RecipeRecord, len(records))
	for i := range records {
		out[records[i].Name+"@"+records[i].Version] = records[i]
	}
	return out, nil
}

// importDelivery pushes one cleared delivery off the medium and reports
// whether the destination now holds it whole.
func (e *Engine) importDelivery(ctx context.Context, sink *taskSink, logger *slog.Logger,
	graph map[string]store.RecipeRecord, v *media.RecipeVerdict,
) bool {
	rid := v.Name + "@" + v.Version
	rec, ok := graph[rid]
	fail := func(err error) bool {
		te := e.mapError(err, rid)
		sink.update(func(t *tasks.Task) bool {
			item := itemFor(t, mediaItemPrefix+rid)
			item.Status = tasks.StatusFailed
			item.Error = tasks.FromTaxonomy(te)
			return true
		})
		logger.LogAttrs(ctx, slog.LevelWarn, "delivery not imported",
			slog.String("recipe", rid), slog.String("code", string(te.Code())),
			slog.String("error", te.Error()))
		return false
	}
	if !ok {
		return fail(fmt.Errorf("the medium's recipe graph carries no entry for %s, "+
			"although its manifest delivers it", rid))
	}
	// Defensive equality against a manifest and a graph that disagree.
	// Verification proves the graph matches its inventory entry, hence
	// that it is the graph the manifest was derived from; a mismatch here
	// would mean the two were written by different runs, which is a
	// refusal to name rather than a state to push from.
	if rec.Digest != v.Digest || rec.ArtifactRepo != v.ArtifactRepo {
		return fail(fmt.Errorf("the medium's manifest and its recipe graph disagree about %s: "+
			"manifest says %s at %s, graph says %s at %s",
			rid, v.Digest, v.ArtifactRepo, rec.Digest, rec.ArtifactRepo))
	}

	f, err := e.recipeOnMedium(ctx, &rec)
	if err != nil {
		return fail(err)
	}
	landed := e.promoteRecipe(ctx, sink, logger, f, rid, mediumContent(&rec))
	settleDelivery(sink, rid, landed)
	if !landed {
		logger.LogAttrs(ctx, slog.LevelWarn, "delivery cleared by verification did not land",
			slog.String("recipe", rid), slog.String("requirement", "FR-052"))
	}
	return landed
}

// mediumContent is the promotion origin of a medium: every path comes
// from the graph the medium carries, and the FR-022 widening is off —
// there is no upstream to widen from in an isolated zone (NFR-019).
func mediumContent(rec *store.RecipeRecord) *origin {
	byName := make(map[string]string, len(rec.Ingredients))
	for i := range rec.Ingredients {
		byName[rec.Ingredients[i].Name] = rec.Ingredients[i].Repo
	}
	return &origin{
		stored: func(ref, ingredient string) (string, error) {
			if ingredient == "" {
				return rec.ArtifactRepo, nil
			}
			if repo, ok := byName[ingredient]; ok && repo != "" {
				return repo, nil
			}
			return "", fmt.Errorf("the medium's recipe graph does not say where ingredient %q of %s@%s (%s) landed",
				ingredient, rec.Name, rec.Version, ref)
		},
	}
}

// recipeOnMedium rebuilds one delivery from the transported store: the
// artifact manifest, the §11.2 layout it must obey, and the cooked
// document inside it.
//
// It is deliberately not Cookbook.LoadDocument: that one reads a cookbook
// over the network through the substitution-aware remote access, and an
// isolated zone has neither. The checks are the same ones and in the same
// order, because they belong to the format and not to the transport —
// including the §11.3 coherence check, which is what stops a medium from
// delivering an artifact that disagrees with the name and version it is
// published under.
func (e *Engine) recipeOnMedium(ctx context.Context, rec *store.RecipeRecord) (*FetchedRecipe, error) {
	if rec.ArtifactRepo == "" || rec.ArtifactTag == "" {
		return nil, fmt.Errorf("the medium's recipe graph does not say where the artifact of %s@%s landed",
			rec.Name, rec.Version)
	}
	payload, mediaType, dgst, err := e.store.RawManifest(ctx, rec.ArtifactRepo, rec.ArtifactTag)
	if err != nil {
		return nil, err
	}
	if dgst != rec.Digest {
		return nil, fmt.Errorf("%s:%s on the medium resolves to %s, but the recipe graph pins %s",
			rec.ArtifactRepo, rec.ArtifactTag, dgst, rec.Digest)
	}
	layout, err := cookbook.VerifyManifest(payload)
	if err != nil {
		return nil, err
	}
	rc, err := e.store.BlobReader(ctx, rec.ArtifactRepo, layout.Document.Digest)
	if err != nil {
		return nil, err
	}
	defer rc.Close() //nolint:errcheck // read side
	yaml, err := readBounded(rc, maxRecipeBytes)
	if err != nil {
		return nil, err
	}
	recipe, err := spec.ParseRecipe(yaml)
	if err != nil {
		return nil, err
	}
	if err := recipe.Validate(spec.ProfileCooked); err != nil {
		return nil, err
	}
	if err := recipe.ValidatePublishLocation(rec.Name, rec.ArtifactTag); err != nil {
		return nil, err
	}
	return &FetchedRecipe{
		NominalRepo:    rec.CookbookRepo,
		Tag:            rec.ArtifactTag,
		ManifestBytes:  payload,
		MediaType:      mediaType,
		ManifestDigest: dgst,
		ConfigDigest:   layout.Config.Digest,
		LayerDigest:    layout.Document.Digest,
		YAML:           yaml,
		Recipe:         recipe,
	}, nil
}

// recordImport advances the per-zone freshness register (R-28).
//
// Only a completed import gets here, which is the requirement's own
// wording: recording on arrival would make a failed import poison the
// next legitimate one. A medium that delivered part of its cargo and
// blocked the rest still counts — the deliveries that landed are real and
// the zone genuinely moved forward — and re-importing a repaired copy of
// the SAME medium stays possible, since equal resolution timestamps are
// not stale ones.
//
// A register that cannot persist is a loud warning and not a task
// failure: the content did cross, and an instance running without a state
// directory (the FR-075 authentication opt-out permits one) would
// otherwise fail every import it performs correctly.
func (e *Engine) recordImport(ctx context.Context, sink *taskSink, logger *slog.Logger, rep *media.Report) {
	if rep.Media == nil {
		return
	}
	record := &media.ImportRecord{
		MediaID:    rep.Media.MediaID,
		ResolvedAt: rep.Media.ResolvedAt,
		ImportedAt: time.Now().UTC(),
		RunID:      sink.runID(),
	}
	if e.imports == nil {
		logger.LogAttrs(ctx, slog.LevelWarn, "import not recorded: no freshness register is wired",
			slog.String("media_id", record.MediaID), slog.String("requirement", "R-28"))
		return
	}
	if err := e.imports.Record(e.zone, record); err != nil {
		logger.LogAttrs(ctx, slog.LevelWarn, "import not recorded in the freshness register",
			slog.String("media_id", record.MediaID),
			slog.String("error", err.Error()), slog.String("requirement", "R-28"))
		return
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "import recorded for the zone",
		slog.String("media_id", record.MediaID),
		slog.Time("resolved_at", record.ResolvedAt),
		slog.String("requirement", "R-28"))
}

// MediaSummary is what a surface shows about a medium before anything is
// done to it: the identity the manifest claims and the freshness record
// this instance holds for the zone (FR-052, R-28).
type MediaSummary struct {
	// Zone is the identity this instance serves.
	Zone string `json:"zone"`
	// Root is the store directory the medium is.
	Root string `json:"root"`
	// MediaID is this store's own identity.
	MediaID string `json:"mediaId"`
	// LastImport is the register's entry for Zone, when there is one.
	LastImport *media.ImportRecord `json:"lastImport,omitempty"`
}

// MediaSummary reports the medium and the freshness record without
// reading a byte of content: it is what a screen or a CLI prints before
// an operator decides to spend twenty minutes re-hashing a disk.
func (e *Engine) MediaSummary() MediaSummary {
	s := MediaSummary{Zone: e.zone, Root: e.store.Root(), MediaID: e.store.MediaID()}
	if e.imports != nil {
		if rec, ok := e.imports.Last(e.zone); ok {
			s.LastImport = &rec
		}
	}
	return s
}
