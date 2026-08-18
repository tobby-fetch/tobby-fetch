// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
)

// adminGet performs a session-authenticated GET.
func adminGet(t *testing.T, mux *http.ServeMux, c *http.Cookie, target string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// adminPost performs a session-authenticated form POST with the CSRF token.
func adminPost(t *testing.T, u *UI, mux *http.ServeMux, c *http.Cookie, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	sess, ok := u.authn.Sessions.Get(c.Value, t0)
	if !ok {
		t.Fatal("no session for cookie")
	}
	form.Set("csrf", sess.CSRF)
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestAdminAccountsGating: the screen is admin-only — operator and viewer
// get the taxonomized 403 naming the required role (UI-SPEC §5.9).
func TestAdminAccountsGating(t *testing.T) {
	u := newTestUI(t, false)
	if err := u.authn.Store.AddAccount("op", auth.RoleOperator, "pw-op", t0); err != nil {
		t.Fatal(err)
	}
	mux := mount(u)

	for user, pass := range map[string]string{"op": "pw-op", "lecteur": "pw-view"} {
		c := login(t, mux, user, pass)
		w := adminGet(t, mux, c, "/admin/accounts")
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
			t.Errorf("%s on /admin/accounts = %d, want taxonomized 403", user, w.Code)
		}
		if !strings.Contains(w.Body.String(), "admin") {
			t.Errorf("%s refusal does not name the required role", user)
		}
	}
}

// TestAdminAccountsScreen: accounts table, CLI note, empty token state,
// and the hx-history exclusion of the page (ADR-0015 §5).
func TestAdminAccountsScreen(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := adminGet(t, mux, c, "/admin/accounts")
	if w.Code != http.StatusOK {
		t.Fatalf("admin/accounts = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		"alexis", "lecteur", // accounts table
		"tobby user",             // password management is CLI-only at J2
		`hx-history="false"`,     // secret screen excluded from history cache
		"No API token yet.",      // empty token state
		"/admin/accounts/tokens", // creation form
	} {
		if !strings.Contains(body, want) {
			t.Errorf("admin page misses %q", want)
		}
	}

	// Other pages keep the history cache attribute off.
	w = adminGet(t, mux, c, "/")
	if strings.Contains(w.Body.String(), "hx-history") {
		t.Error("dashboard carries hx-history, expected on the admin page only")
	}
}

var secretRe = regexp.MustCompile(`tby_[A-Za-z0-9_-]+`)

// TestTokenCreateSecretShownOnce: creation renders the secret exactly
// once; the next GET never re-displays it (FR-072).
func TestTokenCreateSecretShownOnce(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := adminPost(t, u, mux, c, "/admin/accounts/tokens",
		url.Values{"name": {"ci-import"}, "role": {"operator"}})
	if w.Code != http.StatusOK {
		t.Fatalf("token create = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	secret := secretRe.FindString(body)
	if secret == "" {
		t.Fatal("creation response does not display the secret")
	}
	if !strings.Contains(body, "never be shown again") {
		t.Error("secret block misses the one-time warning")
	}
	if !strings.Contains(body, `data-copy="`+secret+`"`) {
		t.Error("secret block misses the copy chip")
	}
	// The secret authenticates with the requested role.
	if tok, ok := u.authn.Store.VerifyToken(secret); !ok || tok.Role != auth.RoleOperator {
		t.Errorf("created secret does not verify as operator: %+v ok=%v", tok, ok)
	}

	// The next GET lists the token but never the secret.
	w = adminGet(t, mux, c, "/admin/accounts")
	if got := secretRe.FindString(w.Body.String()); got != "" {
		t.Errorf("secret re-displayed on a later GET: %q", got)
	}
	if !strings.Contains(w.Body.String(), "ci-import") {
		t.Error("token listing misses the created token")
	}

	// A duplicate active name re-renders the form with the message.
	w = adminPost(t, u, mux, c, "/admin/accounts/tokens",
		url.Values{"name": {"ci-import"}, "role": {"viewer"}})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already carries this name") {
		t.Errorf("duplicate name = %d, want 409 with the inline message", w.Code)
	}
}

// TestTokenRevoke: the dialog-confirmed POST revokes immediately, shows a
// toast, and the token dies (FR-072).
func TestTokenRevoke(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := adminPost(t, u, mux, c, "/admin/accounts/tokens",
		url.Values{"name": {"old-ci"}, "role": {"viewer"}})
	secret := secretRe.FindString(w.Body.String())
	if secret == "" {
		t.Fatal("no secret in creation response")
	}
	// The revocation dialog is a native <dialog>, never confirm().
	if !strings.Contains(w.Body.String(), "<dialog") {
		t.Error("revocation confirmation is not the native <dialog> component")
	}

	w = adminPost(t, u, mux, c, "/admin/accounts/tokens/revoke", url.Values{"name": {"old-ci"}})
	if w.Code != http.StatusOK {
		t.Fatalf("revoke = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "t-toast--success") {
		t.Error("revocation misses the confirmation toast")
	}
	if !strings.Contains(w.Body.String(), "revoked") {
		t.Error("revoked token misses its status badge")
	}
	if _, ok := u.authn.Store.VerifyToken(secret); ok {
		t.Error("secret still verifies after revocation")
	}

	// Unknown token: taxonomized 404.
	w = adminPost(t, u, mux, c, "/admin/accounts/tokens/revoke", url.Values{"name": {"nope"}})
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "TBY-SRV-002") {
		t.Errorf("unknown revoke = %d, want taxonomized 404", w.Code)
	}
}

// TestTokenMutationsRequireCSRF: the creation POST without the session's
// CSRF token is refused (NFR-012).
func TestTokenMutationsRequireCSRF(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	r := httptest.NewRequest(http.MethodPost, "/admin/accounts/tokens",
		strings.NewReader("name=x&role=viewer"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-004") {
		t.Errorf("create without csrf = %d, want taxonomized 403", w.Code)
	}
	if len(u.authn.Store.Tokens()) != 0 {
		t.Error("token created despite the CSRF refusal")
	}
}

// TestAdminAccountCreate: an administrator provisions a second account
// from the screen (FR-073) — no CLI round trip — and the new account signs
// in immediately with the role it was given. The tool computed the hash:
// the form never carried one (FR-066).
func TestAdminAccountCreate(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := adminPost(t, u, mux, c, "/admin/accounts", url.Values{
		"name": {"claire"}, "role": {"operator"},
		"password": {"pw-claire"}, "confirm": {"pw-claire"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("account create = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "t-toast--success") {
		t.Error("account creation misses the confirmation toast")
	}
	if !strings.Contains(w.Body.String(), "claire") {
		t.Error("the created account is not in the re-rendered table")
	}
	acct, ok := u.authn.Store.Account("claire")
	if !ok || acct.Role != auth.RoleOperator {
		t.Fatalf("created account = %+v ok=%v", acct, ok)
	}
	if strings.Contains(acct.Hash, "pw-claire") || !strings.HasPrefix(acct.Hash, "$argon2id$") {
		t.Errorf("password not hashed by the tool: %q", acct.Hash)
	}
	// The account works end to end: it signs in with its own role.
	newCookie := login(t, mux, "claire", "pw-claire")
	if w := adminGet(t, mux, newCookie, "/import"); w.Code != http.StatusOK {
		t.Errorf("the new operator cannot reach /import: %d", w.Code)
	}
	if w := adminGet(t, mux, newCookie, "/admin/accounts"); w.Code != http.StatusForbidden {
		t.Errorf("the new operator reaches the admin screen: %d", w.Code)
	}
}

// TestAdminAccountCreateRejected: the validation and duplicate paths
// answer their taxonomy codes inline and change nothing.
func TestAdminAccountCreateRejected(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	for name, form := range map[string]url.Values{
		"empty name":     {"name": {""}, "role": {"viewer"}, "password": {"pw"}, "confirm": {"pw"}},
		"unknown role":   {"name": {"x"}, "role": {"root"}, "password": {"pw"}, "confirm": {"pw"}},
		"empty password": {"name": {"x"}, "role": {"viewer"}, "password": {""}, "confirm": {""}},
		"mismatch":       {"name": {"x"}, "role": {"viewer"}, "password": {"pw-a"}, "confirm": {"pw-b"}},
	} {
		w := adminPost(t, u, mux, c, "/admin/accounts", form)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "TBY-AUTH-008") {
			t.Errorf("%s = %d, want taxonomized 422 TBY-AUTH-008", name, w.Code)
		}
	}
	if len(u.authn.Store.Accounts()) != 2 {
		t.Error("a rejected submission created an account")
	}

	w := adminPost(t, u, mux, c, "/admin/accounts", url.Values{
		"name": {"lecteur"}, "role": {"viewer"}, "password": {"pw"}, "confirm": {"pw"},
	})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "TBY-AUTH-009") {
		t.Errorf("duplicate login = %d, want taxonomized 409 TBY-AUTH-009", w.Code)
	}
}

// TestAdminAccountRoleChange: the role changes (FR-074) and every session
// of the account is closed — a session carries the role it was opened with.
func TestAdminAccountRoleChange(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")
	victim := login(t, mux, "lecteur", "pw-view")

	w := adminPost(t, u, mux, c, "/admin/accounts/role",
		url.Values{"name": {"lecteur"}, "role": {"operator"}})
	if w.Code != http.StatusOK {
		t.Fatalf("role change = %d: %s", w.Code, w.Body.String())
	}
	if acct, _ := u.authn.Store.Account("lecteur"); acct.Role != auth.RoleOperator {
		t.Errorf("role = %s, want operator", acct.Role)
	}
	if w := adminGet(t, mux, victim, "/account"); w.Code != http.StatusSeeOther {
		t.Errorf("the promoted account kept its old session: %d", w.Code)
	}
	// Signing in again carries the new role.
	fresh := login(t, mux, "lecteur", "pw-view")
	if w := adminGet(t, mux, fresh, "/import"); w.Code != http.StatusOK {
		t.Errorf("the promoted operator cannot reach /import: %d", w.Code)
	}

	// Unknown account and unknown role.
	w = adminPost(t, u, mux, c, "/admin/accounts/role",
		url.Values{"name": {"ghost"}, "role": {"viewer"}})
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "TBY-AUTH-010") {
		t.Errorf("role change on unknown = %d, want taxonomized 404 TBY-AUTH-010", w.Code)
	}
	w = adminPost(t, u, mux, c, "/admin/accounts/role",
		url.Values{"name": {"lecteur"}, "role": {"root"}})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "TBY-AUTH-008") {
		t.Errorf("unknown role = %d, want taxonomized 422 TBY-AUTH-008", w.Code)
	}
}

// TestAdminAccountDelete: removal (FR-073) closes the account's sessions
// immediately and kills its credentials.
func TestAdminAccountDelete(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")
	victim := login(t, mux, "lecteur", "pw-view")

	w := adminPost(t, u, mux, c, "/admin/accounts/delete", url.Values{"name": {"lecteur"}})
	if w.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "t-toast--success") {
		t.Error("deletion misses the confirmation toast")
	}
	if _, ok := u.authn.Store.Account("lecteur"); ok {
		t.Error("account still present after deletion")
	}
	if w := adminGet(t, mux, victim, "/account"); w.Code != http.StatusSeeOther {
		t.Errorf("the deleted account kept browsing on its session: %d", w.Code)
	}
	if _, ok := u.authn.Store.VerifyPassword("lecteur", "pw-view", t0); ok {
		t.Error("the deleted account still authenticates")
	}

	w = adminPost(t, u, mux, c, "/admin/accounts/delete", url.Values{"name": {"ghost"}})
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "TBY-AUTH-010") {
		t.Errorf("delete unknown = %d, want taxonomized 404 TBY-AUTH-010", w.Code)
	}
}

// TestAdminLastAdminRefused is the FR-005 safety net on the screen: the
// only administrator can neither delete itself nor demote itself, and the
// refusal explains how to get through.
func TestAdminLastAdminRefused(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := adminPost(t, u, mux, c, "/admin/accounts/delete", url.Values{"name": {"alexis"}})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "TBY-AUTH-011") {
		t.Errorf("self-delete = %d, want taxonomized 409 TBY-AUTH-011", w.Code)
	}
	w = adminPost(t, u, mux, c, "/admin/accounts/role",
		url.Values{"name": {"alexis"}, "role": {"operator"}})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "TBY-AUTH-011") {
		t.Errorf("self-demotion = %d, want taxonomized 409 TBY-AUTH-011", w.Code)
	}
	// The instance stays administrable, and the acting session survives.
	if acct, _ := u.authn.Store.Account("alexis"); acct.Role != auth.RoleAdmin {
		t.Fatalf("the only admin lost its role: %s", acct.Role)
	}
	if w := adminGet(t, mux, c, "/admin/accounts"); w.Code != http.StatusOK {
		t.Errorf("the refused operations closed the acting session: %d", w.Code)
	}

	// With a second administrator the same operations go through.
	if w := adminPost(t, u, mux, c, "/admin/accounts", url.Values{
		"name": {"claire"}, "role": {"admin"}, "password": {"pw-c"}, "confirm": {"pw-c"},
	}); w.Code != http.StatusOK {
		t.Fatalf("creating the second admin = %d", w.Code)
	}
	if w := adminPost(t, u, mux, c, "/admin/accounts/role",
		url.Values{"name": {"alexis"}, "role": {"operator"}}); w.Code != http.StatusOK {
		t.Errorf("self-demotion with a second admin = %d, want 200", w.Code)
	}
}

// TestAdminAccountAudited: every account-lifecycle action emits the FR-094
// record — the acting administrator as actor, the managed account as
// target, and the last-admin barrier recorded as a refusal, not a failure.
func TestAdminAccountAudited(t *testing.T) {
	var buf bytes.Buffer
	u := newTestUIWithOptions(t, &Options{Store: openTestStore(t)}, &buf)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	adminPost(t, u, mux, c, "/admin/accounts", url.Values{
		"name": {"claire"}, "role": {"viewer"}, "password": {"pw-c"}, "confirm": {"pw-c"},
	})
	adminPost(t, u, mux, c, "/admin/accounts/role", url.Values{"name": {"claire"}, "role": {"operator"}})
	adminPost(t, u, mux, c, "/admin/accounts/delete", url.Values{"name": {"claire"}})
	adminPost(t, u, mux, c, "/admin/accounts/delete", url.Values{"name": {"alexis"}})

	type record struct{ action, target, outcome string }
	var got []record
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("audit log is not JSON Lines: %v", err)
		}
		if rec["log_type"] != "audit" || !strings.HasPrefix(rec["action"].(string), "account.") {
			continue
		}
		for _, field := range []string{"actor", "action", "target", "outcome", "origin", "time"} {
			if _, ok := rec[field]; !ok {
				t.Errorf("audit record misses the %q field of the FR-094 schema: %v", field, rec)
			}
		}
		if rec["actor"] != "alexis" || rec["origin"] != "192.0.2.1" {
			t.Errorf("record does not carry the acting identity and real origin: %v", rec)
		}
		got = append(got, record{rec["action"].(string), rec["target"].(string), rec["outcome"].(string)})
	}
	want := []record{
		{"account.create", "claire", "success"},
		{"account.role_change", "claire", "success"},
		{"account.delete", "claire", "success"},
		{"account.delete", "alexis", "denied"},
	}
	if len(got) != len(want) {
		t.Fatalf("account audit records = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestAdminAccountsScreenControls: the screen carries the FR-073/FR-074
// controls — creation form with a password field and no hash field, a role
// select per row, and a native <dialog> per removal.
func TestAdminAccountsScreenControls(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	body := adminGet(t, mux, c, "/admin/accounts").Body.String()
	for _, want := range []string{
		`action="/admin/accounts"`,        // creation form
		`type="password" name="password"`, // the tool hashes it (FR-066)
		`action="/admin/accounts/role"`,   // role change
		`action="/admin/accounts/delete"`, // removal
		`id="delete-dlg-0"`,               // native dialog, never confirm()
	} {
		if !strings.Contains(body, want) {
			t.Errorf("admin screen misses %q", want)
		}
	}
	if strings.Contains(body, `name="hash"`) {
		t.Error("the screen exposes a password-hash field (FR-066 forbids it)")
	}
	// The acting administrator's own row is marked, so an operator never
	// mistakes it for someone else's.
	if !strings.Contains(body, `alexis <span class="t-badge">you</span>`) {
		t.Error("the acting administrator's row is not marked")
	}
}
