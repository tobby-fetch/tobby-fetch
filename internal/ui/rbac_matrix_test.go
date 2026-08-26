// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The role × route permission matrix (FR-074: "a documented and enforced
// matrix; negative tests confirm each role's refusals"). Its documented
// form is docs/rbac-matrix.md; this file is the enforced one, and the two
// are meant to be read side by side.
//
// The matrix lives in the ui package because it spans both HTTP surfaces:
// the web UI owns the root of the listener and the REST API mirrors it
// (FR-061), so a floor that drifts on one side and not the other is
// exactly the defect worth catching. Splitting the table in two would let
// each half look complete on its own.
//
// Four properties are checked, in order of importance:
//
//  1. COMPLETENESS. Every route the code registers is declared here, and
//     every declaration corresponds to a registered route. This is the
//     property that catches the real regression — a route added later with
//     app() where admin() was meant. It works because Mount and API.Handle
//     record what they register: a new route appears in the recorded set
//     without appearing in the table, and this test fails before anyone
//     has to remember the rule.
//  2. REFUSAL. Every role strictly below a route's floor gets the
//     taxonomized role refusal (TBY-AUTH-003) — the FR-074 negative tests.
//  3. ADMISSION. The floor role is not refused. Guards against
//     over-restriction, which completeness and refusal alone cannot see.
//  4. ANONYMITY. No gated route answers without authentication (R-01).

package ui

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// rolePublic marks a route reachable without authentication. The set is
// deliberately tiny (R-01: the instance is never exposed open) and every
// member is a session-establishment or presentation concern.
const rolePublic auth.Role = ""

// rbacRoute is one row of the matrix.
type rbacRoute struct {
	// Pattern is the route exactly as it is registered — the string this
	// test compares against the recorded route table.
	Pattern string
	// Floor is the minimum role, or rolePublic.
	Floor auth.Role
	// Why records the reason a floor is what it is, when it is not
	// self-evident from the path.
	Why string
	// Method and Path are the concrete probe. Probes deliberately target
	// non-existent resources and submit invalid input: the properties
	// under test are decided by the middleware, BEFORE the handler runs,
	// so a probe that fails afterwards (404, 422) proves the gate opened
	// without mutating anything. That is what lets every probe share one
	// instance instead of rebuilding it per row.
	Method string
	Path   string
	// Form is the UI form body; the session CSRF token is added by the
	// probe. Body is the raw API request body.
	Form string
	Body string
}

// uiMatrix is the role floor of every web UI route (ADR-0015: the UI owns
// the root of the listener).
var uiMatrix = []rbacRoute{
	{Pattern: "GET /static/", Floor: rolePublic, Why: "stylesheet and scripts of the login page itself", Method: "GET", Path: "/static/app.css"},
	{Pattern: "GET /login", Floor: rolePublic, Why: "the sign-in form", Method: "GET", Path: "/login"},
	{Pattern: "POST /login", Floor: rolePublic, Why: "credential submission", Method: "POST", Path: "/login", Form: "username=ghost&password=ghost"},
	{Pattern: "POST /lang", Floor: rolePublic, Why: "the login page offers the language switcher (FR-063)", Method: "POST", Path: "/lang", Form: "lang=fr&back=%2F"},
	{Pattern: "POST /theme", Floor: rolePublic, Why: "presentation preference, offered pre-login like the language", Method: "POST", Path: "/theme", Form: "theme=light&back=%2F"},

	{Pattern: "POST /logout", Floor: auth.RoleViewer, Method: "POST", Path: "/logout"},
	{Pattern: "GET /{$}", Floor: auth.RoleViewer, Method: "GET", Path: "/"},
	{Pattern: "GET /tasks/badge", Floor: auth.RoleViewer, Method: "GET", Path: "/tasks/badge"},
	{Pattern: "GET /tasks", Floor: auth.RoleViewer, Method: "GET", Path: "/tasks"},
	{Pattern: "GET /tasks/{id}", Floor: auth.RoleViewer, Method: "GET", Path: "/tasks/no-such-task"},
	{Pattern: "GET /import", Floor: auth.RoleOperator, Why: "importing is an operator action (FR-023)", Method: "GET", Path: "/import"},
	{Pattern: "POST /import", Floor: auth.RoleOperator, Method: "POST", Path: "/import", Form: "reference="},
	{Pattern: "GET /content", Floor: auth.RoleViewer, Method: "GET", Path: "/content"},
	{Pattern: "GET /content/{repo...}", Floor: auth.RoleViewer, Method: "GET", Path: "/content/no/such/repo"},
	{Pattern: "POST /content/{repo...}", Floor: auth.RoleAdmin, Why: "removal of unit-imported content (FR-044 amendment)", Method: "POST", Path: "/content/no/such/repo/-/delete"},
	{Pattern: "GET /recipes", Floor: auth.RoleViewer, Method: "GET", Path: "/recipes"},
	{Pattern: "POST /recipes/sync", Floor: auth.RoleOperator, Why: "triggering a synchronization is an operator action (FR-014)", Method: "POST", Path: "/recipes/sync"},
	{Pattern: "GET /recipes/publish", Floor: auth.RoleOperator, Why: "the publication form (R-40): only a role that can publish is offered one", Method: "GET", Path: "/recipes/publish"},
	{Pattern: "POST /recipes/publish", Floor: auth.RoleOperator, Why: "publishing writes into another zone's cookbook (R-40); the write is audited (FR-094)", Method: "POST", Path: "/recipes/publish", Form: "reference=&document="},
	{Pattern: "GET /recipes/{recipe}/mapping", Floor: auth.RoleViewer, Method: "GET", Path: "/recipes/no-such-recipe/mapping"},
	{Pattern: "GET /account", Floor: auth.RoleViewer, Why: "self-service: every authenticated role manages its own account (R-34)", Method: "GET", Path: "/account"},
	{Pattern: "POST /account/password", Floor: auth.RoleViewer, Method: "POST", Path: "/account/password", Form: "current=x&new=&confirm="},
	{Pattern: "GET /admin/accounts", Floor: auth.RoleAdmin, Method: "GET", Path: "/admin/accounts"},
	{Pattern: "POST /admin/accounts", Floor: auth.RoleAdmin, Why: "account creation (FR-073)", Method: "POST", Path: "/admin/accounts", Form: "name=&role=viewer&password=&confirm="},
	{Pattern: "POST /admin/accounts/delete", Floor: auth.RoleAdmin, Why: "account removal (FR-073)", Method: "POST", Path: "/admin/accounts/delete", Form: "name=no-such-account"},
	{Pattern: "POST /admin/accounts/role", Floor: auth.RoleAdmin, Why: "role change (FR-074)", Method: "POST", Path: "/admin/accounts/role", Form: "name=no-such-account&role=viewer"},
	{Pattern: "POST /admin/accounts/tokens", Floor: auth.RoleAdmin, Method: "POST", Path: "/admin/accounts/tokens", Form: "name=&role=viewer"},
	{Pattern: "POST /admin/accounts/tokens/revoke", Floor: auth.RoleAdmin, Method: "POST", Path: "/admin/accounts/tokens/revoke", Form: "name=no-such-token"},
	{Pattern: "GET /admin/retriever", Floor: auth.RoleAdmin, Why: "reveals the configured desired-state source (FR-010)", Method: "GET", Path: "/admin/retriever"},
	{Pattern: "POST /admin/retriever/interval", Floor: auth.RoleAdmin, Why: "changes how often this instance promotes, unattended (FR-013); the change is audited as sensitive configuration (FR-094)", Method: "POST", Path: "/admin/retriever/interval"},
	{Pattern: "GET /admin/network", Floor: auth.RoleAdmin, Why: "reveals the instance's own TLS identity and its outbound path (FR-082, FR-080)", Method: "GET", Path: "/admin/network"},
	{Pattern: "POST /admin/network/certificate", Floor: auth.RoleAdmin, Why: "decides what every client of this instance authenticates against (FR-082); audited as sensitive configuration (FR-094)", Method: "POST", Path: "/admin/network/certificate"},
	{Pattern: "GET /help", Floor: auth.RoleViewer, Method: "GET", Path: "/help"},
	{Pattern: "GET /help/{page...}", Floor: auth.RoleViewer, Why: "the embedded operations guides (NFR-003 amendment): readable by whoever operates the instance, and by nobody who has not signed in (R-01)", Method: "GET", Path: "/help/passthrough/operate"},
	{Pattern: "GET /help/-/assets/{name}", Floor: auth.RoleViewer, Why: "the screenshots of those guides; same floor as the pages that show them", Method: "GET", Path: "/help/-/assets/no-such-shot.png"},
	{Pattern: "GET /about", Floor: auth.RoleViewer, Method: "GET", Path: "/about"},
	{Pattern: "GET /about/third-party", Floor: auth.RoleViewer, Method: "GET", Path: "/about/third-party"},
	{Pattern: "GET /api-docs", Floor: auth.RoleViewer, Method: "GET", Path: "/api-docs"},
	{Pattern: "/", Floor: auth.RoleViewer, Why: "the taxonomized 404 renders inside the authenticated shell (UI-SPEC §5.13)", Method: "GET", Path: "/no-such-page"},
}

// apiMatrix is the role floor of every /api/v1 endpoint. It must mirror
// the UI floors action for action (FR-061): a screen an operator can use
// has an endpoint an operator can call, and no more.
var apiMatrix = []rbacRoute{
	{Pattern: "GET /api/v1/openapi.yaml", Floor: auth.RoleViewer, Method: "GET", Path: "/api/v1/openapi.yaml"},
	{Pattern: "GET /api/v1/content", Floor: auth.RoleViewer, Method: "GET", Path: "/api/v1/content"},
	{Pattern: "GET /api/v1/content/{repo...}", Floor: auth.RoleViewer, Method: "GET", Path: "/api/v1/content/no/such/repo"},
	{Pattern: "DELETE /api/v1/content/{repo...}", Floor: auth.RoleAdmin, Method: "DELETE", Path: "/api/v1/content/no/such/repo"},
	{Pattern: "POST /api/v1/import", Floor: auth.RoleOperator, Method: "POST", Path: "/api/v1/import", Body: `{"reference":""}`},
	{Pattern: "GET /api/v1/import/inspect", Floor: auth.RoleOperator, Method: "GET", Path: "/api/v1/import/inspect"},
	{Pattern: "GET /api/v1/tasks", Floor: auth.RoleViewer, Method: "GET", Path: "/api/v1/tasks"},
	{Pattern: "GET /api/v1/tasks/{id}", Floor: auth.RoleViewer, Method: "GET", Path: "/api/v1/tasks/no-such-task"},
	{Pattern: "GET /api/v1/tasks/{id}/logs", Floor: auth.RoleViewer, Method: "GET", Path: "/api/v1/tasks/no-such-task/logs"},
	{Pattern: "GET /api/v1/recipes", Floor: auth.RoleViewer, Method: "GET", Path: "/api/v1/recipes"},
	{Pattern: "GET /api/v1/recipes/{recipe}/mapping", Floor: auth.RoleViewer, Method: "GET", Path: "/api/v1/recipes/no-such-recipe/mapping"},
	{Pattern: "POST /api/v1/sync", Floor: auth.RoleOperator, Method: "POST", Path: "/api/v1/sync"},
	{Pattern: "POST /api/v1/recipes/publish", Floor: auth.RoleOperator, Method: "POST", Path: "/api/v1/recipes/publish", Body: `{"reference":"","document":""}`},
	{Pattern: "GET /api/v1/media", Floor: auth.RoleViewer, Why: "the medium's identity and the zone's last import (FR-052, R-28): reading, like every other inventory view", Method: "GET", Path: "/api/v1/media"},
	{Pattern: "POST /api/v1/media/verify", Floor: auth.RoleOperator, Why: "re-hashing a whole medium is work, not a read (FR-054); the FR-054 waivers inside it need admin, enforced in the handler", Method: "POST", Path: "/api/v1/media/verify", Body: `{}`},
	{Pattern: "POST /api/v1/media/import", Floor: auth.RoleOperator, Why: "pushing a transported medium into the zone registry (FR-052), audited (FR-094); the waivers need admin, enforced in the handler", Method: "POST", Path: "/api/v1/media/import", Body: `{}`},
	{Pattern: "GET /api/v1/network", Floor: auth.RoleAdmin, Method: "GET", Path: "/api/v1/network"},
	{Pattern: "PUT /api/v1/network/certificate", Floor: auth.RoleAdmin, Method: "PUT", Path: "/api/v1/network/certificate", Body: `{"certificate":"","key":""}`},
	{Pattern: "GET /api/v1/retriever", Floor: auth.RoleAdmin, Method: "GET", Path: "/api/v1/retriever"},
	{Pattern: "PUT /api/v1/retriever/interval", Floor: auth.RoleAdmin, Method: "PUT", Path: "/api/v1/retriever/interval"},
	{Pattern: "DELETE /api/v1/retriever/interval", Floor: auth.RoleAdmin, Method: "DELETE", Path: "/api/v1/retriever/interval"},
	{Pattern: "POST /api/v1/account/password", Floor: auth.RoleViewer, Why: "self-service mirror of /account (R-34): no floor beyond authentication", Method: "POST", Path: "/api/v1/account/password", Body: `{"current":"x","new":""}`},
	{Pattern: "GET /api/v1/accounts", Floor: auth.RoleAdmin, Method: "GET", Path: "/api/v1/accounts"},
	{Pattern: "POST /api/v1/accounts", Floor: auth.RoleAdmin, Method: "POST", Path: "/api/v1/accounts", Body: `{"name":"","role":"viewer","password":""}`},
	{Pattern: "PATCH /api/v1/accounts/{name}", Floor: auth.RoleAdmin, Method: "PATCH", Path: "/api/v1/accounts/no-such-account", Body: `{"role":"viewer"}`},
	{Pattern: "DELETE /api/v1/accounts/{name}", Floor: auth.RoleAdmin, Method: "DELETE", Path: "/api/v1/accounts/no-such-account"},
	{Pattern: "GET /api/v1/tokens", Floor: auth.RoleAdmin, Method: "GET", Path: "/api/v1/tokens"},
	{Pattern: "POST /api/v1/tokens", Floor: auth.RoleAdmin, Method: "POST", Path: "/api/v1/tokens", Body: `{"name":"","role":"viewer"}`},
	{Pattern: "POST /api/v1/tokens/{name}/revoke", Floor: auth.RoleAdmin, Method: "POST", Path: "/api/v1/tokens/no-such-token/revoke"},
}

// rolesBelow lists the roles strictly below floor — the negative cases
// FR-074 asks for. Empty for the viewer floor: nothing authenticated ranks
// under it.
func rolesBelow(floor auth.Role) []auth.Role {
	var out []auth.Role
	for _, r := range []auth.Role{auth.RoleViewer, auth.RoleOperator, auth.RoleAdmin} {
		if !r.AtLeast(floor) {
			out = append(out, r)
		}
	}
	return out
}

// recordingRouter is a Router that mounts onto a real mux and remembers
// what it mounted. Go's ServeMux cannot be enumerated, so the route table
// is captured at declaration time — which is also the only moment at
// which it is guaranteed complete.
type recordingRouter struct {
	mux      *http.ServeMux
	patterns []string
}

func (r *recordingRouter) Handle(pattern string, h http.Handler) {
	r.patterns = append(r.patterns, pattern)
	r.mux.Handle(pattern, h)
}

func (r *recordingRouter) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	r.patterns = append(r.patterns, pattern)
	r.mux.HandleFunc(pattern, h)
}

// rbacEnv is one fully wired instance: the UI and the API on a single mux,
// as internal/cli/serve.go assembles them, plus one session and one static
// token per role.
type rbacEnv struct {
	u        *UI
	mux      *http.ServeMux
	patterns []string
	apiRoute []string
	tokens   map[auth.Role]string
}

// newRBACEnv builds the instance. Sessions are created directly on the
// session table rather than through /login, and roles are carried by
// static tokens on the API side: this test is about authorization, and
// making it pay for argon2id on every probe would only make it slow.
func newRBACEnv(t *testing.T) *rbacEnv {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(context.Background(), root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing store: %v", err)
		}
	})
	queue, err := tasks.Open(root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(12 * time.Hour),
		Logger:   slog.New(slog.DiscardHandler),
		Now:      func() time.Time { return t0 },
	}
	u := New(authn, slog.New(slog.DiscardHandler), &Options{
		Version: "0.4.0-test", Mode: "mirror", Store: st, Queue: queue,
	})
	u.Now = func() time.Time { return t0 }

	rec := &recordingRouter{mux: http.NewServeMux()}
	u.Mount(rec)

	restAPI := api.New(authn, slog.New(slog.DiscardHandler))
	api.RegisterContent(restAPI, st)
	api.RegisterTasks(restAPI, queue, st, time.Second, nil)
	api.RegisterAccounts(restAPI, accounts)
	api.RegisterRecipes(restAPI, &api.RecipeOptions{Store: st, Queue: queue})
	// R-40 and FR-082 mirrors. Both are registered without a publisher
	// and without a certificate: the matrix probes the GATE, and every
	// row here is decided before the handler runs.
	api.RegisterPublish(restAPI, nil)
	api.RegisterNetwork(restAPI, &api.NetworkOptions{})
	// FR-052: registered without an engine, like the two above — the
	// matrix probes the gate, and every row is decided before the
	// handler runs.
	api.RegisterMedia(restAPI, &api.MediaOptions{})
	api.RegisterOpenAPI(restAPI)
	rec.mux.Handle("/api/v1/", restAPI.Handler())

	env := &rbacEnv{
		u: u, mux: rec.mux, patterns: rec.patterns,
		apiRoute: restAPI.Routes(),
		tokens:   map[auth.Role]string{},
	}
	for _, role := range []auth.Role{auth.RoleViewer, auth.RoleOperator, auth.RoleAdmin} {
		secret, _, err := accounts.CreateToken("probe-"+string(role), role, t0)
		if err != nil {
			t.Fatal(err)
		}
		env.tokens[role] = secret
	}
	return env
}

// probeUI issues one UI probe as role, with a fresh session and its CSRF
// token. A fresh session per probe keeps POST /logout from taking the
// following rows down with it.
func (e *rbacEnv) probeUI(t *testing.T, role auth.Role, row *rbacRoute) *httptest.ResponseRecorder {
	t.Helper()
	body := row.Form
	var r *http.Request
	if role != rolePublic {
		sess := e.u.authn.Sessions.Create("probe-"+string(role), role, t0)
		if row.Method != http.MethodGet {
			if body != "" {
				body += "&"
			}
			body += "csrf=" + sess.CSRF
		}
		r = httptest.NewRequest(row.Method, row.Path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sess.ID})
	} else {
		r = httptest.NewRequest(row.Method, row.Path, strings.NewReader(body))
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

// probeAPI issues one API probe as role, authenticated with that role's
// static token (FR-072).
func (e *rbacEnv) probeAPI(t *testing.T, role auth.Role, row *rbacRoute) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(row.Method, row.Path, strings.NewReader(row.Body))
	r.Header.Set("Content-Type", "application/json")
	if role != rolePublic {
		r.Header.Set("Authorization", "Bearer "+e.tokens[role])
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

// roleRefused reports whether the response is the taxonomized role
// refusal. Both halves are load-bearing. The status alone would confuse it
// with a policy refusal (TBY-POL-*, also 403), and the code alone would
// match any page that merely mentions it — /help is the whole error-code
// index, TBY-AUTH-003 included.
func roleRefused(w *httptest.ResponseRecorder) bool {
	return w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), "TBY-AUTH-003")
}

// TestRBACMatrixCoversEveryRoute is the gate that matters: a route the
// code registers but the matrix does not declare fails here, so a new
// screen or endpoint cannot ship with an unreviewed role floor (FR-074).
func TestRBACMatrixCoversEveryRoute(t *testing.T) {
	env := newRBACEnv(t)

	for _, c := range []struct {
		surface    string
		registered []string
		matrix     []rbacRoute
	}{
		{"ui", env.patterns, uiMatrix},
		{"api", env.apiRoute, apiMatrix},
	} {
		declared := make([]string, 0, len(c.matrix))
		for i := range c.matrix {
			declared = append(declared, c.matrix[i].Pattern)
		}
		for _, pattern := range c.registered {
			if !slices.Contains(declared, pattern) {
				t.Errorf("%s route %q is registered but absent from the permission matrix: "+
					"declare its role floor here and in docs/rbac-matrix.md (FR-074)", c.surface, pattern)
			}
		}
		for _, pattern := range declared {
			if !slices.Contains(c.registered, pattern) {
				t.Errorf("%s matrix declares %q, which no longer exists: remove the row "+
					"and its line in docs/rbac-matrix.md", c.surface, pattern)
			}
		}
		if len(declared) != len(c.registered) {
			t.Errorf("%s: %d matrix rows for %d registered routes (a duplicate pattern?)",
				c.surface, len(declared), len(c.registered))
		}
	}
}

// TestRBACMatrixRefusesBelowFloor is the FR-074 negative battery: for
// every route, every role under its floor gets TBY-AUTH-003. Refusals are
// decided before the handler runs, so nothing is mutated here.
func TestRBACMatrixRefusesBelowFloor(t *testing.T) {
	env := newRBACEnv(t)

	for i := range uiMatrix {
		row := &uiMatrix[i]
		for _, role := range rolesBelow(row.Floor) {
			w := env.probeUI(t, role, row)
			if !roleRefused(w) {
				t.Errorf("ui %s as %s = %d, want 403 TBY-AUTH-003 (floor %s)",
					row.Pattern, role, w.Code, row.Floor)
				continue
			}
			if !strings.Contains(w.Body.String(), string(row.Floor)) {
				t.Errorf("ui %s: the refusal shown to %s does not name the required role %s",
					row.Pattern, role, row.Floor)
			}
		}
	}
	for i := range apiMatrix {
		row := &apiMatrix[i]
		for _, role := range rolesBelow(row.Floor) {
			w := env.probeAPI(t, role, row)
			if !roleRefused(w) {
				t.Errorf("api %s as %s = %d, want 403 TBY-AUTH-003 (floor %s): %s",
					row.Pattern, role, w.Code, row.Floor, w.Body.String())
			}
		}
	}
}

// TestRBACMatrixAdmitsAtFloor is the other direction: the floor role is
// never refused. Without it an over-restricted route — admin() where
// operator() was meant — would sail through completeness and refusal
// alike. The probes still fail, on purpose, but after the gate.
func TestRBACMatrixAdmitsAtFloor(t *testing.T) {
	env := newRBACEnv(t)

	for i := range uiMatrix {
		row := &uiMatrix[i]
		role := row.Floor
		if role == rolePublic {
			role = auth.RoleViewer // a public route must also serve the lowest role
		}
		w := env.probeUI(t, role, row)
		if roleRefused(w) {
			t.Errorf("ui %s refuses its own floor %s: %d", row.Pattern, role, w.Code)
		}
		if loc := w.Header().Get("Location"); strings.HasPrefix(loc, "/login?next=") {
			t.Errorf("ui %s sends an authenticated %s to the login page", row.Pattern, role)
		}
	}
	for i := range apiMatrix {
		row := &apiMatrix[i]
		w := env.probeAPI(t, row.Floor, row)
		if roleRefused(w) {
			t.Errorf("api %s refuses its own floor %s: %d %s",
				row.Pattern, row.Floor, w.Code, w.Body.String())
		}
		if w.Code == http.StatusUnauthorized {
			t.Errorf("api %s answers 401 to a valid %s token", row.Pattern, row.Floor)
		}
	}
}

// TestRBACMatrixRefusesAnonymous is the R-01 acceptance across the whole
// matrix: nothing gated answers without credentials. The UI redirects a
// browser to the sign-in page, the API answers an RFC 9457 problem.
func TestRBACMatrixRefusesAnonymous(t *testing.T) {
	env := newRBACEnv(t)

	for i := range uiMatrix {
		row := &uiMatrix[i]
		if row.Floor == rolePublic {
			continue
		}
		w := env.probeUI(t, rolePublic, row)
		if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/login?next=") {
			t.Errorf("anonymous ui %s = %d location=%q, want 303 to /login",
				row.Pattern, w.Code, w.Header().Get("Location"))
		}
	}
	for i := range apiMatrix {
		row := &apiMatrix[i]
		w := env.probeAPI(t, rolePublic, row)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("anonymous api %s = %d, want 401", row.Pattern, w.Code)
		}
	}
}

// TestRBACMatrixMirrorsUIFloors pins the FR-061 parity the matrix exists
// to make visible: each account and content action carries the same floor
// on both surfaces. Read as a list of pairs, not as a loop over the
// tables — the pairing is editorial, and writing it out is what makes a
// divergence obvious in review.
func TestRBACMatrixMirrorsUIFloors(t *testing.T) {
	floorOf := func(rows []rbacRoute, pattern string) auth.Role {
		for i := range rows {
			if rows[i].Pattern == pattern {
				return rows[i].Floor
			}
		}
		t.Fatalf("no matrix row for %q", pattern)
		return rolePublic
	}
	for _, pair := range []struct{ ui, api string }{
		{"GET /content", "GET /api/v1/content"},
		{"GET /content/{repo...}", "GET /api/v1/content/{repo...}"},
		{"POST /content/{repo...}", "DELETE /api/v1/content/{repo...}"},
		{"POST /import", "POST /api/v1/import"},
		{"GET /tasks", "GET /api/v1/tasks"},
		{"GET /recipes", "GET /api/v1/recipes"},
		{"POST /recipes/sync", "POST /api/v1/sync"},
		{"POST /recipes/publish", "POST /api/v1/recipes/publish"},
		{"GET /admin/network", "GET /api/v1/network"},
		{"POST /admin/network/certificate", "PUT /api/v1/network/certificate"},
		{"GET /admin/retriever", "GET /api/v1/retriever"},
		{"POST /admin/retriever/interval", "PUT /api/v1/retriever/interval"},
		{"POST /account/password", "POST /api/v1/account/password"},
		{"GET /admin/accounts", "GET /api/v1/accounts"},
		{"POST /admin/accounts", "POST /api/v1/accounts"},
		{"POST /admin/accounts/role", "PATCH /api/v1/accounts/{name}"},
		{"POST /admin/accounts/delete", "DELETE /api/v1/accounts/{name}"},
		{"POST /admin/accounts/tokens", "POST /api/v1/tokens"},
		{"POST /admin/accounts/tokens/revoke", "POST /api/v1/tokens/{name}/revoke"},
	} {
		if got, want := floorOf(apiMatrix, pair.api), floorOf(uiMatrix, pair.ui); got != want {
			t.Errorf("FR-061 parity: %q needs %s but its mirror %q needs %s",
				pair.ui, want, pair.api, got)
		}
	}
}
