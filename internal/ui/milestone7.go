// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

// Screens reserved at the opening of milestone 7 (ADR-0015: canonical URLs,
// "/-/" before a sub-resource), the UI halves of the API routes reserved in
// package api (milestone7.go there, FR-061). None is registered yet; a
// track registers its screen, its navigation entry and its RBAC-matrix row
// in one change.
const (
	// Track A — consumption aids (7.1). The existing recipe mapping screen
	// (/recipes/{recipe}/mapping) gains the exports and the snippet.
	PathMappingExport     = "/recipes/{recipe}/mapping/-/export"          // viewer: ?format=
	PathMappingRegistries = "/recipes/{recipe}/mapping/-/registries.yaml" // viewer
	PathConsumption       = "/consumption"                                // viewer: the whole store, same aids

	// Track B — operations (R-30, R-27, R-32).
	PathDiagnostics       = "/admin/diagnostics"        // admin
	PathDiagnosticsBundle = "/admin/diagnostics/bundle" // admin, POST
	PathState             = "/admin/state"              // admin

	// Track C — content controls (R-15, R-17).
	PathPolicy = "/admin/policy" // admin: exceptions and hardening, read-only (configuration file only)
)
