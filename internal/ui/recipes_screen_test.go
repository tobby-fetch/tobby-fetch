// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// newTestUIWithOptions wires a full UI over a real account store — admin,
// operator, and viewer accounts — honoring the given Options (store,
// retriever source, banners, queue). A nil logw discards the logs; a
// writer captures them for audit assertions (FR-094).
func newTestUIWithOptions(t *testing.T, opts *Options, logw io.Writer) *UI {
	t.Helper()
	accounts := testAccounts(t)
	// This file's screens exercise the operator floor, so it adds the one
	// account the shared fixture leaves out — admin_screen_test creates
	// "op" itself and must not find it already there.
	if err := accounts.AddAccount("op", auth.RoleOperator, "pw-op", t0); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	if logw != nil {
		logger = slog.New(slog.NewJSONHandler(logw, nil))
	}
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(12 * time.Hour),
		Logger:   logger,
	}
	if opts.Version == "" {
		opts.Version = "0.3.0-test"
	}
	if opts.Mode == "" {
		opts.Mode = "mirror"
	}
	if opts.Store == nil {
		opts.Store = openTestStore(t)
	}
	return New(authn, logger, opts)
}

// newTestQueue opens a fresh, unstarted persistent queue: Create enqueues
// without running anything — the tests observe the created tasks.
func newTestQueue(t *testing.T) *tasks.Queue {
	t.Helper()
	q, err := tasks.Open(t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// csrfOf resolves the session's CSRF token from its cookie.
func csrfOf(t *testing.T, u *UI, c *http.Cookie) string {
	t.Helper()
	sess, ok := u.authn.Sessions.Get(c.Value, t0)
	if !ok {
		t.Fatal("no session for cookie")
	}
	return sess.CSRF
}

// postForm performs an authenticated form POST.
func postForm(t *testing.T, mux *http.ServeMux, c *http.Cookie, target, form string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(c)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// seedRecipeRecords stores the recipe graph fixture: two versions of
// wordpress — the older one admitted unsigned through a declared scope
// (FR-033) — and one redis recipe.
func seedRecipeRecords(t *testing.T, st *store.Store) {
	t.Helper()
	fixtures := []store.RecipeRecord{
		{
			Name: "wordpress", Version: "6.8.2",
			CookbookRepo: "cookbook.example/recipes/wordpress",
			Digest:       "sha256:aaa1", Zone: "zone-a",
			ResolvedAt: t0, Verified: true,
			Ingredients: []store.IngredientRecord{
				{Name: "wordpress", Kind: "ContainerImage", Repo: "docker.io/bitnami/wordpress", Tag: "6.8.2", Digest: "sha256:d001"},
				{Name: "chart", Kind: "HelmChart", Repo: "docker.io/bitnamicharts/wordpress", Tag: "26.0.0", Digest: "sha256:d002"},
			},
		},
		{
			Name: "wordpress", Version: "6.8.1",
			CookbookRepo: "cookbook.example/recipes/wordpress",
			Digest:       "sha256:aaa0", Zone: "zone-a",
			ResolvedAt: t0.Add(-2 * time.Hour), Verified: false, TrustScope: "legacy-zone",
			Ingredients: []store.IngredientRecord{
				{Name: "wordpress", Kind: "ContainerImage", Repo: "docker.io/bitnami/wordpress", Tag: "6.8.1", Digest: "sha256:d000"},
			},
		},
		{
			Name: "redis", Version: "8.0.0",
			CookbookRepo: "cookbook.example/recipes/redis",
			Digest:       "sha256:bbb0",
			ResolvedAt:   t0.Add(-time.Hour), Verified: true,
			Ingredients: []store.IngredientRecord{
				{Name: "redis", Kind: "ContainerImage", Repo: "docker.io/library/redis", Tag: "8.0.0", Digest: "sha256:d100"},
			},
		},
	}
	for i := range fixtures {
		if err := st.PutRecipeRecord(&fixtures[i]); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRecipesListEmptyState: a store without recipes renders the explicit
// empty state, and the sync trigger is gated — live for operator+ with a
// source, disabled with an explanation otherwise, never hidden.
func TestRecipesListEmptyState(t *testing.T) {
	u := newTestUIWithOptions(t, &Options{RetrieverSource: "oci://cookbook.example/retriever:1"}, nil)
	mux := mount(u)

	c := login(t, mux, "op", "pw-op")
	w := get(t, mux, c, "/recipes", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/recipes = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		"No recipe recorded yet.",
		"oci://cookbook.example/retriever:1",
		`action="/recipes/sync"`, // live trigger for operator+ with a source
	} {
		if !strings.Contains(body, want) {
			t.Errorf("operator /recipes misses %q", want)
		}
	}

	// Viewer: the trigger is disabled with the role explanation (UI-SPEC
	// §7: never hidden).
	cv := login(t, mux, "lecteur", "pw-view")
	w = get(t, mux, cv, "/recipes", nil)
	body = w.Body.String()
	if strings.Contains(body, `action="/recipes/sync"`) {
		t.Error("viewer /recipes offers a live sync form")
	}
	if !strings.Contains(body, "disabled") || !strings.Contains(body, "operator role") {
		t.Error("viewer /recipes misses the disabled trigger with its role explanation")
	}
}

// TestRecipesListNoSource: without a configured Retriever source the
// header says so and the trigger is disabled with the explanation, even
// for an operator (FR-010).
func TestRecipesListNoSource(t *testing.T) {
	u := newTestUIWithOptions(t, &Options{}, nil)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := get(t, mux, c, "/recipes", nil)
	body := w.Body.String()
	if strings.Contains(body, `action="/recipes/sync"`) {
		t.Error("sync form live without a configured source")
	}
	for _, want := range []string{"not configured", "disabled", "No Retriever source is configured"} {
		if !strings.Contains(body, want) {
			t.Errorf("/recipes without source misses %q", want)
		}
	}
}

// TestRecipesListPopulated: the recorded recipes render with name, kind
// badge, version, zone, cookbook, resolution date, ingredient count, the
// mapping link — and the verified state is never silent: an
// admitted-unsigned recipe names its declared scope (FR-033).
func TestRecipesListPopulated(t *testing.T) {
	st := openTestStore(t)
	seedRecipeRecords(t, st)
	u := newTestUIWithOptions(t, &Options{Store: st, RetrieverSource: "oci://cookbook.example/retriever:1"}, nil)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	w := get(t, mux, c, "/recipes", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/recipes = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		">wordpress<", ">redis<",
		"t-badge--recipe", ">Recipe<",
		">6.8.2<", ">6.8.1<", ">8.0.0<",
		"zone-a",
		"cookbook.example/recipes/wordpress",
		`href="/recipes/wordpress/mapping"`,
		`href="/recipes/redis/mapping"`,
		">verified<",          // FR-033: explicit OK badge
		">scope legacy-zone<", // FR-033: admitted-unsigned names its scope
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/recipes misses %q", want)
		}
	}
	// Name-ordered: redis before wordpress; per name, newest first.
	if strings.Index(body, ">redis<") > strings.Index(body, ">wordpress<") {
		t.Error("recipes are not name-ordered")
	}
	if strings.Index(body, ">6.8.2<") > strings.Index(body, ">6.8.1<") {
		t.Error("wordpress versions are not newest-first")
	}
}

// TestRecipeMappingScreen: one section per recorded version, newest
// first, ingredient rows with the local relocated path linked to the
// content detail, tag, and copiable digest (FR-035, FR-065). Unknown
// recipes answer the taxonomized 404.
func TestRecipeMappingScreen(t *testing.T) {
	st := openTestStore(t)
	seedRecipeRecords(t, st)
	u := newTestUIWithOptions(t, &Options{Store: st}, nil)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	w := get(t, mux, c, "/recipes/wordpress/mapping", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("mapping = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		"Version 6.8.2", "Version 6.8.1",
		`href="/content/docker.io/bitnami/wordpress"`,
		`href="/content/docker.io/bitnamicharts/wordpress"`,
		`data-copy="docker.io/bitnami/wordpress"`,
		`data-copy="sha256:d001"`,
		"t-badge--image", "t-badge--chart",
		">26.0.0<",
		`href="/recipes"`, // breadcrumb back to the listing
		"scope legacy-zone",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mapping misses %q", want)
		}
	}
	if strings.Index(body, "Version 6.8.2") > strings.Index(body, "Version 6.8.1") {
		t.Error("mapping versions are not newest-first")
	}

	w = get(t, mux, c, "/recipes/ghost/mapping", nil)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "TBY-SRV-002") {
		t.Errorf("unknown recipe mapping: code=%d, want taxonomized 404", w.Code)
	}
}

// TestRecipesSyncTrigger is the FR-014 acceptance on the UI surface: an
// operator trigger creates one tracked sync task and redirects to it
// (HX-Redirect for htmx, 303 otherwise); a viewer is refused with the
// taxonomized role denial; without a source the trigger answers
// TBY-CFG-001 — never a silent no-op.
func TestRecipesSyncTrigger(t *testing.T) {
	queue := newTestQueue(t)
	const source = "https://retriever.example/retriever.yaml"
	u := newTestUIWithOptions(t, &Options{Queue: queue, RetrieverSource: source}, nil)
	mux := mount(u)

	c := login(t, mux, "op", "pw-op")
	w := postForm(t, mux, c, "/recipes/sync", "csrf="+csrfOf(t, u, c), map[string]string{"HX-Request": "true"})
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("HX-Redirect"), "/tasks/") {
		t.Fatalf("htmx sync: code=%d hx-redirect=%q", w.Code, w.Header().Get("HX-Redirect"))
	}
	created := queue.List("", tasks.TypeSync, "")
	if len(created) != 1 {
		t.Fatalf("sync tasks = %d, want 1", len(created))
	}
	if created[0].Reference != source || created[0].Actor != "op" || len(created[0].Items) != 0 {
		t.Errorf("task = ref %q actor %q items %d", created[0].Reference, created[0].Actor, len(created[0].Items))
	}

	// Plain POST: 303 to the task.
	w = postForm(t, mux, c, "/recipes/sync", "csrf="+csrfOf(t, u, c), nil)
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/tasks/") {
		t.Errorf("plain sync: code=%d location=%q", w.Code, w.Header().Get("Location"))
	}

	// Viewer: taxonomized role denial (TBY-AUTH-003).
	cv := login(t, mux, "lecteur", "pw-view")
	w = postForm(t, mux, cv, "/recipes/sync", "csrf="+csrfOf(t, u, cv), nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
		t.Errorf("viewer sync: code=%d, want 403 TBY-AUTH-003", w.Code)
	}

	// No source configured: the coded configuration refusal.
	u2 := newTestUIWithOptions(t, &Options{Queue: newTestQueue(t)}, nil)
	mux2 := mount(u2)
	c2 := login(t, mux2, "alexis", "pw-admin")
	w = postForm(t, mux2, c2, "/recipes/sync", "csrf="+csrfOf(t, u2, c2), nil)
	if !strings.Contains(w.Body.String(), "TBY-CFG-001") {
		t.Errorf("sync without source: code=%d body misses TBY-CFG-001", w.Code)
	}
}

// TestAdminRetrieverScreen: the read-only configuration screen shows the
// source (FR-010), the relaxed scopes (FR-033), the anonymous FileSets
// (FR-047), and the last sync task; admin-gated.
func TestAdminRetrieverScreen(t *testing.T) {
	queue := newTestQueue(t)
	u := newTestUIWithOptions(t, &Options{
		Queue:              queue,
		RetrieverSource:    "oci://cookbook.example/retriever:1",
		RelaxedTrustScopes: []string{"legacy-zone"},
		AnonymousFileSets:  []string{"drivers"},
	}, nil)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := get(t, mux, c, "/admin/retriever", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/admin/retriever = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		"oci://cookbook.example/retriever:1",
		"legacy-zone", "drivers",
		// FR-003: everything but the FR-013 cadence comes from the
		// configuration layers and cannot be edited at runtime.
		"Everything else comes from the configuration file",
		// FR-013: an instance with no destination and no scheduler says so
		// rather than showing an inert control with no explanation.
		"No destination configured",
		"This instance runs no reconciliation loop",
		"No synchronization has run yet.",
		`action="/recipes/sync"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/admin/retriever misses %q", want)
		}
	}

	// After a trigger, the last sync links to the task.
	tk, err := queue.Create(tasks.TypeSync, "oci://cookbook.example/retriever:1", "alexis", nil)
	if err != nil {
		t.Fatal(err)
	}
	w = get(t, mux, c, "/admin/retriever", nil)
	if !strings.Contains(w.Body.String(), `href="/tasks/`+tk.ID+`"`) {
		t.Error("/admin/retriever misses the last-sync task link")
	}

	// Operator and viewer: the admin gate answers the taxonomized 403.
	co := login(t, mux, "op", "pw-op")
	w = get(t, mux, co, "/admin/retriever", nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
		t.Errorf("operator /admin/retriever: code=%d, want 403 TBY-AUTH-003", w.Code)
	}
}

// TestSecurityBanners: the FR-033 relaxed-scope danger banner and the
// FR-047 anonymous-FileSet warning banner are permanent, on every page,
// naming the scopes and FileSets — never silent.
func TestSecurityBanners(t *testing.T) {
	u := newTestUIWithOptions(t, &Options{
		RelaxedTrustScopes: []string{"legacy-zone", "lab"},
		AnonymousFileSets:  []string{"drivers"},
	}, nil)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	for _, page := range []string{"/", "/content", "/recipes"} {
		body := get(t, mux, c, page, nil).Body.String()
		if !strings.Contains(body, "t-banner--danger") ||
			!strings.Contains(body, "legacy-zone, lab") {
			t.Errorf("%s misses the FR-033 relaxed-scope danger banner", page)
		}
		// The transport-error div is a permanent (hidden) warning banner, so
		// the assertion targets the banner sentence itself.
		if !strings.Contains(body, "FileSets served without authentication: drivers.") {
			t.Errorf("%s misses the FR-047 anonymous-FileSet warning banner", page)
		}
	}

	// Without any relaxation: no security banner.
	u2 := newTestUIWithOptions(t, &Options{}, nil)
	mux2 := mount(u2)
	c2 := login(t, mux2, "lecteur", "pw-view")
	body := get(t, mux2, c2, "/", nil).Body.String()
	if strings.Contains(body, "t-banner--danger") ||
		strings.Contains(body, "FileSets served without authentication") {
		t.Error("security banners present without any relaxed posture")
	}
}

// TestRecipesNavEntry: the shell carries a real /recipes entry for every
// role — the milestone-3 placeholder is gone, the media one stays under
// ShowUpcoming.
func TestRecipesNavEntry(t *testing.T) {
	u := newTestUIWithOptions(t, &Options{ShowUpcoming: true}, nil)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	body := get(t, mux, c, "/", nil).Body.String()
	if !strings.Contains(body, `href="/recipes"`) {
		t.Error("nav misses the real /recipes entry")
	}
	if strings.Contains(body, "Recipes — milestone") {
		t.Error("nav still carries the recipes placeholder")
	}
	if !strings.Contains(body, "Media — milestone 5") {
		t.Error("nav lost the media placeholder (ShowUpcoming)")
	}
}
