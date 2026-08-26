// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
)

// registeredRoutes is the hard-coded list of every route registered on the
// /api/v1 mux (RegisterContent, RegisterTasks, RegisterAccounts,
// RegisterOpenAPI), in OpenAPI path syntax. Adding an endpoint without
// documenting it in openapi.yaml — or documenting a route that is not
// registered — fails the cross-check below (FR-060).
var registeredRoutes = []string{
	"POST /api/v1/account/password",
	"GET /api/v1/accounts",
	"POST /api/v1/accounts",
	"PATCH /api/v1/accounts/{name}",
	"DELETE /api/v1/accounts/{name}",
	"GET /api/v1/content",
	"DELETE /api/v1/content/{repo}",
	"GET /api/v1/content/{repo}",
	"GET /api/v1/content/{repo}/-/tags/{tag}",
	"GET /api/v1/filesets",
	"POST /api/v1/filesets/pack",
	"POST /api/v1/import",
	"POST /api/v1/oci-layout/export",
	"POST /api/v1/oci-layout/import",
	"POST /api/v1/oci-layout/plan",
	"GET /api/v1/import/inspect",
	"GET /api/v1/media",
	"POST /api/v1/media/import",
	"POST /api/v1/media/verify",
	"GET /api/v1/network",
	"PUT /api/v1/network/certificate",
	"GET /api/v1/openapi.yaml",
	"POST /api/v1/plan",
	"GET /api/v1/recipes",
	"GET /api/v1/recipes/{recipe}/mapping",
	"POST /api/v1/recipes/publish",
	"GET /api/v1/retriever",
	"PUT /api/v1/retriever/interval",
	"DELETE /api/v1/retriever/interval",
	"POST /api/v1/store/reset",
	"POST /api/v1/sync",
	"GET /api/v1/sync/prune-preview",
	"GET /api/v1/tasks",
	"GET /api/v1/tasks/{id}",
	"GET /api/v1/tasks/{id}/logs",
	"GET /api/v1/tokens",
	"POST /api/v1/tokens",
	"POST /api/v1/tokens/{name}/revoke",
}

// openAPIOperations extracts "METHOD path" pairs from the embedded
// document.
func openAPIOperations(t *testing.T) []string {
	t.Helper()
	var doc struct {
		OpenAPI string                    `yaml:"openapi"`
		Info    struct{ Version string }  `yaml:"info"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(api.OpenAPI(), &doc); err != nil {
		t.Fatalf("openapi.yaml does not parse: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Errorf("openapi = %q, want a 3.1 document", doc.OpenAPI)
	}
	if doc.Info.Version != "v1" {
		t.Errorf("info.version = %q, want the static API major %q (the build version lives on /about)", doc.Info.Version, "v1")
	}
	var ops []string
	for path, item := range doc.Paths {
		for _, method := range []string{"get", "post", "put", "patch", "delete"} {
			if _, ok := item[method]; ok {
				ops = append(ops, strings.ToUpper(method)+" "+path)
			}
		}
	}
	sort.Strings(ops)
	return ops
}

// TestOpenAPICoversEveryRoute cross-checks the registered route list with
// the embedded document, both directions.
func TestOpenAPICoversEveryRoute(t *testing.T) {
	ops := openAPIOperations(t)
	documented := map[string]bool{}
	for _, op := range ops {
		documented[op] = true
	}
	for _, route := range registeredRoutes {
		if !documented[route] {
			t.Errorf("registered route %q is missing from openapi.yaml (FR-060)", route)
		}
	}
	registered := map[string]bool{}
	for _, route := range registeredRoutes {
		registered[route] = true
	}
	for _, op := range ops {
		if !registered[op] {
			t.Errorf("openapi.yaml documents %q, which is not a registered route", op)
		}
	}
}

// TestOpenAPIProblemSchema pins the documented RFC 9457 error schema onto
// the taxonomy's extension members: every error response of the API
// references it.
func TestOpenAPIProblemSchema(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(api.OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	problem, ok := doc.Components.Schemas["Problem"]
	if !ok {
		t.Fatal("openapi.yaml misses the shared Problem schema")
	}
	for _, member := range []string{"type", "title", "status", "code", "probable_cause", "action", "correlation_id"} {
		if _, ok := problem.Properties[member]; !ok {
			t.Errorf("Problem schema misses the %q member of the taxonomy contract", member)
		}
	}
	// Every documented error response points at the shared schema.
	raw := string(api.OpenAPI())
	refs := strings.Count(raw, `$ref: "#/components/responses/Problem"`)
	if refs < len(registeredRoutes) {
		t.Errorf("only %d operations reference the shared Problem response for %d routes", refs, len(registeredRoutes))
	}
}

// TestOpenAPIEndpoint: the document is served as application/yaml behind
// authentication, viewer role (UI-SPEC §10.2: nothing anonymous but the
// probes).
func TestOpenAPIEndpoint(t *testing.T) {
	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("lecteur", auth.RoleViewer, "pw-view", time.Now()); err != nil {
		t.Fatal(err)
	}
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(time.Hour),
		Logger:   slog.New(slog.DiscardHandler),
	}
	a := api.New(authn, slog.New(slog.DiscardHandler))
	api.RegisterOpenAPI(a)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())

	// Anonymous: refused with a problem document.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", http.NoBody)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous openapi.yaml = %d, want 401", w.Code)
	}

	r = httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", http.NoBody)
	r.SetBasicAuth("lecteur", "pw-view")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("openapi.yaml = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}
	if !strings.Contains(w.Body.String(), "openapi: 3.1.0") {
		t.Error("served document does not carry the OpenAPI 3.1 header")
	}
}
