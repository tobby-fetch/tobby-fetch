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
	"github.com/tobby-fetch/tobby-fetch/internal/importer"
	"github.com/tobby-fetch/tobby-fetch/internal/policy"
	"github.com/tobby-fetch/tobby-fetch/internal/schedule"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
	"github.com/tobby-fetch/tobby-fetch/internal/tlsadmin"
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

	themeOverride string
	// secureCookies forces the Secure attribute on every cookie when TLS
	// terminates in front of the listener (server.secureCookies, NFR-015).
	secureCookies     bool
	inspectTimeout    time.Duration
	importPolicy      importer.Option
	allowlist         *policy.Allowlist
	retrieverSource   string
	relaxedScopes     []string
	anonymousFileSets []string
	destination       string
	cookbook          string
	interval          *schedule.Interval
	// publisher backs the R-40 publication screen; nil on an instance
	// wired without one, which renders the form inert.
	publisher Publisher
	// serverCert backs the FR-082 network screen: what the listener
	// presents, and — through its own Destination — where a replacement
	// goes. Asking the certificate rather than carrying the configured
	// paths alongside it is what lets an instance on the generated
	// fallback accept one at all. A nil serverCert means plain HTTP.
	serverCert tlsadmin.ServerCert
	// egress is the outbound posture reported on the same screen
	// (FR-080, FR-081).
	egress tlsadmin.Egress
	// Now injects time in tests.
	Now func() time.Time
}

// Options carries the configuration the UI needs.
type Options struct {
	Version       string
	Mode          string
	ThemeOverride string
	ShowUpcoming  bool
	// SecureCookies mirrors server.secureCookies (NFR-015): the operator
	// states that TLS terminates in front of the plain-HTTP listener, so
	// every cookie must carry Secure even though r.TLS is nil.
	SecureCookies bool
	// Store backs the content browsing screens and the dashboard counters
	// (FR-062) through the accessors of internal/store — never the HTTP
	// loopback.
	Store *store.Store
	// Queue backs the import and task screens (FR-023, roadmap 2.4).
	Queue *tasks.Queue
	// ImportPolicy carries the source policy unit import runs under —
	// the allowlist (FR-030) and the per-host plain-HTTP opt-ins
	// (registries.insecure), forwarded to the importer.
	ImportPolicy importer.Option
	// Allowlist is the instance's registry policy (FR-030), reported on
	// the administration screens.
	Allowlist *policy.Allowlist
	// InspectTimeout bounds one remote inspection (import.inspectTimeout);
	// zero falls back to a safe default.
	InspectTimeout time.Duration
	// RetrieverSource is the configured desired-state source (FR-010),
	// reported on the recipes screens.
	RetrieverSource string
	// RelaxedTrustScopes names the declared allowUnsigned trust scopes
	// (FR-033): a permanent banner surfaces the relaxed posture, like the
	// FR-075 override.
	RelaxedTrustScopes []string
	// AnonymousFileSets names the FileSets served without authentication
	// (FR-047 opt-in), surfaced like the FR-075 override.
	AnonymousFileSets []string
	// Destination is the promotion target host and Cookbook the path
	// recipes are propagated to (FR-013, FR-034); both empty on an
	// instance that promotes nothing.
	Destination string
	Cookbook    string
	// Interval paces the reconciliation loop (FR-013), editable from
	// /admin/retriever. Nil in mirror mode, where FR-014 forbids
	// unattended synchronization: the screen then says the setting does
	// not apply rather than offering a control that would do nothing.
	Interval *schedule.Interval
	// Publisher publishes recipe documents into a cookbook (R-40). Nil
	// leaves the publication screen readable and its form inert.
	Publisher Publisher
	// ServerCert is the certificate the listener presents (FR-082). Nil
	// on an instance serving plain HTTP — the screen says so rather than
	// showing an empty certificate.
	ServerCert tlsadmin.ServerCert
	// Egress is the instance's outbound transport, reported as posture
	// (FR-080, FR-081). Only its printable accessors are ever called.
	Egress tlsadmin.Egress
}

// New assembles the UI.
func New(authn *auth.Authenticator, logger *slog.Logger, opts *Options) *UI {
	timeout := opts.InspectTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	render := NewRenderer(logger, opts.Version, opts.Mode, authn.Disabled, opts.ShowUpcoming, opts.ThemeOverride != "")
	// The permanent security banners read the relaxed posture from the
	// renderer (FR-033, FR-047 — same channel as the FR-075 override).
	render.RelaxedScopes = opts.RelaxedTrustScopes
	render.AnonymousFileSets = opts.AnonymousFileSets
	return &UI{
		render:            render,
		authn:             authn,
		store:             opts.Store,
		queue:             opts.Queue,
		logger:            logger,
		themeOverride:     opts.ThemeOverride,
		secureCookies:     opts.SecureCookies,
		inspectTimeout:    timeout,
		importPolicy:      opts.ImportPolicy,
		allowlist:         opts.Allowlist,
		retrieverSource:   opts.RetrieverSource,
		relaxedScopes:     opts.RelaxedTrustScopes,
		anonymousFileSets: opts.AnonymousFileSets,
		destination:       opts.Destination,
		cookbook:          opts.Cookbook,
		interval:          opts.Interval,
		publisher:         opts.Publisher,
		serverCert:        opts.ServerCert,
		egress:            opts.Egress,
	}
}

func (u *UI) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

// Router is the subset of *http.ServeMux Mount needs. The seam exists so
// the RBAC matrix test can record the route table as it is declared and
// fail on a route that ships without a documented role floor (FR-074) —
// Go's ServeMux is write-only, a mounted route cannot be enumerated after
// the fact. Production code passes the real mux (internal/cli/serve.go).
type Router interface {
	Handle(pattern string, handler http.Handler)
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// Mount registers every UI route on mux. Routes and reserved prefixes are
// kept apart by the ADR-0015 collision test; the role floor of each route
// is the wrapper it is declared with here, and docs/rbac-matrix.md is its
// documented form (FR-074).
func (u *UI) Mount(rt Router) {
	// Every UI response — pages, fragments, errors, redirects, static
	// assets — carries the browser security headers (v0.4.2 hardening,
	// headers.go). Wrapping at mount time is what makes a route added
	// later unable to forget them.
	mux := securedRouter{r: rt}
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
	// The only mutation under /content: the FR-044 amendment removal of one
	// unit-imported repository, on the "/-/delete" sub-resource (ADR-0015
	// §3). Admin-gated; the handler enforces the provenance policy.
	mux.Handle("POST /content/{repo...}", admin(u.contentDelete))

	// Recipe screens (FR-014, FR-035/FR-065, UI-SPEC §6): the recorded
	// recipe graph, the per-recipe mapping table, and the sync trigger.
	mux.Handle("GET /recipes", app(u.recipesList))
	mux.Handle("POST /recipes/sync", operator(u.recipesSync))
	// Recipe publication (R-40): the interface half of `tobby recipe
	// push`. Operator-gated — publishing writes into a cookbook — and
	// declared before /recipes/{recipe}/mapping only for readability:
	// the two patterns differ in segment count and cannot collide.
	mux.Handle("GET /recipes/publish", operator(u.recipePublishScreen))
	mux.Handle("POST /recipes/publish", operator(u.recipePublishSubmit))
	mux.Handle("GET /recipes/{recipe}/mapping", app(u.recipeMapping))

	// Account self-service (R-34, FR-061): every authenticated role,
	// viewer included — the admin gate stays on /admin/accounts.
	mux.Handle("GET /account", app(u.accountScreen))
	mux.Handle("POST /account/password", app(u.accountPassword))

	// Annex surfaces (UI-SPEC §5.9-§5.12): accounts & tokens behind the
	// admin gate; help, about, and the API viewer readable by every role.
	mux.Handle("GET /admin/accounts", admin(u.adminAccounts))
	// Account lifecycle (FR-073, FR-074): creation, removal, and role
	// change, so a second administrator can be provisioned without leaving
	// the tool. Sub-resources of /admin/accounts rather than a REST-ish
	// method on the collection: the surface is HTML forms (ADR-0015 §3).
	mux.Handle("POST /admin/accounts", admin(u.adminAccountCreate))
	mux.Handle("POST /admin/accounts/delete", admin(u.adminAccountDelete))
	mux.Handle("POST /admin/accounts/role", admin(u.adminAccountRole))
	mux.Handle("POST /admin/accounts/tokens", admin(u.adminTokenCreate))
	mux.Handle("POST /admin/accounts/tokens/revoke", admin(u.adminTokenRevoke))
	mux.Handle("GET /admin/retriever", admin(u.adminRetriever))
	// The one editable setting on an otherwise read-only screen: the
	// FR-013 cadence, which the requirement asks to be changeable without
	// redeployment. Admin-gated and audited (FR-094), with the exact
	// mirror at PUT/DELETE /api/v1/retriever/interval (FR-061).
	mux.Handle("POST /admin/retriever/interval", admin(u.adminInterval))
	// Network posture and listener certificate (FR-082, FR-062). Admin:
	// the screen reveals the instance's own identity and its outbound
	// path, and the replacement decides what every client of this
	// instance authenticates against.
	mux.Handle("GET /admin/network", admin(u.adminNetwork))
	mux.Handle("POST /admin/network/certificate", admin(u.adminNetworkCertificate))
	mux.Handle("GET /help", app(u.helpScreen))
	// The embedded documentation (R-05, NFR-003): the guides at
	// /help/<section>/<page>, their screenshots on the "/-/" sub-resource
	// separator (ADR-0015 §3) so the asset namespace can never collide
	// with a page key. The asset pattern is more specific than the
	// catch-all and wins on ServeMux's precedence rules, whatever the
	// declaration order.
	mux.Handle("GET /help/-/assets/{name}", app(u.helpAsset))
	mux.Handle("GET /help/{page...}", app(u.helpPage))
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
	origin := auth.ClientOrigin(r)

	// An origin over its failure budget is refused BEFORE the password
	// check (v0.4.2 hardening): the throttle exists to stop the argon2id
	// spend, and the browser surface must not stay open as the cheap way
	// to brute-force what the machine surfaces now bound. Same taxonomy
	// entry as the 429 the API serves (FR-061 parity).
	if !u.authn.FailureAllowed(origin) {
		v := u.render.view(r, loginData{
			Next:   next,
			Failed: errView(requestLang(r), taxonomy.New(taxonomy.CodeAuthRateLimited, nil)),
		})
		u.render.render(w, r, "login", http.StatusTooManyRequests, v)
		return
	}

	acct, ok := u.authn.Store.VerifyPassword(name, r.PostFormValue("password"), u.now())
	if !ok {
		u.authn.RecordFailure(origin)
		audit.Log(r.Context(), u.logger, &audit.Event{
			Actor: name, Action: audit.ActionLogin, Target: "ui",
			Outcome: audit.OutcomeDenied, Origin: origin,
		})
		v := u.render.view(r, loginData{
			Next:   next,
			Failed: errView(requestLang(r), taxonomy.New(taxonomy.CodeAuthFailed, nil)),
		})
		u.render.render(w, r, "login", http.StatusUnauthorized, v)
		return
	}

	sess := u.authn.Sessions.Create(acct.Name, acct.Role, u.now())
	u.setSessionCookie(w, r, sess.ID, sess.Expires)
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
	u.clearSessionCookie(w, r)
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
		MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode, Secure: u.cookieSecure(r),
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
		MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode, Secure: u.cookieSecure(r),
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
