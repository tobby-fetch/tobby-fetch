// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// UI is the web interface: handlers, renderer, and their wiring onto the
// shared listener. It owns the root of the URL space; machine surfaces
// live under the reserved prefixes (ADR-0015 §2).
type UI struct {
	render *Renderer
	authn  *auth.Authenticator
	store  *store.Store
	queue  *tasks.Queue
	logger *slog.Logger

	themeOverride  string
	inspectTimeout time.Duration
	insecureHosts  []string
	// Now injects time in tests.
	Now func() time.Time
}

// Options carries the configuration the UI needs.
type Options struct {
	Version       string
	Mode          string
	ThemeOverride string
	ShowUpcoming  bool
	// Store backs the content browsing screens and the dashboard counters
	// (FR-062) through the accessors of internal/store — never the HTTP
	// loopback.
	Store *store.Store
	// Queue backs the import and task screens (FR-023, roadmap 2.4).
	Queue *tasks.Queue
	// InsecureRegistries lists the per-host plain-HTTP opt-ins
	// (registries.insecure), forwarded to the importer.
	InsecureRegistries []string
	// InspectTimeout bounds one remote inspection (import.inspectTimeout);
	// zero falls back to a safe default.
	InspectTimeout time.Duration
}

// New assembles the UI.
func New(authn *auth.Authenticator, logger *slog.Logger, opts *Options) *UI {
	timeout := opts.InspectTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &UI{
		render:         NewRenderer(logger, opts.Version, opts.Mode, authn.Disabled, opts.ShowUpcoming, opts.ThemeOverride != ""),
		authn:          authn,
		store:          opts.Store,
		queue:          opts.Queue,
		logger:         logger,
		themeOverride:  opts.ThemeOverride,
		inspectTimeout: timeout,
		insecureHosts:  opts.InsecureRegistries,
	}
}

func (u *UI) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

// Mount registers every UI route on mux. Routes and reserved prefixes are
// kept apart by the ADR-0015 collision test.
func (u *UI) Mount(mux *http.ServeMux) {
	mux.Handle("GET /static/", StaticHandler(u.themeOverride))

	// Session endpoints (outside the session gate).
	mux.HandleFunc("GET /login", u.loginPage)
	mux.HandleFunc("POST /login", u.loginSubmit)
	mux.HandleFunc("POST /lang", u.switchLang)
	mux.HandleFunc("POST /theme", u.switchTheme)

	// Authenticated application routes.
	app := func(h http.HandlerFunc) http.Handler { return u.RequireSession(h) }
	operator := func(h http.HandlerFunc) http.Handler {
		return u.RequireSession(u.RequireRole(auth.RoleOperator, h))
	}
	admin := func(h http.HandlerFunc) http.Handler {
		return u.RequireSession(u.RequireRole(auth.RoleAdmin, h))
	}
	mux.Handle("POST /logout", app(u.logout))
	mux.Handle("GET /{$}", app(u.dashboard))
	mux.Handle("GET /tasks/badge", app(u.tasksBadge))
	mux.Handle("GET /tasks", app(u.tasksList))
	mux.Handle("GET /tasks/{id}", app(u.taskDetail))
	mux.Handle("GET /import", operator(u.importScreen))
	mux.Handle("POST /import", operator(u.importSubmit))
	mux.Handle("GET /content", app(u.contentList))
	mux.Handle("GET /content/{repo...}", app(u.contentDetail))

	// Annex surfaces (UI-SPEC §5.9-§5.12): accounts & tokens behind the
	// admin gate; help, about, and the API viewer readable by every role.
	mux.Handle("GET /admin/accounts", admin(u.adminAccounts))
	mux.Handle("POST /admin/accounts/tokens", admin(u.adminTokenCreate))
	mux.Handle("POST /admin/accounts/tokens/revoke", admin(u.adminTokenRevoke))
	mux.Handle("GET /help", app(u.helpScreen))
	mux.Handle("GET /about", app(u.aboutScreen))
	mux.Handle("GET /about/third-party", app(u.thirdPartyNotices))
	mux.Handle("GET /api-docs", app(u.apiDocsScreen))

	// Anything else under the UI namespace is a taxonomized 404 in the
	// shell — never a bare page (UI-SPEC §5.13).
	mux.Handle("/", app(u.notFound))
}

// dashboardData feeds the landing page: the latest real tasks of the
// queue, ready-badged (UI-SPEC §5.2).
type dashboardData struct {
	Recent []taskRow
}

// dashboard renders the landing page (FR-062: instance status, mode
// displayed and never switchable, latest tasks, store counters).
func (u *UI) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("tile") == "store" {
		u.storeTile(w, r)
		return
	}
	data := dashboardData{}
	if u.queue != nil {
		for _, t := range u.queue.List("", "", "") {
			if len(data.Recent) == 4 {
				break
			}
			data.Recent = append(data.Recent, newTaskRow(t, u.now()))
		}
	}
	u.render.Page(w, r, "dashboard", data)
}

// storeTileData feeds the async store tile: real counters via the store
// accessors (FR-062).
type storeTileData struct {
	Repos int
	Tags  int
	Bytes int64
}

// storeTile renders the dashboard's store counter tile — its own swap
// target with a value state and a compact inline error state carrying a
// retry action (UI-SPEC §5.2).
func (u *UI) storeTile(w http.ResponseWriter, r *http.Request) {
	c, err := u.store.Counts(r.Context())
	if err != nil {
		u.logger.LogAttrs(r.Context(), slog.LevelError, "reading store counters",
			slog.String("error", err.Error()))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		u.render.Fragment(w, r, "dashboard", "tile-store-error", nil)
		return
	}
	u.render.Fragment(w, r, "dashboard", "tile-store",
		storeTileData{Repos: c.Repositories, Tags: c.Tags, Bytes: c.PhysicalBytes})
}

// tasksBadge renders the nav task counter: the active-task count, polled
// every 30 s only while something is active — the server stops re-emitting
// the polling attributes when the count reaches zero, and a page load
// re-arms them (auto-terminating load-polling, ADR-0015 / UI-SPEC §8).
func (u *UI) tasksBadge(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	n := 0
	if u.queue != nil {
		n = u.queue.ActiveCount()
	}
	if n == 0 {
		_, _ = w.Write([]byte(`<span id="task-badge"></span>`))
		return
	}
	_, _ = fmt.Fprintf(w, `<span id="task-badge" class="t-nav-count" hx-get="/tasks/badge" hx-trigger="every 30s" hx-swap="outerHTML">%d</span>`, n)
}

func (u *UI) notFound(w http.ResponseWriter, r *http.Request) {
	u.render.Error(w, r, taxonomy.New(taxonomy.CodeNotFound, nil))
}

// loginData feeds the login template.
type loginData struct {
	Next string
	// Failed switches the taxonomized failure block on (generic message:
	// the page never reveals whether the account exists, NFR-015).
	Failed *ErrView
}

// loginPage serves the sign-in form. With authentication disabled there is
// nothing to sign into: 302 to the dashboard, where the permanent banner
// explains the state (FR-075).
func (u *UI) loginPage(w http.ResponseWriter, r *http.Request) {
	if u.authn.Disabled {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if _, ok := u.sessionOf(r); ok {
		http.Redirect(w, r, sanitizeNext(r.URL.Query().Get("next")), http.StatusFound)
		return
	}
	u.render.Page(w, r, "login", loginData{Next: sanitizeNext(r.URL.Query().Get("next"))})
}

// loginSubmit verifies the credentials, opens the session, and redirects
// to the preserved destination. Failures render the same page with the
// generic TBY-AUTH-002 block and are audited with the real network origin
// (FR-094).
func (u *UI) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if u.authn.Disabled {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	name := r.PostFormValue("username")
	next := sanitizeNext(r.PostFormValue("next"))

	acct, ok := u.authn.Store.VerifyPassword(name, r.PostFormValue("password"), u.now())
	if !ok {
		audit.Log(r.Context(), u.logger, &audit.Event{
			Actor: name, Action: audit.ActionLogin, Target: "ui",
			Outcome: audit.OutcomeDenied, Origin: auth.ClientOrigin(r),
		})
		v := u.render.view(r, loginData{
			Next:   next,
			Failed: errView(requestLang(r), taxonomy.New(taxonomy.CodeAuthFailed, nil)),
		})
		u.render.render(w, r, "login", http.StatusUnauthorized, v)
		return
	}

	sess := u.authn.Sessions.Create(acct.Name, acct.Role, u.now())
	setSessionCookie(w, r, sess.ID, sess.Expires)
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: acct.Name, Action: audit.ActionLogin, Target: "ui",
		Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r),
	})
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// logout closes the session. Runs behind RequireSession, so CSRF is
// already checked.
func (u *UI) logout(w http.ResponseWriter, r *http.Request) {
	if sess, ok := sessionFrom(r.Context()); ok {
		u.authn.Sessions.Delete(sess.ID)
		audit.Log(r.Context(), u.logger, &audit.Event{
			Actor: sess.Account, Action: audit.ActionLogout, Target: "ui",
			Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r),
		})
	}
	clearSessionCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// switchLang sets the language cookie and reloads the page completely —
// never a fragment: a half-translated DOM with a wrong html lang is worse
// than a reload (ADR-0015 §7). Reachable pre-login (the login page offers
// the switcher, FR-063); with a session the CSRF token is required.
func (u *UI) switchLang(w http.ResponseWriter, r *http.Request) {
	if !u.prefCSRFOK(r) {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeCSRF, nil))
		return
	}
	lang := r.PostFormValue("lang")
	if lang != "en" && lang != "fr" {
		lang = "en"
	}
	http.SetCookie(w, &http.Cookie{
		Name: langCookie, Value: lang, Path: "/",
		MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil,
	})
	u.redirectBack(w, r)
}

// switchTheme sets the theme cookie; the server stamps data-theme on every
// render, so the choice survives with zero FOUC.
func (u *UI) switchTheme(w http.ResponseWriter, r *http.Request) {
	if !u.prefCSRFOK(r) {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeCSRF, nil))
		return
	}
	theme := r.PostFormValue("theme")
	if theme != "light" {
		theme = "dark"
	}
	http.SetCookie(w, &http.Cookie{
		Name: themeCookie, Value: theme, Path: "/",
		MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil,
	})
	u.redirectBack(w, r)
}

// prefCSRFOK checks the CSRF token for the preference switchers: required
// with a live session, tolerated without one (the login page has no
// session yet, and the preference is harmless).
func (u *UI) prefCSRFOK(r *http.Request) bool {
	if u.authn.Disabled {
		return true
	}
	sess, ok := u.sessionOf(r)
	if !ok {
		return true
	}
	return sess.CheckCSRF(r.PostFormValue("csrf"))
}

// redirectBack returns to the submitted local origin page.
func (u *UI) redirectBack(w http.ResponseWriter, r *http.Request) {
	back := sanitizeNext(r.PostFormValue("back"))
	if _, err := url.Parse(back); err != nil {
		back = "/"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
