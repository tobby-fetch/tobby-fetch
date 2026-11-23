// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package hardening carries the per-instance switches of FR-098 (R-17)
// that remove a way in: hardening.disableUnitImport and
// hardening.disableRegistryPush.
//
// They are hardening, not relaxation — no acknowledgement is needed to
// set them — but every surface they close says so, with the dedicated
// code and the setting named, and every refusal is an audit event
// (hardening.refused, FR-094): an operator must never mistake a refusal
// for a fault. Recipe-driven writes (FR-027) go through the store
// directly (ADR-0005) and meet neither switch.
//
// The refusals are built here once, so the import screen, its API, the
// FileSet packing of every surface and the embedded registry answer with
// the same entry and the same parameters.
package hardening

import (
	"context"
	"log/slog"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The configuration keys, as the refusals and the reports name them.
const (
	SettingDisableUnitImport   = "hardening.disableUnitImport"
	SettingDisableRegistryPush = "hardening.disableRegistryPush"
)

// Switches is the effective pair of hardening settings of one instance.
// The zero value is the default: both off.
type Switches struct {
	// DisableUnitImport refuses unit imports (FR-023) and local FileSet
	// packing (FR-048) on every surface.
	DisableUnitImport bool
	// DisableRegistryPush makes the embedded registry read-only for every
	// client and every role.
	DisableRegistryPush bool
}

// UnitImportRefusal is the TBY-POL-005 refusal, naming the setting.
func UnitImportRefusal() *taxonomy.Error {
	return taxonomy.New(taxonomy.CodeUnitImportDisabled, taxonomy.Params{"setting": SettingDisableUnitImport})
}

// RegistryPushRefusal is the TBY-POL-006 refusal, naming the setting.
func RegistryPushRefusal() *taxonomy.Error {
	return taxonomy.New(taxonomy.CodeRegistryPushDisabled, taxonomy.Params{"setting": SettingDisableRegistryPush})
}

// AuditRefused records one write a switch refused (FR-094): who asked,
// from where, for what, and which setting refused it. One refusal is one
// record — the surface emits no second denial of its own.
func AuditRefused(ctx context.Context, logger *slog.Logger, actor, origin, target, setting string) {
	audit.Log(ctx, logger, &audit.Event{
		Actor:   actor,
		Action:  audit.ActionHardeningRefused,
		Target:  target,
		Outcome: audit.OutcomeDenied,
		Origin:  origin,
		Setting: setting,
	})
}
