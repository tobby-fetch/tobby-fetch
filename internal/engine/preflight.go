// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/tobby-fetch/tobby-fetch/internal/preflight"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The FR-055 gate on the transfer path.
//
// The plan engine already computes everything the gate needs, so the gate
// IS a plan — run with the destination skipped, because a synchronization
// that cannot land locally will never get as far as pushing, and probing
// a destination to find that out would be one network round trip per
// artifact spent on a question already answered.
//
// It costs one manifest walk per cycle: version listings, recipe
// manifests and ingredient manifests, all of which the run that follows
// fetches anyway. No content byte is read, and the source-side size facts
// are cached on the pinned digests (see Planner.sizes), so a steady-state
// passthrough cycle re-reads little more than the tag lists it has to
// re-read regardless.

// preflightGate refuses a synchronization that cannot land.
//
// It fails OPEN on everything that is not a verdict. A plan that could
// not resolve a recipe, a store whose size could not be read, a registry
// that timed out — none of those says the transfer will not fit, and
// grounding a synchronization because the check itself broke would make
// the safety feature the least reliable part of the product. Only an
// actual refusal — not enough space, a filesystem that cannot hold the
// largest file — stops the run.
func (e *Engine) preflightGate(ctx context.Context, logger *slog.Logger) error {
	plan, err := e.planner.Plan(ctx, PlanOptions{SkipDestination: true})
	if err != nil {
		logger.LogAttrs(ctx, slog.LevelWarn, "pre-flight could not be computed",
			slog.String("error", err.Error()), slog.String("requirement", "FR-055"))
		return nil
	}

	for i := range plan.Checks {
		c := &plan.Checks[i]
		for _, w := range c.Warnings {
			logger.LogAttrs(ctx, slog.LevelWarn, "pre-flight warning",
				slog.String("target", string(c.Target)),
				slog.String("path", c.Path),
				slog.String("warning", string(w)),
				slog.String("filesystem", c.Filesystem.Type),
				slog.String("requirement", "FR-055"))
		}
	}

	logger.LogAttrs(ctx, slog.LevelInfo, "pre-flight computed",
		slog.Int64("transfer_bytes", plan.Totals.TransferBytes),
		slog.Int64("largest_file_bytes", plan.Totals.LargestFileBytes),
		slog.Int64("store_bytes", plan.Totals.StoreBytes),
		slog.Int64("projected_store_bytes", plan.Totals.ProjectedStoreBytes),
		slog.Int("recipes", plan.Totals.Recipes),
		slog.Int("new", plan.Totals.New),
		slog.Int("outdated", plan.Totals.Outdated),
		slog.Int("up_to_date", plan.Totals.UpToDate),
		slog.String("requirement", "FR-055"))

	refusal := preflightRefusal(plan)
	if refusal == nil {
		return nil
	}
	if e.planner.cfg.MarginDisabled {
		// FR-075 shape: the safety check is removed only by an explicit
		// opt-in, and the opt-in is loud every time it is exercised —
		// never a silent pass.
		logger.LogAttrs(ctx, slog.LevelWarn, "pre-flight refusal overridden by configuration",
			slog.String("code", string(refusal.Code())),
			slog.String("setting", "preflight.disabled"),
			slog.String("requirement", "FR-055/FR-075"))
		return nil
	}
	logger.LogAttrs(ctx, slog.LevelError, "synchronization refused before any transfer",
		slog.String("code", string(refusal.Code())),
		slog.String("requirement", "FR-055"))
	return refusal
}

// preflightRefusal extracts the blocking verdict of a plan, if any.
func preflightRefusal(plan *Plan) *taxonomy.Error {
	for i := range plan.Checks {
		c := &plan.Checks[i]
		if c.OK() {
			continue
		}
		switch c.RefusalCode {
		case taxonomy.CodeFileTooLarge:
			return taxonomy.New(taxonomy.CodeFileTooLarge, taxonomy.Params{
				"path":       c.Path,
				"filesystem": c.Filesystem.Type,
				"limit":      itoa(c.Filesystem.MaxFileSize),
				"size":       itoa(c.LargestFileBytes),
				"what":       string(c.Target),
			})
		case taxonomy.CodeInsufficientSpace:
			return taxonomy.New(taxonomy.CodeInsufficientSpace, taxonomy.Params{
				"path":      c.Path,
				"needed":    itoa(c.ProjectedBytes),
				"available": itoa(c.UsableBytes),
				"shortfall": itoa(c.ShortfallBytes),
				"margin":    itoa(int64(c.MarginPercent)),
				"free":      itoa(c.Space.FreeBytes),
			})
		}
	}
	return nil
}

// fileTooLargeError turns the operating system's mid-write "file too
// large" into the same taxonomy entry the pre-flight refusal uses
// (FR-055), naming the filesystem the store actually sits on.
//
// The two conditions are one problem seen at two moments, and giving them
// one code is what lets the message say the same thing: an operator who
// hit the ceiling three hours into a transfer needs the sentence about
// reformatting the medium just as much as the one who was refused before
// starting — arguably more.
func fileTooLargeError(err error, root string, size int64) *taxonomy.Error {
	if !preflight.IsFileTooLarge(err) {
		return nil
	}
	fsType := "the target filesystem"
	limit := int64(0)
	if fs, _, ierr := preflight.System.Inspect(root); ierr == nil && fs.Type != "" {
		fsType = fs.Type
		limit = fs.MaxFileSize
	}
	return taxonomy.New(taxonomy.CodeFileTooLarge, taxonomy.Params{
		"path":       root,
		"filesystem": fsType,
		"limit":      itoa(limit),
		"size":       itoa(size),
		"what":       string(preflight.TargetStore),
	}).WithCause(err)
}

// itoa renders a byte count for a taxonomy parameter. The templates state
// raw bytes on purpose: a rendered "3.9 GiB" is a number an operator
// cannot subtract, and the localized form is the UI's job (ADR-0015 §7).
func itoa(n int64) string { return strconv.FormatInt(n, 10) }
