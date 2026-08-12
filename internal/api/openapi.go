// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api

import (
	_ "embed"
	"net/http"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
)

// The OpenAPI 3.1 document travels inside the binary (NFR-003) and is the
// FR-060 contract of /api/v1: every registered endpoint is described here,
// and a test cross-checks the route list against the document so an
// endpoint cannot ship undocumented. The document is deliberately static —
// info.version is the API major ("v1"); the binary's build version lives
// on /about.
//
//go:embed openapi.yaml
var openAPIDoc []byte

// OpenAPI returns the raw embedded OpenAPI document. The /api-docs viewer
// of the web UI parses it server-side (UI-SPEC §5.12).
func OpenAPI() []byte { return openAPIDoc }

// RegisterOpenAPI mounts GET /api/v1/openapi.yaml (FR-060). Reading the
// contract needs the viewer role, like every read of this API
// (UI-SPEC §10.2: nothing anonymous but the probes).
func RegisterOpenAPI(a *API) {
	a.Handle("GET /api/v1/openapi.yaml", a.RequireRole(auth.RoleViewer,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write(openAPIDoc)
		}))
}
