// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api

// Routes reserved at the opening of milestone 7, so its tracks mount their
// endpoints in known places instead of negotiating them. A track registers
// its routes and documents them in openapi.yaml in the same change:
// TestOpenAPICoversEveryRoute holds the document to the registered routes.
// Track B's three are registered by RegisterOperations (operations.go). Every route follows the RBAC
// matrix (FR-074) and the UI/API parity of FR-061 with the screens of
// package ui (milestone7.go there).
const (
	// Track A — consumption aids (FR-065, 7.1), registered in consumption.go.
	// The existing GET /api/v1/recipes/{recipe}/mapping gains
	// ?format=csv|json|markdown: without it the answer is unchanged, with
	// it the exportable table; the whole store gets the same table.
	RouteMappingAll       = "GET /api/v1/mapping"                          // viewer: ?format=csv|json|markdown (default json)
	RouteRegistriesRecipe = "GET /api/v1/recipes/{recipe}/registries.yaml" // viewer
	RouteRegistriesAll    = "GET /api/v1/registries.yaml"                  // viewer

	// Track B — operations (FR-095, FR-096, FR-097).
	RouteDiagnosticsBundle = "POST /api/v1/diagnostics/bundle" // admin: the archive in the response, audited
	RouteClock             = "GET /api/v1/clock"               // viewer: the last FR-096 verdicts
	RouteState             = "GET /api/v1/state"               // admin: the FR-097 inventory, never contents

	// Track C — content controls (FR-031 R-15, FR-098). Provenance rides
	// on the existing content routes (content.go). RoutePolicy is
	// registered by RegisterPolicy (policy.go).
	RoutePolicy = "GET /api/v1/policy" // admin: exceptions with scope, source and state; VEX documents; hardening switches
)
