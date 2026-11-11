// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// ScanSettings wires vulnerability scanning into the engine (FR-031,
// FR-032, milestone 6). A nil Scanner leaves the engine as it was before
// scanning existed — the position of the unit tests that do not exercise
// it; an instance always installs one, and an unavailable scanner is then
// the visible degraded mode, never an absent one.
type ScanSettings struct {
	Scanner scan.Scanner
	// Mode, Threshold and Exceptions are the instance level (scan.*).
	Mode       scan.Mode
	Threshold  float64
	Exceptions []scan.Exception
	// Freshness is scan.maxDbAge and scan.maxDbAgeAction.
	Freshness scan.Freshness
	// Observe receives every per-item report (the metrics).
	Observe func(*scan.Report)
	// Refresh re-reads the scanner's own deliveries — its database and,
	// on Linux and Windows hosts, its binary — and is called after every
	// recorded recipe that carries an OCIArtifact or a FileSet: the shape
	// of the tobby-system recipe. Synchronizing that recipe first in a
	// Retriever therefore arms the scanner for the recipes after it, in
	// the same run (FR-032 bootstrap).
	Refresh func(ctx context.Context)
	// Now is the evaluation clock; nil is time.Now.
	Now func() time.Time
}

// ScanSettingsFrom maps the configuration's scan section onto the engine's
// settings. Exceptions are stamped with the instance level.
func ScanSettingsFrom(c *config.Scan, scanner scan.Scanner) ScanSettings {
	s := ScanSettings{
		Scanner:   scanner,
		Mode:      scan.Mode(c.Mode),
		Threshold: c.Threshold,
		Freshness: scan.Freshness{MaxAge: time.Duration(c.MaxDBAge), Block: c.MaxDBAgeAction == config.ScanModeBlock},
	}
	for _, ex := range c.Exceptions {
		s.Exceptions = append(s.Exceptions, scan.Exception{ID: ex.ID, Justification: ex.Justification, Expires: ex.Expires, Level: scan.LevelInstance})
	}
	return s
}

// SetScan installs vulnerability scanning.
func (e *Engine) SetScan(s *ScanSettings) {
	e.scan = *s
	if e.scan.Now == nil {
		e.scan.Now = time.Now
	}
}

// ScanPolicyBlocking reports whether the policy in force refuses what
// violates it: the instance's scan.mode, or a threshold the served
// Retriever poses (a threshold posed for a perimeter is blocking for it).
// It feeds the advisory banner and its metric, so it reads the Retriever
// of the last run; before any run, the instance setting alone.
func (e *Engine) ScanPolicyBlocking() bool {
	return e.scan.Mode == scan.ModeBlock || e.zoneThresholdSet.Load()
}

// zonePolicy is what the served Retriever's spec.scanPolicy declares
// (RECIPE-SPEC §10.4).
type zonePolicy struct {
	threshold  *float64
	exceptions []scan.Exception
}

// zonePolicyOf reads a Retriever's scan policy. The document was validated
// by the SDK, so an unparsable expiry cannot reach here; one would be read
// as already expired, the side that never tolerates more than declared.
func zonePolicyOf(p *spec.ScanPolicy) zonePolicy {
	var z zonePolicy
	if p == nil {
		return z
	}
	if p.Threshold != nil {
		v := *p.Threshold
		z.threshold = &v
	}
	for _, ex := range p.Exceptions {
		se := scan.Exception{ID: ex.ID, Justification: ex.Justification, Level: scan.LevelRetriever}
		if ex.Expires != "" {
			t, err := time.Parse(time.RFC3339, ex.Expires)
			if err != nil {
				t = time.Unix(0, 0)
			}
			se.Expires = t
		}
		z.exceptions = append(z.exceptions, se)
	}
	return z
}

// recipeThreshold reads the recipe annotation recipe.tobby.dev/scan-threshold.
// The schema validates it; a value that still does not parse is reported
// and applied as 0 — every scored finding violates — because an
// annotation that asks for a stricter policy is never silently dropped.
func recipeThreshold(r *spec.Recipe) (*float64, bool) {
	raw, ok := r.Metadata.Annotations[spec.AnnotationScanThreshold]
	if !ok {
		return nil, true
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || !config.ValidCVSSScore(v) {
		zero := 0.0
		return &zero, false
	}
	return &v, true
}

// policyFor combines the three levels for one recipe.
func (e *Engine) policyFor(r *spec.Recipe, zone zonePolicy, logger *slog.Logger) scan.Policy {
	rt, ok := recipeThreshold(r)
	if !ok {
		logger.LogAttrs(context.Background(), slog.LevelWarn, "recipe scan-threshold annotation is not a CVSS score: applied as 0",
			slog.String("annotation", spec.AnnotationScanThreshold),
			slog.String("value", r.Metadata.Annotations[spec.AnnotationScanThreshold]),
			slog.String("requirement", "FR-031"))
	}
	return scan.Combine(&scan.Levels{
		InstanceMode:        e.scan.Mode,
		InstanceThreshold:   e.scan.Threshold,
		InstanceExceptions:  e.scan.Exceptions,
		RetrieverThreshold:  zone.threshold,
		RetrieverExceptions: zone.exceptions,
		RecipeThreshold:     rt,
	})
}

// scanned reports whether an ingredient kind is scanned at retrieval.
// Container images are; the other kinds carry no operating-system or
// language package database Trivy reads as such (ADR-0008 decision 1:
// "where applicable"), and the tobby-system deliveries — the database
// itself, the scanner binary — are among them.
func scanned(kind spec.IngredientKind) bool { return kind == spec.IngredientContainerImage }

// scanRecipe scans the container images of one synchronized recipe, at
// retrieval, in passthrough as in mirror mode (FR-031). Each report lands
// on the ingredient's fetch item — never nil for a scanned kind: an item
// that could not be scanned says so, with the reason. It returns the
// refusals by ingredient name: what violates a blocking policy, what
// could not be scanned under one, what was scanned with a database beyond
// scan.maxDbAge under the block action.
func (e *Engine) scanRecipe(ctx context.Context, sink *taskSink, logger *slog.Logger, f *FetchedRecipe, rid string, zone zonePolicy) map[string]*taxonomy.Error {
	if e.scan.Scanner == nil {
		return nil
	}
	policy := e.policyFor(f.Recipe, zone, logger)
	refusals := map[string]*taxonomy.Error{}
	for i := range f.Recipe.Spec.Ingredients {
		ing := &f.Recipe.Spec.Ingredients[i]
		if !scanned(ing.Kind) {
			continue
		}
		itemName := rid + "/" + ing.Name
		failed := false
		sink.update(func(t *tasks.Task) bool {
			failed = itemFor(t, itemName).Status == tasks.StatusFailed
			return false
		})
		if failed {
			// Not in the store: nothing to scan, and the recipe is already
			// incomplete — it crosses nowhere.
			continue
		}
		repo, err := relocate.PathWithBase(e.base, ing.Ref)
		if err != nil {
			continue
		}
		ref := ing.Ref + "@" + ing.Digest
		subj := scan.Subject{Reference: ref, Recipe: rid}
		var rep *scan.Report
		res, err := e.scan.Scanner.Scan(ctx, scan.Target{Digest: ing.Digest, Repository: repo, Kind: string(ing.Kind)})
		if err != nil {
			var te *taxonomy.Error
			if !errors.As(err, &te) {
				te = taxonomy.New(taxonomy.CodeScanFailed, taxonomy.Params{"reference": ref, "detail": err.Error()}).WithCause(err)
			}
			rep = scan.NotScanned(&policy, subj, te)
		} else {
			rep = scan.Evaluate(&policy, subj, res, e.scan.Freshness, e.scan.Now())
		}
		sink.update(func(t *tasks.Task) bool {
			itemFor(t, itemName).Scan = rep
			return true
		})
		if e.scan.Observe != nil {
			e.scan.Observe(rep)
		}
		logScanReport(ctx, logger, ing, rep)
		if refusal := rep.Refusal(); refusal != nil {
			refusals[ing.Name] = refusal
			audit.Log(ctx, logger, &audit.Event{
				Actor:   audit.ActorLocal,
				Action:  audit.ActionScanBlock,
				Target:  ref,
				Outcome: audit.OutcomeDenied,
				Origin:  audit.OriginLocal,
			})
		}
	}
	return refusals
}

// logScanReport writes the per-item record: the verdict, the counts, the
// database age — on every report, as R-13 requires — and the reasons.
func logScanReport(ctx context.Context, logger *slog.Logger, ing *spec.Ingredient, rep *scan.Report) {
	attrs := []slog.Attr{
		slog.String("ingredient", ing.Name),
		slog.String("digest", ing.Digest),
		slog.String("verdict", string(rep.Policy.Verdict)),
		slog.String("mode", string(rep.Policy.Policy.Mode)),
		slog.String("threshold", scan.FormatScore(rep.Policy.Policy.Threshold)),
		slog.String("threshold_level", string(rep.Policy.Policy.ThresholdLevel)),
		slog.Int("critical", rep.Counts.Critical), slog.Int("high", rep.Counts.High),
		slog.Int("medium", rep.Counts.Medium), slog.Int("low", rep.Counts.Low), slog.Int("unknown", rep.Counts.Unknown),
		slog.String("requirement", "FR-031"),
	}
	if rep.Database != nil {
		attrs = append(attrs,
			slog.String("db_version", rep.Database.Version), slog.String("db_digest", rep.Database.Digest),
			slog.Int64("db_age_seconds", rep.Database.AgeSeconds), slog.Bool("db_stale", rep.Database.Stale))
	}
	if len(rep.Policy.Violations) > 0 {
		attrs = append(attrs, slog.String("violations", strings.Join(rep.Policy.Violations, ",")))
	}
	codes := make([]string, 0, len(rep.Policy.Reasons))
	for _, r := range rep.Policy.Reasons {
		codes = append(codes, string(r.Code))
	}
	if len(codes) > 0 {
		attrs = append(attrs, slog.String("reasons", strings.Join(codes, ",")))
	}
	level := slog.LevelInfo
	msg := "ingredient scanned"
	switch {
	case rep.Blocks():
		level, msg = slog.LevelWarn, "ingredient refused by the vulnerability policy"
	case rep.Policy.Verdict == scan.VerdictNotScanned:
		level, msg = slog.LevelWarn, "ingredient not scanned: the policy is advisory, it proceeds"
	case rep.Policy.Verdict == scan.VerdictWarn:
		level, msg = slog.LevelWarn, "ingredient violates the advisory vulnerability policy: it proceeds"
	}
	logger.LogAttrs(ctx, level, msg, attrs...)
}

// withholdRecipe refuses a recipe that carries a refused ingredient, at the
// granularity of R-19: the recipe is withheld whole. Its artifact is not
// stored in the local cookbook (a zone below cannot resolve it), it is not
// recorded in the recipe graph (a medium does not deliver it: the media
// manifest is built from that graph, FR-054), and nothing of it is pushed
// to a destination — the refused ingredient's push item and the recipe's
// own say why. The other recipes of the run are unaffected.
func (e *Engine) withholdRecipe(ctx context.Context, sink *taskSink, logger *slog.Logger, f *FetchedRecipe, rid string, refusals map[string]*taxonomy.Error) {
	names := make([]string, 0, len(refusals))
	for n := range refusals {
		names = append(names, n)
	}
	sort.Strings(names)
	first := refusals[names[0]]
	sink.update(func(t *tasks.Task) bool {
		item := itemFor(t, rid+"/recipe")
		item.Status = tasks.StatusFailed
		item.Error = tasks.FromTaxonomy(first)
		return true
	})
	if e.dest != nil {
		for _, n := range names {
			_ = e.pushFailed(sink, logger, rid, n, refusals[n], n)
		}
		_ = e.pushFailed(sink, logger, rid, "recipe", first, rid)
	}
	// A recipe recorded by an earlier run — scanned then with an older
	// database, or not scanned at all — is withdrawn from the graph, so a
	// medium written now does not deliver it.
	if d, ok := e.store.(interface {
		DeleteRecipeRecord(name, version string) error
	}); ok {
		if err := d.DeleteRecipeRecord(f.Recipe.Metadata.Name, f.Recipe.Metadata.Version); err != nil {
			logger.LogAttrs(ctx, slog.LevelWarn, "withheld recipe could not be withdrawn from the recipe graph",
				slog.String("recipe", rid), slog.String("error", err.Error()))
		}
	}
	// The ingredients were fetched before they could be scanned: their
	// bytes stay in the store (on a medium, they travel), delivered by
	// nothing — no recipe record reaches them, so no destination verifies
	// nor pushes them, and `tobby store verify` lists them as orphans
	// (TBY-INT-003). Said here, where the operator reads the refusal.
	logger.LogAttrs(ctx, slog.LevelWarn, "recipe withheld by the vulnerability policy",
		slog.String("recipe", rid), slog.String("ingredients", strings.Join(names, ",")),
		slog.String("code", string(first.Code())),
		slog.String("content", "fetched content stays in the store, reachable from no recipe: never delivered, reported as orphaned by the store verification"),
		slog.String("requirement", "FR-031/R-19"))
}

// refreshAfter calls the Refresh hook when a recorded recipe may have
// delivered the scanner's database or binary.
func (e *Engine) refreshAfter(ctx context.Context, r *spec.Recipe) {
	if e.scan.Refresh == nil {
		return
	}
	for i := range r.Spec.Ingredients {
		switch r.Spec.Ingredients[i].Kind {
		case spec.IngredientOCIArtifact, spec.IngredientFileSet:
			e.scan.Refresh(ctx)
			return
		}
	}
}
