// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
)

// TestAccountScreen: /account opens for every authenticated role, viewer
// included; the page carries the profile card, the password form, and the
// hx-history exclusion (passwords transit here — ADR-0015 §5). The user
// menu links to it above the sign-out button.
func TestAccountScreen(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	w := adminGet(t, mux, c, "/account")
	if w.Code != http.StatusOK {
		t.Fatalf("viewer /account = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		"lecteur", "viewer", // profile card
		`action="/account/password"`, // password form
		`hx-history="false"`,         // secret screen excluded from history cache
		`href="/account"`,            // user-menu entry
	} {
		if !strings.Contains(body, want) {
			t.Errorf("account page misses %q", want)
		}
	}
	// The link sits in the user-menu pop-under, above the sign-out button.
	if link, out := strings.Index(body, `href="/account"`), strings.Index(body, ">Sign out<"); link < 0 || out < 0 || link > out {
		t.Error("account link is not above the sign-out button in the user menu")
	}
}

// TestAccountPasswordChange: the happy path — the new password signs in,
// the old one dies, the confirmation toast shows, and the OTHER sessions
// of the account are closed while the acting one survives (R-34).
func TestAccountPasswordChange(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	acting := login(t, mux, "lecteur", "pw-view")
	other := login(t, mux, "lecteur", "pw-view")

	w := adminPost(t, u, mux, acting, "/account/password", url.Values{
		"current": {"pw-view"}, "new": {"pw-next"}, "confirm": {"pw-next"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("password change = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "t-toast--success") {
		t.Error("password change misses the confirmation toast")
	}
	if _, ok := u.authn.Store.VerifyPassword("lecteur", "pw-next", t0); !ok {
		t.Error("new password does not verify after the change")
	}
	if _, ok := u.authn.Store.VerifyPassword("lecteur", "pw-view", t0); ok {
		t.Error("old password still verifies after the change")
	}

	// The acting session survives; the other one is signed out.
	if w := adminGet(t, mux, acting, "/account"); w.Code != http.StatusOK {
		t.Errorf("acting session died with the change: %d", w.Code)
	}
	if w := adminGet(t, mux, other, "/account"); w.Code != http.StatusSeeOther {
		t.Errorf("other session survived the change: %d", w.Code)
	}

	// The new password signs in (login fails the test otherwise).
	login(t, mux, "lecteur", "pw-next")
}

// TestAccountPasswordWrongCurrent: a wrong current password re-renders the
// page with the taxonomized TBY-AUTH-006 block and changes nothing.
func TestAccountPasswordWrongCurrent(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	w := adminPost(t, u, mux, c, "/account/password", url.Values{
		"current": {"nope"}, "new": {"pw-next"}, "confirm": {"pw-next"},
	})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "TBY-AUTH-006") {
		t.Errorf("wrong current = %d, want taxonomized 422; body misses TBY-AUTH-006", w.Code)
	}
	if !strings.Contains(w.Body.String(), `action="/account/password"`) {
		t.Error("failure does not re-render the form")
	}
	if _, ok := u.authn.Store.VerifyPassword("lecteur", "pw-view", t0); !ok {
		t.Error("password changed despite the refused current password")
	}
}

// TestAccountPasswordInvalidNew: empty, identical-to-current, and
// mismatched-confirmation submissions all answer TBY-AUTH-007.
func TestAccountPasswordInvalidNew(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	for name, form := range map[string]url.Values{
		"empty":     {"current": {"pw-view"}, "new": {""}, "confirm": {""}},
		"identical": {"current": {"pw-view"}, "new": {"pw-view"}, "confirm": {"pw-view"}},
		"mismatch":  {"current": {"pw-view"}, "new": {"pw-a"}, "confirm": {"pw-b"}},
	} {
		w := adminPost(t, u, mux, c, "/account/password", form)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "TBY-AUTH-007") {
			t.Errorf("%s new password = %d, want taxonomized 422 TBY-AUTH-007", name, w.Code)
		}
	}
	if _, ok := u.authn.Store.VerifyPassword("lecteur", "pw-view", t0); !ok {
		t.Error("password changed despite the rejected submissions")
	}
}

// TestAccountPasswordRequiresCSRF: the mutation without the session's CSRF
// token is refused (NFR-012) and changes nothing.
func TestAccountPasswordRequiresCSRF(t *testing.T) {
	u := newTestUI(t, false)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	r := httptest.NewRequest(http.MethodPost, "/account/password",
		strings.NewReader("current=pw-view&new=pw-next&confirm=pw-next"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-004") {
		t.Errorf("change without csrf = %d, want taxonomized 403", w.Code)
	}
	if _, ok := u.authn.Store.VerifyPassword("lecteur", "pw-view", t0); !ok {
		t.Error("password changed despite the CSRF refusal")
	}
}

// TestAccountPasswordAudited: success AND failure emit the FR-094 record —
// six-field schema, actor and target are the session identity, origin is
// the real peer address.
func TestAccountPasswordAudited(t *testing.T) {
	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("lecteur", auth.RoleViewer, "pw-view", t0); err != nil {
		t.Fatal(err)
	}
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(12 * time.Hour),
		Logger:   slog.New(slog.DiscardHandler),
	}
	var buf bytes.Buffer
	u := New(authn, slog.New(slog.NewJSONHandler(&buf, nil)),
		&Options{Version: "0.3.0-test", Mode: "mirror"})
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	adminPost(t, u, mux, c, "/account/password", url.Values{
		"current": {"nope"}, "new": {"pw-next"}, "confirm": {"pw-next"},
	})
	adminPost(t, u, mux, c, "/account/password", url.Values{
		"current": {"pw-view"}, "new": {"pw-next"}, "confirm": {"pw-next"},
	})

	var outcomes []string
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("not JSON Lines: %v", err)
		}
		if rec["log_type"] != "audit" || rec["action"] != "account.password_change" {
			continue
		}
		if rec["actor"] != "lecteur" || rec["target"] != "lecteur" || rec["origin"] != "192.0.2.1" {
			t.Errorf("audit record off schema: %v", rec)
		}
		outcomes = append(outcomes, rec["outcome"].(string))
	}
	if len(outcomes) != 2 || outcomes[0] != "failure" || outcomes[1] != "success" {
		t.Errorf("audit outcomes = %v, want [failure success]", outcomes)
	}
}

// TestAccountUIAPIParity is the R-06/FR-061 invariant on the password
// surface: the same request yields the same taxonomy code and status on
// the /account screen and on POST /api/v1/account/password.
func TestAccountUIAPIParity(t *testing.T) {
	u := newTestUI(t, false)
	uiMux := mount(u)
	c := login(t, uiMux, "lecteur", "pw-view")

	restAPI := api.New(u.authn, slog.New(slog.DiscardHandler))
	api.RegisterAccounts(restAPI, u.authn.Store)
	apiMux := http.NewServeMux()
	apiMux.Handle("/api/v1/", restAPI.Handler())

	apiChange := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/account/password", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.SetBasicAuth("lecteur", "pw-view")
		w := httptest.NewRecorder()
		apiMux.ServeHTTP(w, r)
		return w
	}

	// Wrong current password: same code, same 422, both surfaces.
	wUI := adminPost(t, u, uiMux, c, "/account/password", url.Values{
		"current": {"nope"}, "new": {"pw-a"}, "confirm": {"pw-a"},
	})
	wAPI := apiChange(`{"current":"nope","new":"pw-a"}`)
	if wUI.Code != http.StatusUnprocessableEntity || wAPI.Code != http.StatusUnprocessableEntity {
		t.Errorf("wrong current: ui=%d api=%d, want 422 on both", wUI.Code, wAPI.Code)
	}
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(wAPI.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "TBY-AUTH-006" || !strings.Contains(wUI.Body.String(), "TBY-AUTH-006") {
		t.Errorf("wrong-current codes diverge: api=%q", problem.Code)
	}

	// Invalid new password: same code on both surfaces.
	wUI = adminPost(t, u, uiMux, c, "/account/password", url.Values{
		"current": {"pw-view"}, "new": {"pw-view"}, "confirm": {"pw-view"},
	})
	wAPI = apiChange(`{"current":"pw-view","new":"pw-view"}`)
	if !strings.Contains(wUI.Body.String(), "TBY-AUTH-007") || !strings.Contains(wAPI.Body.String(), "TBY-AUTH-007") {
		t.Errorf("invalid-new codes diverge: ui=%d api=%d", wUI.Code, wAPI.Code)
	}

	// Success on the API mirror with the same rules as the screen.
	if w := apiChange(`{"current":"pw-view","new":"pw-b"}`); w.Code != http.StatusNoContent {
		t.Fatalf("api change = %d: %s", w.Code, w.Body.String())
	}
	if _, ok := u.authn.Store.VerifyPassword("lecteur", "pw-b", t0); !ok {
		t.Error("api-changed password does not verify")
	}
}
