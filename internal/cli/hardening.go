// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"log/slog"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/hardening"
	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// setupHardening states the FR-098 switches the way every security setting
// is stated (R-17): one startup record with both values, whatever they
// are, one warning per switch set naming what it closes, and the two
// metric families — tobby_hardening_enabled for both settings, always,
// and the unmanaged-content gauge read from the store at scrape time.
//
// The switches are hardening, not relaxation: no acknowledgement is asked
// for. They are announced anyway so that an operator never takes a refusal
// for a fault — a docker push answering DENIED on an instance that says,
// in its first lines, that its registry is read-only.
func setupHardening(ctx context.Context, cfg *config.Config, st *store.Store, reg *metrics.Registry, logger *slog.Logger) hardening.Switches {
	sw := hardening.Switches{
		DisableUnitImport:   cfg.Hardening.DisableUnitImport,
		DisableRegistryPush: cfg.Hardening.DisableRegistryPush,
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "hardening switches",
		slog.Bool(hardening.SettingDisableUnitImport, sw.DisableUnitImport),
		slog.Bool(hardening.SettingDisableRegistryPush, sw.DisableRegistryPush),
		slog.String("requirement", "FR-098"))
	if sw.DisableUnitImport {
		logger.LogAttrs(ctx, slog.LevelWarn, "unit imports, OCI layout imports and FileSet packing are disabled",
			slog.String("setting", hardening.SettingDisableUnitImport),
			slog.String("effect", "the import screen, its API, every OCI layout import surface and every packing surface refuse with TBY-POL-005; recipe synchronizations and media imports are unaffected"),
			slog.String("requirement", "FR-098"))
	}
	if sw.DisableRegistryPush {
		logger.LogAttrs(ctx, slog.LevelWarn, "the embedded registry is read-only",
			slog.String("setting", hardening.SettingDisableRegistryPush),
			slog.String("effect", "every /v2/ write answers DENIED with TBY-POL-006, for every client and role; pulls and recipe synchronizations are unaffected, and seeding this instance by push is impossible"),
			slog.String("requirement", "FR-098"))
	}
	reg.RegisterHardening(sw.DisableUnitImport, sw.DisableRegistryPush)
	reg.RegisterUnmanaged(func() (map[string]int, error) {
		counts, err := st.UnmanagedRepositories(context.Background())
		if err != nil {
			return nil, err
		}
		return map[string]int{
			metrics.ClassUnitImport: counts[store.ProvenanceUnitImport],
			metrics.ClassSeed:       counts[store.ProvenanceSeed],
		}, nil
	})
	return sw
}
