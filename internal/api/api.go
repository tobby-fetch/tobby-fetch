// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package api serves the versioned REST API under /api/v1 (FR-060), in
// strict parity with the web UI (FR-061): every UI action has its mirror
// endpoint, filters and search share the exact same parameters, and errors
// are the same taxonomy entries rendered as RFC 9457 problem documents.
// Payloads carry raw bytes and RFC 3339 timestamps exclusively —
// localization is a UI concern (ADR-0015 §7).
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// API is the /api/v1 surface. Feature packages register their endpoints on
// it (content, tasks, accounts…), each in its own file.
type API struct {
	mux    *http.ServeMux
	authn  *auth.Authenticator
	logger *slog.Logger
	// routes records every registered pattern in declaration order. Go's
	// ServeMux is write-only, and two tests need to walk the surface: the
	// OpenAPI cross-check (FR-060) and the RBAC matrix (FR-074), which
	// fails on any endpoint shipped without a documented role floor.
	routes []string
}

// New builds the API surface.
func New(authn *auth.Authenticator, logger *slog.Logger) *API {
	a := &API{mux: http.NewServeMux(), authn: authn, logger: logger}
	// Unknown API paths answer a problem document, never HTML.
	a.mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
		a.Problem(w, r, taxonomy.New(taxonomy.CodeNotFound, nil))
	})
	return a
}

// Handle registers an endpoint with a Go 1.22 method-aware pattern
// ("GET /api/v1/content").
func (a *API) Handle(pattern string, h http.HandlerFunc) {
	a.routes = append(a.routes, pattern)
	a.mux.HandleFunc(pattern, h)
}

// Routes returns the registered patterns, in declaration order. The
// catch-all registered by New is not one of them: it is the surface's 404,
// not an endpoint.
func (a *API) Routes() []string {
	out := make([]string, len(a.routes))
	copy(out, a.routes)
	return out
}

// Handler returns the authenticated handler to mount at /api/v1/.
func (a *API) Handler() http.Handler {
	return a.authn.API(a.mux)
}

// lang negotiates the problem-document language from Accept-Language: the
// API has no cookie, the header is the contract.
func lang(r *http.Request) string {
	return r.Header.Get("Accept-Language")
}

// Problem writes e as an RFC 9457 problem document with its entry's HTTP
// status.
func (a *API) Problem(w http.ResponseWriter, r *http.Request, e *taxonomy.Error) {
	taxonomy.WriteProblem(w, lang(r), e)
}

// JSON writes v with the given status.
func (a *API) JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		a.logger.LogAttrs(context.Background(), slog.LevelError, "encoding api response",
			slog.String("error", err.Error()))
	}
}

// RequireRole guards an endpoint behind a minimum role, answering the
// taxonomized 403 (same entry as the UI, FR-061).
func (a *API) RequireRole(floor auth.Role, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.IdentityFrom(r.Context())
		if !id.Role.AtLeast(floor) {
			a.Problem(w, r, taxonomy.New(taxonomy.CodeRoleDenied,
				taxonomy.Params{"role": string(floor)}))
			return
		}
		h(w, r)
	}
}
