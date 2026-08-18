// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

var t0 = time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)

// TestCatalogsComplete diffs the EN and FR label catalogs: a key present in
// one and missing in the other fails the build (FR-063, crucible gate).
func TestCatalogsComplete(t *testing.T) {
	en, err := messageIDs("en")
	if err != nil {
		t.Fatal(err)
	}
	fr, err := messageIDs("fr")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(en)
	sort.Strings(fr)
	set := func(ids []string) map[string]bool {
		m := map[string]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	enSet, frSet := set(en), set(fr)
	for _, id := range en {
		if !frSet[id] {
			t.Errorf("key %q missing from fr.yaml", id)
		}
	}
	for _, id := range fr {
		if !enSet[id] {
			t.Errorf("key %q missing from en.yaml", id)
		}
	}
}

// TestTemplateKeysExist walks every template for {{.T "key"}} / {{.TN "key"}}
// calls and checks each key exists in the English catalog: no visible
// string escapes the catalogs (ADR-0015 §7).
func TestTemplateKeysExist(t *testing.T) {
	en, err := messageIDs("en")
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, id := range en {
		known[id] = true
	}
	call := regexp.MustCompile(`\.TN?\s+"([^"]+)"`)
	err = fs.WalkDir(templatesFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := templatesFS.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range call.FindAllStringSubmatch(string(raw), -1) {
			if !known[m[1]] {
				t.Errorf("%s: key %q missing from the catalogs", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// newTestUI wires a full UI over a real account store and a real, empty
// OCI store (the content screens need one).
func newTestUI(t *testing.T, disabled bool) *UI {
	t.Helper()
	return newTestUIWithStore(t, disabled, openTestStore(t))
}

// openTestStore opens a fresh embedded store in a temporary directory.
func openTestStore(t *testing.T) *store.Store {
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
	return st
}

// fixtureAccounts holds the accounts file every UI test starts from,
// built exactly once.
//
// argon2id is deliberately expensive — that is its job — and this package
// runs ~96 tests, each of which used to hash two passwords, doubled again
// by the anti-flaky -count=2. On the slower CI architecture that alone
// pushed the package past the ten-minute test timeout. The cost belongs
// in the product, not in repeating the same two hashes four hundred
// times: they are computed once and the resulting file is copied per
// test, so every test still gets its own mutable store.
var fixtureAccounts = sync.OnceValues(func() ([]byte, error) {
	dir, err := os.MkdirTemp("", "tobby-ui-accounts")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	acc, err := auth.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := acc.AddAccount("alexis", auth.RoleAdmin, "pw-admin", t0); err != nil {
		return nil, err
	}
	if err := acc.AddAccount("lecteur", auth.RoleViewer, "pw-view", t0); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(dir, "accounts.yaml")) //nolint:gosec // G304: dir comes from os.MkdirTemp just above, not from input
})

// testAccounts returns a private account store seeded with the fixture.
func testAccounts(t *testing.T) *auth.Store {
	t.Helper()
	raw, err := fixtureAccounts()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "accounts.yaml"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	acc, err := auth.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

// newTestUIWithStore wires a full UI over a real account store and the
// given OCI store.
func newTestUIWithStore(t *testing.T, disabled bool, st *store.Store) *UI {
	t.Helper()
	accounts := testAccounts(t)
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(12 * time.Hour),
		Disabled: disabled,
		Logger:   slog.New(slog.DiscardHandler),
	}
	return New(authn, slog.New(slog.DiscardHandler),
		&Options{Version: "0.2.0-test", Mode: "mirror", Store: st})
}

func mount(u *UI) *http.ServeMux {
	mux := http.NewServeMux()
	u.Mount(mux)
	return mux
}

// login signs in and returns the session cookie.
func login(t *testing.T, mux *http.ServeMux, user, pass string) *http.Cookie {
	t.Helper()
	form := "username=" + user + "&password=" + pass + "&next=%2F"
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login = %d, want 303", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			if !c.HttpOnly {
				t.Error("session cookie must be HttpOnly (NFR-015)")
			}
			return c
		}
	}
	t.Fatal("no session cookie set")
	return nil
}

// TestUINeverExposedOpen is the R-01 acceptance on the UI surface: every
// application route redirects an anonymous browser to /login, and htmx
// requests get 401 + HX-Redirect instead of a fragment (ADR-0015 §6).
func TestUINeverExposedOpen(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)

	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/login?next=") {
		t.Errorf("anonymous / : code=%d location=%q", w.Code, w.Header().Get("Location"))
	}

	r = httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	r.Header.Set("HX-Request", "true")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || !strings.HasPrefix(w.Header().Get("HX-Redirect"), "/login") {
		t.Errorf("anonymous htmx: code=%d hx-redirect=%q", w.Code, w.Header().Get("HX-Redirect"))
	}
}

func TestLoginFlow(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)

	// The login page renders pre-auth, in French on demand (FR-063).
	r := httptest.NewRequest(http.MethodGet, "/login", http.NoBody)
	r.Header.Set("Accept-Language", "fr-FR,fr;q=0.9")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	// html/template escapes the apostrophe in text nodes.
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Connexion à l&#39;instance") {
		t.Errorf("login page fr: code=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `lang="fr"`) {
		t.Error("html lang attribute does not follow the negotiated language")
	}

	// Wrong password: 401, generic taxonomized block, no account probing.
	form := "username=alexis&password=wrong&next=%2F"
	r = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "TBY-AUTH-002") {
		t.Errorf("failed login: code=%d body misses TBY-AUTH-002", w.Code)
	}

	// Successful login reaches the dashboard with the shell.
	c := login(t, mux, "alexis", "pw-admin")
	r = httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	r.AddCookie(c)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("dashboard = %d", w.Code)
	}
	for _, want := range []string{"Dashboard", "mirror", "alexis", "Administration"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard misses %q", want)
		}
	}

	// A viewer sees neither Import nor Administration in the nav, and the
	// empty state explains the role instead of offering a dead CTA.
	cv := login(t, mux, "lecteur", "pw-view")
	r = httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	r.AddCookie(cv)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	vbody := w.Body.String()
	if strings.Contains(vbody, `href="/import"`) || strings.Contains(vbody, `href="/admin/accounts"`) {
		t.Error("viewer nav leaks operator/admin entries")
	}
	if !strings.Contains(vbody, "operator role") && !strings.Contains(vbody, "rôle operator") {
		t.Error("viewer empty state misses the role explanation")
	}
}

func TestLogoutRequiresCSRF(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	// Without the CSRF token the mutation is refused (NFR-012).
	r := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(""))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-004") {
		t.Errorf("logout without csrf: code=%d", w.Code)
	}

	// With it, the session closes and the cookie clears.
	sess, _ := u.authn.Sessions.Get(c.Value, t0)
	r = httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader("csrf="+sess.CSRF))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(c)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Errorf("logout = %d, want 303", w.Code)
	}
	if _, ok := u.authn.Sessions.Get(c.Value, t0); ok {
		t.Error("session survived logout")
	}
}

// TestAuthDisabledBanner checks the FR-075 override: everything opens as
// the explicit anonymous identity, and the permanent danger banner is on
// every page.
func TestAuthDisabledBanner(t *testing.T) {
	u := newTestUI(t, true)
	mux := mount(u)

	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("dashboard under override = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "t-banner--danger") {
		t.Error("missing permanent FR-075 banner")
	}
	// /login has nothing to sign into: redirect home.
	r = httptest.NewRequest(http.MethodGet, "/login", http.NoBody)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusFound {
		t.Errorf("/login under override = %d, want 302", w.Code)
	}
}

// TestErrorPages: unknown routes render the taxonomized 404 in the shell;
// fragments get the inline block with the real status.
func TestErrorPages(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	r := httptest.NewRequest(http.MethodGet, "/nowhere", http.NoBody)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "TBY-SRV-002") {
		t.Errorf("404 page: code=%d", w.Code)
	}

	r = httptest.NewRequest(http.MethodGet, "/nowhere", http.NoBody)
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Target", "zone")
	r.AddCookie(c)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `role="alert"`) {
		t.Errorf("404 fragment: code=%d body=%q", w.Code, w.Body.String())
	}
}

// TestThemeAndLangCookies: the switchers set cookies and reload; the theme
// is stamped server-side on <html> (zero FOUC).
// TestLayoutShellRegressions pins the v0.2.0 shell fixes: the inline
// script wires its document-level listeners once per browser page (B-001),
// bold marks the CURRENT language (B-003), and the /lang and /theme forms
// force a full page load — their state lives on <html>, which a boosted
// swap never touches (B-004).
func TestLayoutShellRegressions(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := get(t, mux, c, "/", nil)
	body := w.Body.String()

	if !strings.Contains(body, "window.__tobbyWired") {
		t.Error("layout script misses the idempotence guard (B-001)")
	}
	if strings.Count(body, `hx-boost="false"`) < 2 {
		t.Error("/lang and /theme forms must opt out of hx-boost (B-004)")
	}
	if !strings.Contains(body, "<b>EN</b> · FR") {
		t.Error("bold must mark the current language, EN by default (B-003)")
	}

	// French negotiated: bold moves to FR.
	w = get(t, mux, c, "/", map[string]string{"Accept-Language": "fr"})
	if !strings.Contains(w.Body.String(), "EN · <b>FR</b>") {
		t.Error("bold must mark the current language, FR when negotiated (B-003)")
	}
}

func TestThemeAndLangCookies(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)

	r := httptest.NewRequest(http.MethodPost, "/theme", strings.NewReader("theme=light&back=/login"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var themeC *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == themeCookie {
			themeC = c
		}
	}
	if themeC == nil || themeC.Value != "light" {
		t.Fatal("theme cookie not set")
	}

	r = httptest.NewRequest(http.MethodGet, "/login", http.NoBody)
	r.AddCookie(themeC)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `data-theme="light"`) {
		t.Error("server does not stamp data-theme from the cookie")
	}
}
