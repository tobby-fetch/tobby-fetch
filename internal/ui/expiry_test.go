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

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
)

// clockedUI is a test UI whose clock the test moves, with the instance's
// idle timeout and an audit capture.
func clockedUI(t *testing.T, idle time.Duration) (*UI, *http.ServeMux, *time.Time, *bytes.Buffer) {
	t.Helper()
	u := newTestUI(t, false)
	now := t0
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	u.Now = func() time.Time { return now }
	u.logger = logger
	u.authn.Logger = logger
	u.authn.Sessions.SetIdleTimeout(idle)
	u.authn.Sessions.OnExpire(func(sess *auth.Session, reason auth.ExpiryReason, origin string) {
		logger.Info("session.expire", "log_type", "audit", "actor", sess.Account,
			"action", "session.expire", "target", string(reason), "outcome", "success", "origin", origin)
	})
	return u, mount(u), &now, &buf
}

func auditLines(raw []byte, action string) []map[string]any {
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var rec map[string]any
		if json.Unmarshal(line, &rec) == nil && rec["log_type"] == "audit" && rec["action"] == action {
			out = append(out, rec)
		}
	}
	return out
}

// TestIdleSessionExpires is the R-14 UI acceptance: a session left idle
// past auth.session.idleTimeout is refused, the browser lands on the
// sign-in page with TBY-AUTH-005, and the audit log records an idle
// expiry; a session kept active survives the same delay.
func TestIdleSessionExpires(t *testing.T) {
	u, mux, now, buf := clockedUI(t, 30*time.Minute)
	idle := login(t, mux, "alexis", "pw-admin")
	active := login(t, mux, "lecteur", "pw-view")

	for range 4 { // 4 × 20 min of activity on one session only
		*now = now.Add(20 * time.Minute)
		if w := adminGet(t, mux, active, "/"); w.Code != http.StatusOK {
			t.Fatalf("active session refused at %s: %d", now.Sub(t0), w.Code)
		}
	}

	w := adminGet(t, mux, idle, "/tasks")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("idle session: %d, want 303 to the sign-in page", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/login?next=%2Ftasks") || !strings.Contains(loc, "expired=1") {
		t.Errorf("idle session redirected to %q, want /login?next=/tasks with the expired marker", loc)
	}
	// The dead cookie is cleared rather than left to be presented again.
	var cleared bool
	for _, c := range w.Result().Cookies() {
		cleared = cleared || (c.Name == auth.SessionCookie && c.MaxAge < 0)
	}
	if !cleared {
		t.Error("the expired session cookie was not cleared")
	}

	r := httptest.NewRequest(http.MethodGet, loc, http.NoBody)
	page := httptest.NewRecorder()
	mux.ServeHTTP(page, r)
	if !strings.Contains(page.Body.String(), "TBY-AUTH-005") {
		t.Error("the sign-in page after an idle expiry does not show TBY-AUTH-005")
	}
	if !strings.Contains(page.Body.String(), `name="next" value="/tasks"`) {
		t.Error("the destination is not preserved across the expiry")
	}

	recs := auditLines(buf.Bytes(), "session.expire")
	if len(recs) != 1 || recs[0]["actor"] != "alexis" || recs[0]["target"] != "idle" {
		t.Errorf("session.expire records = %v, want one idle expiry of alexis", recs)
	}
	if w := adminGet(t, mux, active, "/"); w.Code != http.StatusOK {
		t.Errorf("the active session did not survive: %d", w.Code)
	}

	// htmx: 401 + HX-Redirect carrying the same marker (ADR-0015 §6).
	*now = now.Add(time.Hour)
	r = httptest.NewRequest(http.MethodGet, "/tasks/badge", http.NoBody)
	r.Header.Set("HX-Request", "true")
	r.AddCookie(active)
	hw := httptest.NewRecorder()
	mux.ServeHTTP(hw, r)
	if hw.Code != http.StatusUnauthorized || !strings.Contains(hw.Header().Get("HX-Redirect"), "expired=1") {
		t.Errorf("htmx on an expired session: %d %q", hw.Code, hw.Header().Get("HX-Redirect"))
	}
	_ = u
}

// TestLoginPageWithoutExpiryMarker: a plain visit shows no error block.
func TestLoginPageWithoutExpiryMarker(t *testing.T) {
	_, mux, _, _ := clockedUI(t, time.Minute)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/login?next=%2F", http.NoBody))
	if strings.Contains(w.Body.String(), "TBY-AUTH-005") {
		t.Error("a plain sign-in page shows TBY-AUTH-005")
	}
}

// TestTokenLifetimeForm covers the R-14 token form: an explicit lifetime
// in days, the default lifetime when the field is empty, the expiry and
// the expired status in the listing, and the refusal of a nonsensical
// lifetime.
func TestTokenLifetimeForm(t *testing.T) {
	u, mux, now, _ := clockedUI(t, time.Hour)
	c := login(t, mux, "alexis", "pw-admin")
	post := func(form url.Values) *httptest.ResponseRecorder {
		sess, ok := u.authn.Sessions.Get(c.Value, *now)
		if !ok {
			t.Fatal("no session")
		}
		form.Set("csrf", sess.CSRF)
		r := httptest.NewRequest(http.MethodPost, "/admin/accounts/tokens", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(c)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	if w := post(url.Values{"name": {"week"}, "role": {"viewer"}, "lifetimeDays": {"7"}}); w.Code != http.StatusOK {
		t.Fatalf("create with lifetime: %d", w.Code)
	}
	if w := post(url.Values{"name": {"bad"}, "role": {"viewer"}, "lifetimeDays": {"0"}}); w.Code != http.StatusBadRequest {
		t.Errorf("lifetime 0: %d, want 400", w.Code)
	}
	u.authn.Store.SetDefaultTokenLifetime(48 * time.Hour)
	if w := post(url.Values{"name": {"dflt"}, "role": {"viewer"}}); w.Code != http.StatusOK {
		t.Fatalf("create with the default: %d", w.Code)
	}
	byName := map[string]auth.Token{}
	for _, tok := range u.authn.Store.Tokens() {
		byName[tok.Name] = tok
	}
	if got := byName["week"].Expires; !got.Equal(t0.Add(7 * 24 * time.Hour)) {
		t.Errorf("week token expiry = %v", got)
	}
	if got := byName["dflt"].Expires; !got.Equal(t0.Add(48 * time.Hour)) {
		t.Errorf("default-lifetime token expiry = %v", got)
	}
	if _, ok := byName["bad"]; ok {
		t.Error("a token with lifetime 0 was created")
	}

	w := adminGet(t, mux, c, "/admin/accounts")
	body := w.Body.String()
	if !strings.Contains(body, "data-token-expires") || !strings.Contains(body, "2 d") {
		t.Errorf("listing misses the expiry column or the default-lifetime hint")
	}
	*now = t0.Add(3 * 24 * time.Hour)
	c = login(t, mux, "alexis", "pw-admin")
	body = adminGet(t, mux, c, "/admin/accounts").Body.String()
	if !strings.Contains(body, ">expired<") {
		t.Error("an expired token is not marked expired in the listing")
	}
}

// TestPollingDoesNotKeepTheSessionAlive: a tab that only polls expires on
// time, and the poll that finds it dead sends the PAGE — not the fragment
// URL — through the sign-in page with TBY-AUTH-005.
func TestPollingDoesNotKeepTheSessionAlive(t *testing.T) {
	_, mux, now, buf := clockedUI(t, 30*time.Minute)
	c := login(t, mux, "alexis", "pw-admin")
	poll := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/tasks/badge", http.NoBody)
		r.Header.Set("HX-Request", "true")
		r.Header.Set("HX-Current-URL", "http://tobby.test/tasks?status=running")
		r.Header.Set(BackgroundHeader, "1")
		r.AddCookie(c)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	for range 29 {
		*now = now.Add(time.Minute)
		if w := poll(); w.Code != http.StatusOK {
			t.Fatalf("poll at %s: %d", now.Sub(t0), w.Code)
		}
	}
	*now = now.Add(2 * time.Minute)
	w := poll()
	want := "/login?next=" + url.QueryEscape("/tasks?status=running") + "&expired=1"
	if w.Code != http.StatusUnauthorized || w.Header().Get("HX-Redirect") != want {
		t.Errorf("poll after 31 minutes without activity: %d %q, want 401 to %q", w.Code, w.Header().Get("HX-Redirect"), want)
	}
	if recs := auditLines(buf.Bytes(), "session.expire"); len(recs) != 1 || recs[0]["target"] != "idle" {
		t.Errorf("session.expire records = %v", recs)
	}

	// A user's own htmx request (no background marker) still renews.
	c = login(t, mux, "alexis", "pw-admin")
	for range 3 {
		*now = now.Add(20 * time.Minute)
		r := httptest.NewRequest(http.MethodGet, "/tasks", http.NoBody)
		r.Header.Set("HX-Request", "true")
		r.AddCookie(c)
		rw := httptest.NewRecorder()
		mux.ServeHTTP(rw, r)
		if rw.Code != http.StatusOK {
			t.Fatalf("user htmx request at +%s: %d", now.Sub(t0), rw.Code)
		}
	}
}

// TestPollingElementsAreMarked: every polling element of the templates
// carries the background marker — a poll that forgot it would keep its
// tab alive forever.
func TestPollingElementsAreMarked(t *testing.T) {
	for _, page := range []string{"tasks.html", "media.html", "task-detail.html"} {
		raw, err := templatesFS.ReadFile("templates/pages/" + page)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, `hx-trigger="every`) && !strings.Contains(line, BackgroundHeader) {
				t.Errorf("%s: a polling element without the background marker: %s", page, strings.TrimSpace(line))
			}
		}
	}
}
