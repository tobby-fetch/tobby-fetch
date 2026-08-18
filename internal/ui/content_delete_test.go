// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// seedProvenance stamps the FR-044 provenance fixture on the seeded
// content store: coredns is a recorded unit import, the wordpress image
// is recipe-managed through a recorded recipe, and the chart stays
// seeded (no record — pushed through /v2/).
func seedProvenance(t *testing.T, st *store.Store) {
	t.Helper()
	if err := st.SetProvenance("registry.k8s.io/coredns/coredns",
		&store.Provenance{Class: store.ProvenanceUnitImport}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRecipeRecord(&store.RecipeRecord{
		Name: "wordpress", Version: "6.4.2",
		CookbookRepo: "cookbook.example/recipes/wordpress",
		Digest:       "sha256:aaa1", ResolvedAt: t0, Verified: true,
		Ingredients: []store.IngredientRecord{
			{Name: "wordpress", Kind: "ContainerImage", Repo: "docker.io/bitnami/wordpress", Tag: "6.4.2", Digest: "sha256:d001"},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestRepoProvenanceZone: the repository page shows the provenance class,
// and the removal action follows the FR-044 amendment — live on
// unit-import for an admin, disabled with an explanation naming the
// managing recipes on recipe-managed content, disabled on seeded
// content; viewer and operator get no action at all.
func TestRepoProvenanceZone(t *testing.T) {
	st := seedContentStore(t)
	seedProvenance(t, st)
	u := newTestUIWithOptions(t, &Options{Store: st}, nil)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	// Unit import: provenance shown, live danger action with its dialog.
	w := get(t, mux, c, "/content/registry.k8s.io/coredns/coredns", nil)
	body := w.Body.String()
	for _, want := range []string{
		"Unit import",
		`data-dialog="delete-repo-dlg"`,
		`id="delete-repo-dlg"`,
		`action="/content/registry.k8s.io/coredns/coredns/-/delete"`,
		"Delete this repository",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("unit-import repo page misses %q", want)
		}
	}

	// Recipe-managed: the action is disabled and names the recipe — never
	// hidden (UI-SPEC §5.4).
	w = get(t, mux, c, "/content/docker.io/bitnami/wordpress", nil)
	body = w.Body.String()
	for _, want := range []string{
		"Recipe-managed",
		"wordpress@6.4.2",
		"disabled",
		"it goes away by removing the recipe",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("recipe-managed repo page misses %q", want)
		}
	}
	if strings.Contains(body, `data-dialog="delete-repo-dlg"`) {
		t.Error("recipe-managed repo page offers a live delete action")
	}

	// Seeded: disabled with the /v2/ explanation.
	w = get(t, mux, c, "/content/docker.io/bitnamicharts/wordpress", nil)
	body = w.Body.String()
	if !strings.Contains(body, "Seeded") || !strings.Contains(body, "disabled") {
		t.Error("seeded repo page misses the disabled action with its explanation")
	}
	if strings.Contains(body, `data-dialog="delete-repo-dlg"`) {
		t.Error("seeded repo page offers a live delete action")
	}

	// Viewer: no danger zone at all (standard role gating).
	cv := login(t, mux, "lecteur", "pw-view")
	w = get(t, mux, cv, "/content/registry.k8s.io/coredns/coredns", nil)
	if strings.Contains(w.Body.String(), "delete-repo-dlg") ||
		strings.Contains(w.Body.String(), "/-/delete") {
		t.Error("viewer repo page leaks the delete action")
	}
}

// TestContentDeleteFlow is the FR-044 amendment acceptance on the UI
// surface: an admin removes a unit-imported repository — redirect to the
// listing with a toast, content gone, FR-094 audit record — while
// recipe-managed content answers the 403 policy refusal TBY-POL-002,
// seeded content TBY-POL-003, and a viewer the role denial.
func TestContentDeleteFlow(t *testing.T) {
	st := seedContentStore(t)
	seedProvenance(t, st)
	var logs bytes.Buffer
	u := newTestUIWithOptions(t, &Options{Store: st}, &logs)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	// Viewer: refused by role before any policy runs.
	cv := login(t, mux, "lecteur", "pw-view")
	w := postForm(t, mux, cv, "/content/registry.k8s.io/coredns/coredns/-/delete",
		"csrf="+csrfOf(t, u, cv), nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
		t.Errorf("viewer delete: code=%d, want 403 TBY-AUTH-003", w.Code)
	}

	// Recipe-managed: 403 TBY-POL-002 naming the managing recipe.
	w = postForm(t, mux, c, "/content/docker.io/bitnami/wordpress/-/delete",
		"csrf="+csrfOf(t, u, c), nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-POL-002") {
		t.Errorf("recipe-managed delete: code=%d, want 403 TBY-POL-002", w.Code)
	}
	if !strings.Contains(w.Body.String(), "wordpress@6.4.2") {
		t.Error("policy refusal does not name the managing recipe")
	}

	// Seeded: 403 TBY-POL-003.
	w = postForm(t, mux, c, "/content/docker.io/bitnamicharts/wordpress/-/delete",
		"csrf="+csrfOf(t, u, c), nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-POL-003") {
		t.Errorf("seeded delete: code=%d, want 403 TBY-POL-003", w.Code)
	}

	// Unknown repository: the taxonomized 404, never a policy answer.
	w = postForm(t, mux, c, "/content/ghost.example.com/nothing/-/delete",
		"csrf="+csrfOf(t, u, c), nil)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "TBY-SRV-002") {
		t.Errorf("unknown delete: code=%d, want 404 TBY-SRV-002", w.Code)
	}

	// Missing CSRF: refused (NFR-012).
	w = postForm(t, mux, c, "/content/registry.k8s.io/coredns/coredns/-/delete", "", nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-004") {
		t.Errorf("delete without csrf: code=%d, want 403 TBY-AUTH-004", w.Code)
	}

	// Unit import as admin: 303 to the listing with the toast parameter…
	w = postForm(t, mux, c, "/content/registry.k8s.io/coredns/coredns/-/delete",
		"csrf="+csrfOf(t, u, c), nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d, want 303", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/content?deleted=") {
		t.Fatalf("delete redirect = %q", loc)
	}

	// …the listing renders the success toast (toasts carry successes only)…
	w = get(t, mux, c, loc, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "deleted.") {
		t.Errorf("post-delete listing: code=%d body misses the toast", w.Code)
	}
	if strings.Contains(w.Body.String(), `data-copy="registry.k8s.io/coredns/coredns"`) {
		t.Error("deleted repository still listed")
	}

	// …the repository is gone…
	w = get(t, mux, c, "/content/registry.k8s.io/coredns/coredns", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("deleted repo page = %d, want 404", w.Code)
	}

	// …and the FR-094 audit trail carries the six-field record.
	trail := logs.String()
	if !strings.Contains(trail, `"action":"content.delete"`) ||
		!strings.Contains(trail, `"target":"registry.k8s.io/coredns/coredns"`) ||
		!strings.Contains(trail, `"outcome":"success"`) ||
		!strings.Contains(trail, `"actor":"alexis"`) {
		t.Error("audit trail misses the content.delete success record")
	}
	if !strings.Contains(trail, `"outcome":"denied"`) {
		t.Error("audit trail misses the denied policy attempts")
	}

	// The htmx variant answers HX-Redirect (UI-SPEC §5.6 pattern). The
	// repository is gone, so the answer is the 404 — re-import first.
	w = postForm(t, mux, c, "/content/registry.k8s.io/coredns/coredns/-/delete",
		"csrf="+csrfOf(t, u, c), map[string]string{"HX-Request": "true", "HX-Target": "zone"})
	if w.Code != http.StatusNotFound {
		t.Errorf("re-delete = %d, want 404", w.Code)
	}
}

// TestContentDeleteUIAPIParity is the FR-061 invariant on the removal:
// the API mirror applies the same policy answers as the screen —
// TBY-POL-002 on recipe-managed content — and a successful API removal
// is visible on the screen.
func TestContentDeleteUIAPIParity(t *testing.T) {
	st := seedContentStore(t)
	seedProvenance(t, st)
	u := newTestUIWithOptions(t, &Options{Store: st}, nil)
	uiMux := mount(u)
	c := login(t, uiMux, "alexis", "pw-admin")

	restAPI := api.New(u.authn, slog.New(slog.DiscardHandler))
	api.RegisterRecipes(restAPI, &api.RecipeOptions{Store: st})
	apiMux := http.NewServeMux()
	apiMux.Handle("/api/v1/", restAPI.Handler())

	del := func(target, account, password string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodDelete, target, http.NoBody)
		r.SetBasicAuth(account, password)
		w := httptest.NewRecorder()
		apiMux.ServeHTTP(w, r)
		return w
	}

	// Same policy refusal as the screen on recipe-managed content.
	w := del("/api/v1/content/docker.io/bitnami/wordpress", "alexis", "pw-admin")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-POL-002") {
		t.Errorf("api recipe-managed delete: code=%d, want 403 TBY-POL-002", w.Code)
	}

	// Same role gate: a viewer is refused.
	w = del("/api/v1/content/registry.k8s.io/coredns/coredns", "lecteur", "pw-view")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
		t.Errorf("api viewer delete: code=%d, want 403 TBY-AUTH-003", w.Code)
	}

	// A successful API removal (204) is visible on the screen (404).
	w = del("/api/v1/content/registry.k8s.io/coredns/coredns", "alexis", "pw-admin")
	if w.Code != http.StatusNoContent {
		t.Fatalf("api delete = %d: %s", w.Code, w.Body.String())
	}
	if got := get(t, uiMux, c, "/content/registry.k8s.io/coredns/coredns", nil); got.Code != http.StatusNotFound {
		t.Errorf("screen after api delete = %d, want 404", got.Code)
	}
}

// TestRecipesUIAPIParity is the FR-061 invariant on the recipe surfaces:
// the API mirror lists exactly the recipes the screen shows, and the
// mapping endpoint carries the same ingredient repositories the screen
// links.
func TestRecipesUIAPIParity(t *testing.T) {
	st := openTestStore(t)
	seedRecipeRecords(t, st)
	u := newTestUIWithOptions(t, &Options{Store: st}, nil)
	uiMux := mount(u)
	c := login(t, uiMux, "lecteur", "pw-view")

	restAPI := api.New(u.authn, slog.New(slog.DiscardHandler))
	api.RegisterRecipes(restAPI, &api.RecipeOptions{Store: st})
	apiMux := http.NewServeMux()
	apiMux.Handle("/api/v1/", restAPI.Handler())

	r := httptest.NewRequest(http.MethodGet, "/api/v1/recipes", http.NoBody)
	r.SetBasicAuth("lecteur", "pw-view")
	wAPI := httptest.NewRecorder()
	apiMux.ServeHTTP(wAPI, r)
	if wAPI.Code != http.StatusOK {
		t.Fatalf("api recipes = %d: %s", wAPI.Code, wAPI.Body.String())
	}
	apiBody := wAPI.Body.String()

	uiBody := get(t, uiMux, c, "/recipes", nil).Body.String()
	for _, name := range []string{"wordpress", "redis"} {
		if !strings.Contains(apiBody, `"name":"`+name+`"`) {
			t.Errorf("api misses recipe %s", name)
		}
		if !strings.Contains(uiBody, ">"+name+"<") {
			t.Errorf("ui misses recipe %s", name)
		}
	}
	// The FR-033 label travels on both surfaces.
	if !strings.Contains(apiBody, `"trustScope":"legacy-zone"`) || !strings.Contains(uiBody, "scope legacy-zone") {
		t.Error("admitted-unsigned scope label missing from a surface")
	}

	r = httptest.NewRequest(http.MethodGet, "/api/v1/recipes/wordpress/mapping", http.NoBody)
	r.SetBasicAuth("lecteur", "pw-view")
	wAPI = httptest.NewRecorder()
	apiMux.ServeHTTP(wAPI, r)
	mapUI := get(t, uiMux, c, "/recipes/wordpress/mapping", nil).Body.String()
	for _, repo := range []string{"docker.io/bitnami/wordpress", "docker.io/bitnamicharts/wordpress"} {
		if !strings.Contains(wAPI.Body.String(), `"repo":"`+repo+`"`) {
			t.Errorf("api mapping misses %s", repo)
		}
		if !strings.Contains(mapUI, `href="/content/`+repo+`"`) {
			t.Errorf("ui mapping misses %s", repo)
		}
	}
}
