// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

const testSource = "oci://cookbook.example/retriever:1"

// newRecipesAPI seeds a real store — one unit-imported repository, one
// recipe-managed one, one seeded one — records the recipe graph, and
// mounts the recipe endpoints behind real Basic authentication with the
// three roles.
func newRecipesAPI(t *testing.T, source string) (*http.ServeMux, *tasks.Queue) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing store: %v", err)
		}
	})
	srv := httptest.NewServer(st.APIHandler())
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	for _, repo := range []string{
		"docker.io/bitnami/wordpress",
		"registry.k8s.io/coredns/coredns",
		"docker.io/bitnamicharts/wordpress",
	} {
		img, err := random.Image(256, 1)
		if err != nil {
			t.Fatal(err)
		}
		ref, err := name.ParseReference(addr + "/" + repo + ":1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetProvenance("registry.k8s.io/coredns/coredns",
		&store.Provenance{Class: store.ProvenanceUnitImport}); err != nil {
		t.Fatal(err)
	}
	baseTime := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	fixtures := []store.RecipeRecord{
		{
			Name: "wordpress", Version: "6.8.2",
			CookbookRepo: "cookbook.example/recipes/wordpress",
			Digest:       "sha256:aaa1", Zone: "zone-a",
			ResolvedAt: baseTime, Verified: true,
			Ingredients: []store.IngredientRecord{
				{Name: "wordpress", Kind: "ContainerImage", Repo: "docker.io/bitnami/wordpress", Tag: "1.0.0", Digest: "sha256:d001"},
			},
		},
		{
			Name: "wordpress", Version: "6.8.1",
			CookbookRepo: "cookbook.example/recipes/wordpress",
			Digest:       "sha256:aaa0", Zone: "zone-a",
			ResolvedAt: baseTime.Add(-2 * time.Hour), Verified: false, TrustScope: "legacy-zone",
			Ingredients: []store.IngredientRecord{
				{Name: "wordpress", Kind: "ContainerImage", Repo: "docker.io/bitnami/wordpress", Tag: "0.9.0", Digest: "sha256:d000"},
			},
		},
	}
	for i := range fixtures {
		if err := st.PutRecipeRecord(&fixtures[i]); err != nil {
			t.Fatal(err)
		}
	}

	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := accounts.AddAccount("lecteur", auth.RoleViewer, "pw-view", now); err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("op", auth.RoleOperator, "pw-op", now); err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("root", auth.RoleAdmin, "pw-admin", now); err != nil {
		t.Fatal(err)
	}
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(time.Hour),
		Logger:   slog.New(slog.DiscardHandler),
	}
	queue, err := tasks.Open(t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	a := api.New(authn, slog.New(slog.DiscardHandler))
	api.RegisterRecipes(a, st, queue, source, []string{"legacy-zone"}, []string{"drivers"})
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())
	return mux, queue
}

// TestRecipesListEndpoint covers GET /api/v1/recipes: the recorded graph
// as-is, name-ordered and newest-first per name, with the FR-033
// verified/trustScope pair always explicit. Role viewer.
func TestRecipesListEndpoint(t *testing.T) {
	mux, _ := newRecipesAPI(t, testSource)

	w := call(t, mux, http.MethodGet, "/api/v1/recipes", "lecteur", "pw-view", "")
	if w.Code != http.StatusOK {
		t.Fatalf("recipes = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Recipes []struct {
			Name        string `json:"name"`
			Version     string `json:"version"`
			Zone        string `json:"zone"`
			Verified    bool   `json:"verified"`
			TrustScope  string `json:"trustScope"`
			Ingredients []struct {
				Repo   string `json:"repo"`
				Digest string `json:"digest"`
			} `json:"ingredients"`
		} `json:"recipes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Recipes) != 2 {
		t.Fatalf("recipes = %+v", resp.Recipes)
	}
	if resp.Recipes[0].Version != "6.8.2" || resp.Recipes[1].Version != "6.8.1" {
		t.Errorf("order = %s, %s — want newest resolution first", resp.Recipes[0].Version, resp.Recipes[1].Version)
	}
	if !resp.Recipes[0].Verified || resp.Recipes[0].TrustScope != "" {
		t.Errorf("verified recipe = %+v", resp.Recipes[0])
	}
	if resp.Recipes[1].Verified || resp.Recipes[1].TrustScope != "legacy-zone" {
		t.Errorf("admitted-unsigned recipe misses its scope (FR-033): %+v", resp.Recipes[1])
	}
	if len(resp.Recipes[0].Ingredients) != 1 || resp.Recipes[0].Ingredients[0].Repo != "docker.io/bitnami/wordpress" {
		t.Errorf("ingredients = %+v", resp.Recipes[0].Ingredients)
	}

	// Anonymous requests never reach the store.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/recipes", http.NoBody)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous recipes = %d, want 401", rec.Code)
	}
}

// TestRecipeMappingEndpoint covers GET /api/v1/recipes/{recipe}/mapping
// (FR-035, FR-065) and its taxonomized 404.
func TestRecipeMappingEndpoint(t *testing.T) {
	mux, _ := newRecipesAPI(t, testSource)

	w := call(t, mux, http.MethodGet, "/api/v1/recipes/wordpress/mapping", "lecteur", "pw-view", "")
	if w.Code != http.StatusOK {
		t.Fatalf("mapping = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Recipe   string `json:"recipe"`
		Versions []struct {
			Version     string `json:"version"`
			Ingredients []struct {
				Name   string `json:"name"`
				Kind   string `json:"kind"`
				Repo   string `json:"repo"`
				Tag    string `json:"tag"`
				Digest string `json:"digest"`
			} `json:"ingredients"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Recipe != "wordpress" || len(resp.Versions) != 2 || resp.Versions[0].Version != "6.8.2" {
		t.Fatalf("mapping = %+v", resp)
	}
	ing := resp.Versions[0].Ingredients
	if len(ing) != 1 || ing[0].Repo != "docker.io/bitnami/wordpress" || ing[0].Tag != "1.0.0" || ing[0].Digest != "sha256:d001" {
		t.Errorf("ingredients = %+v", ing)
	}

	w = call(t, mux, http.MethodGet, "/api/v1/recipes/ghost/mapping", "lecteur", "pw-view", "")
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "TBY-SRV-002") {
		t.Errorf("unknown mapping: code=%d, want 404 TBY-SRV-002", w.Code)
	}
}

// TestSyncEndpoint covers POST /api/v1/sync (FR-014): 201 with the
// created sync task for an operator, the taxonomized role denial for a
// viewer, and the coded configuration refusal without a source.
func TestSyncEndpoint(t *testing.T) {
	mux, queue := newRecipesAPI(t, testSource)

	w := call(t, mux, http.MethodPost, "/api/v1/sync", "op", "pw-op", "")
	if w.Code != http.StatusCreated {
		t.Fatalf("sync = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Task struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			Reference string `json:"reference"`
			Actor     string `json:"actor"`
		} `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Task.Type != tasks.TypeSync || resp.Task.Reference != testSource || resp.Task.Actor != "op" {
		t.Errorf("task = %+v", resp.Task)
	}
	if _, ok := queue.Get(resp.Task.ID); !ok {
		t.Error("created task not in the queue")
	}

	// Viewer: taxonomized role denial (FR-014 is operator+).
	w = call(t, mux, http.MethodPost, "/api/v1/sync", "lecteur", "pw-view", "")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
		t.Errorf("viewer sync: code=%d, want 403 TBY-AUTH-003", w.Code)
	}

	// No source configured: the coded refusal, never a silent no-op.
	mux2, _ := newRecipesAPI(t, "")
	w = call(t, mux2, http.MethodPost, "/api/v1/sync", "op", "pw-op", "")
	if !strings.Contains(w.Body.String(), "TBY-CFG-001") {
		t.Errorf("sync without source: code=%d body misses TBY-CFG-001", w.Code)
	}
}

// TestRetrieverEndpoint covers GET /api/v1/retriever: source (FR-010),
// relaxed scopes (FR-033), anonymous FileSets (FR-047), last sync task.
// Admin-gated, like the /admin/retriever screen (FR-061).
func TestRetrieverEndpoint(t *testing.T) {
	mux, queue := newRecipesAPI(t, testSource)

	w := call(t, mux, http.MethodGet, "/api/v1/retriever", "root", "pw-admin", "")
	if w.Code != http.StatusOK {
		t.Fatalf("retriever = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Source             string   `json:"source"`
		RelaxedTrustScopes []string `json:"relaxed_trust_scopes"`
		AnonymousFileSets  []string `json:"anonymous_filesets"`
		LastSync           *struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"last_sync"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Source != testSource ||
		len(resp.RelaxedTrustScopes) != 1 || resp.RelaxedTrustScopes[0] != "legacy-zone" ||
		len(resp.AnonymousFileSets) != 1 || resp.AnonymousFileSets[0] != "drivers" {
		t.Errorf("retriever = %+v", resp)
	}
	if resp.LastSync != nil {
		t.Errorf("last_sync = %+v before any trigger", resp.LastSync)
	}

	tk, err := queue.Create(tasks.TypeSync, testSource, "root", nil)
	if err != nil {
		t.Fatal(err)
	}
	w = call(t, mux, http.MethodGet, "/api/v1/retriever", "root", "pw-admin", "")
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.LastSync == nil || resp.LastSync.ID != tk.ID || resp.LastSync.Type != tasks.TypeSync {
		t.Errorf("last_sync = %+v", resp.LastSync)
	}

	// Below admin: the taxonomized 403 (same gate as the screen).
	w = call(t, mux, http.MethodGet, "/api/v1/retriever", "op", "pw-op", "")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
		t.Errorf("operator retriever: code=%d, want 403 TBY-AUTH-003", w.Code)
	}
}

// TestContentDeleteEndpoint covers DELETE /api/v1/content/{repo...}
// (FR-044 amendment): 204 for an admin on unit-import provenance, the
// policy refusals TBY-POL-002/TBY-POL-003 on recipe-managed and seeded
// content, the shared 404 on unknown paths, and the role gate.
func TestContentDeleteEndpoint(t *testing.T) {
	mux, _ := newRecipesAPI(t, testSource)

	// Below admin: refused.
	w := call(t, mux, http.MethodDelete, "/api/v1/content/registry.k8s.io/coredns/coredns", "op", "pw-op", "")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
		t.Errorf("operator delete: code=%d, want 403 TBY-AUTH-003", w.Code)
	}

	// Recipe-managed: TBY-POL-002 naming the managing recipes, as a
	// problem document.
	w = call(t, mux, http.MethodDelete, "/api/v1/content/docker.io/bitnami/wordpress", "root", "pw-admin", "")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-POL-002") {
		t.Errorf("recipe-managed delete: code=%d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("content-type = %s", ct)
	}
	if !strings.Contains(w.Body.String(), "wordpress@6.8") {
		t.Error("policy refusal does not name the managing recipes")
	}

	// Seeded: TBY-POL-003.
	w = call(t, mux, http.MethodDelete, "/api/v1/content/docker.io/bitnamicharts/wordpress", "root", "pw-admin", "")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-POL-003") {
		t.Errorf("seeded delete: code=%d, want 403 TBY-POL-003", w.Code)
	}

	// Unknown: the shared 404, never a policy answer.
	w = call(t, mux, http.MethodDelete, "/api/v1/content/ghost.example.com/nothing", "root", "pw-admin", "")
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "TBY-SRV-002") {
		t.Errorf("unknown delete: code=%d, want 404 TBY-SRV-002", w.Code)
	}

	// Unit import: 204, and the repository is gone (a second DELETE 404s).
	w = call(t, mux, http.MethodDelete, "/api/v1/content/registry.k8s.io/coredns/coredns", "root", "pw-admin", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", w.Code, w.Body.String())
	}
	w = call(t, mux, http.MethodDelete, "/api/v1/content/registry.k8s.io/coredns/coredns", "root", "pw-admin", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("re-delete = %d, want 404", w.Code)
	}
}
