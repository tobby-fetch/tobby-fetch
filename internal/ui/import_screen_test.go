// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"context"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/importer"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// newTestUIWithQueue wires a full UI over one store root shared by the
// embedded store and a live task queue with the unit-import runner —
// exactly the serve wiring.
func newTestUIWithQueue(t *testing.T) (*UI, *tasks.Queue, *store.Store) {
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
	q, err := tasks.Open(root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	q.Register(tasks.TypeUnitImport, importer.NewRunner(st))

	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("alexis", auth.RoleAdmin, "pw-admin", t0); err != nil {
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
	u := New(authn, slog.New(slog.DiscardHandler), &Options{
		Version: "0.2.0-test", Mode: "mirror",
		Store: st, Queue: q, InspectTimeout: 10 * time.Second,
	})
	return u, q, st
}

// seedImportUpstream starts a real in-memory OCI registry (never a
// protocol mock) and pushes a multi-arch index; returns its reference.
func seedImportUpstream(t *testing.T, platforms ...string) string {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var idx v1.ImageIndex = empty.Index
	for _, p := range platforms {
		parts := strings.SplitN(p, "/", 3)
		img, err := random.Image(256, 1)
		if err != nil {
			t.Fatal(err)
		}
		pf := v1.Platform{OS: parts[0], Architecture: parts[1]}
		if len(parts) == 3 {
			pf.Variant = parts[2]
		}
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{
			Add: img, Descriptor: v1.Descriptor{Platform: &pf},
		})
	}
	ref := host.Host + "/library/redis:7.2"
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(r, idx); err != nil {
		t.Fatal(err)
	}
	return ref
}

// postImportForm performs an authenticated POST /import with the
// session's CSRF token.
func postImportForm(t *testing.T, u *UI, mux *http.ServeMux, c *http.Cookie, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	sess, ok := u.authn.Sessions.Get(c.Value, time.Now())
	if !ok {
		t.Fatal("no live session for the cookie")
	}
	form.Set("csrf", sess.CSRF)
	r := httptest.NewRequest(http.MethodPost, "/import", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(c)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestImportScreenDeepLink is the §5.6 addressable-inspection contract:
// GET /import?ref= pre-fills the form and arms the load-triggered
// inspection; ?platform= rides along. Viewers never reach the screen
// (operator+, taxonomized 403).
func TestImportScreenDeepLink(t *testing.T) {
	u, _, _ := newTestUIWithQueue(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := get(t, mux, c, "/import?ref=docker.io/library/redis:7.2&platform=linux-amd64", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/import = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`value="docker.io/library/redis:7.2"`, // pre-filled form
		`hx-trigger="load"`,                   // inspection fires on load
		"inspect=1",
		"platform=linux-amd64", // pre-selection carried through
		`id="inspect-zone"`,
		`aria-busy="true"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/import?ref= misses %q", want)
		}
	}

	// Without a reference: no armed inspection, empty zone.
	w = get(t, mux, c, "/import", nil)
	if body := w.Body.String(); strings.Contains(body, "inspect=1") {
		t.Error("/import without ref must not trigger an inspection")
	}

	// Viewer: operator+ gate, taxonomized 403 (TBY-AUTH-003).
	cv := login(t, mux, "lecteur", "pw-view")
	w = get(t, mux, cv, "/import", nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
		t.Errorf("viewer /import = %d, want taxonomized 403", w.Code)
	}
}

// TestImportInspectFragment covers step 2 on the canonical URL: platform
// table with per-digest statuses (FR-026), everything checked but
// up-to-date, and the ?platform= narrowing.
func TestImportInspectFragment(t *testing.T) {
	u, _, _ := newTestUIWithQueue(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")
	ref := seedImportUpstream(t, "linux/amd64", "linux/arm64")

	w := get(t, mux, c, "/import?ref="+url.QueryEscape(ref), map[string]string{"HX-Request": "true"})
	if w.Code != http.StatusOK {
		t.Fatalf("inspect fragment = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("fragment carries the full document")
	}
	for _, want := range []string{
		"linux/amd64", "linux/arm64",
		"t-badge--new", "sha256:",
		`data-focus-target`, // step-2 heading takes focus
		"(2 platforms",      // the button states the selection
	} {
		if !strings.Contains(body, want) {
			t.Errorf("inspect fragment misses %q", want)
		}
	}
	if got := strings.Count(body, " checked"); got != 2 {
		t.Errorf("checked platforms = %d, want 2 (all new)", got)
	}

	// Deep-linked platform: only linux/amd64 pre-checked.
	w = get(t, mux, c, "/import?ref="+url.QueryEscape(ref)+"&platform=linux-amd64",
		map[string]string{"HX-Request": "true"})
	body = w.Body.String()
	if got := strings.Count(body, " checked"); got != 1 {
		t.Errorf("pre-selected checked platforms = %d, want 1", got)
	}
	if !strings.Contains(body, "(1 platform,") {
		t.Error("selection summary does not follow the pre-selection")
	}
}

// seedChartUpstream pushes a single-manifest artifact whose config media
// type marks it as a Helm chart, and returns its reference.
func seedChartUpstream(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	img, err := random.Image(128, 1)
	if err != nil {
		t.Fatal(err)
	}
	chart := mutate.ConfigMediaType(img, "application/vnd.cncf.helm.config.v1+json")
	ref := host.Host + "/bitnamicharts/wordpress:19.2.6"
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(r, chart); err != nil {
		t.Fatal(err)
	}
	return ref
}

// TestImportVendorOptIn is the FR-025 screen contract: the vendoring
// checkbox renders only when the inspection detects a chart — with the
// FR-075 note stating the digest/signature consequence — and the POST
// carries the explicit opt-in onto the created task, default off.
func TestImportVendorOptIn(t *testing.T) {
	u, q, _ := newTestUIWithQueue(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	// A container image never shows the vendoring control.
	iref := seedImportUpstream(t, "linux/amd64")
	w := get(t, mux, c, "/import?ref="+url.QueryEscape(iref), map[string]string{"HX-Request": "true"})
	if strings.Contains(w.Body.String(), `name="vendor"`) {
		t.Error("vendor checkbox rendered for a container image")
	}

	// A chart does, unchecked, with the consequence stated (FR-075).
	cref := seedChartUpstream(t)
	w = get(t, mux, c, "/import?ref="+url.QueryEscape(cref), map[string]string{"HX-Request": "true"})
	body := w.Body.String()
	if !strings.Contains(body, `name="vendor"`) {
		t.Fatalf("vendor checkbox missing for a chart: %s", body)
	}
	if !strings.Contains(body, "vendoring") || !strings.Contains(body, "digest") {
		t.Error("the vendoring note does not state the consequence")
	}

	// POST with the box checked: the task carries the opt-in.
	w = postImportForm(t, u, mux, c, url.Values{"ref": {cref}, "platform": {"artifact"}, "vendor": {"1"}},
		map[string]string{"HX-Request": "true"})
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("HX-Redirect"), "/tasks/tsk_") {
		t.Fatalf("vendor POST = %d redirect=%q", w.Code, w.Header().Get("HX-Redirect"))
	}
	created := q.List("", "", "")
	if len(created) != 1 || !created[0].VendorDependencies {
		t.Fatalf("task after checked POST = %+v", created)
	}

	// Without it: default off (TBY-CHT-001 keeps refusing at run time).
	w = postImportForm(t, u, mux, c, url.Values{"ref": {cref}, "platform": {"artifact"}}, nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("plain POST = %d", w.Code)
	}
	created = q.List("", "", "")
	if len(created) != 2 {
		t.Fatalf("queue holds %d tasks, want 2", len(created))
	}
	// Newest first: the unchecked submission must not carry the opt-in.
	if created[0].VendorDependencies {
		t.Errorf("unchecked POST created a vendoring task: %+v", created[0])
	}
}

// TestImportInspectErrorStatus: an unreachable source registry answers the
// R-03 block with its real HTTP status, inside the swap zone (ADR-0015
// §6).
func TestImportInspectErrorStatus(t *testing.T) {
	u, _, _ := newTestUIWithQueue(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := get(t, mux, c, "/import?ref="+url.QueryEscape("127.0.0.1:1/library/x:1"),
		map[string]string{"HX-Request": "true"})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("unreachable inspect = %d, want 502", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"TBY-REG-002", `role="alert"`, `id="inspect-zone"`} {
		if !strings.Contains(body, want) {
			t.Errorf("inspect error misses %q", want)
		}
	}
}

// TestImportSubmitCreatesTask locks the POST contract: server-side
// re-inspection (digests pinned by the server, never the client), one item
// per checked platform, HX-Redirect to the task — and a plain 303 for the
// no-JS path.
func TestImportSubmitCreatesTask(t *testing.T) {
	u, q, _ := newTestUIWithQueue(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")
	ref := seedImportUpstream(t, "linux/amd64", "linux/arm64")

	form := url.Values{"ref": {ref}, "platform": {"linux/amd64", "linux/arm64"}}
	w := postImportForm(t, u, mux, c, form, map[string]string{"HX-Request": "true"})
	if w.Code != http.StatusOK {
		t.Fatalf("htmx POST /import = %d: %s", w.Code, w.Body.String())
	}
	redirect := w.Header().Get("HX-Redirect")
	if !strings.HasPrefix(redirect, "/tasks/tsk_") {
		t.Fatalf("HX-Redirect = %q, want /tasks/tsk_…", redirect)
	}
	created := q.List("", "", "")
	if len(created) != 1 {
		t.Fatalf("queue holds %d tasks, want 1", len(created))
	}
	task := created[0]
	if task.Reference != ref || task.Actor != "alexis" || len(task.Items) != 2 {
		t.Errorf("task = %+v", task)
	}
	for _, it := range task.Items {
		if !strings.HasPrefix(it.Digest, "sha256:") {
			t.Errorf("item %s digest %q not pinned server-side", it.Name, it.Digest)
		}
	}

	// The no-JS path answers a standard 303 to the same task URL space.
	w = postImportForm(t, u, mux, c, url.Values{"ref": {ref}, "platform": {"linux/amd64"}}, nil)
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/tasks/tsk_") {
		t.Errorf("plain POST /import = %d location=%q, want 303 to /tasks/…", w.Code, w.Header().Get("Location"))
	}

	// A selection that names no real platform re-opens the inspection.
	w = postImportForm(t, u, mux, c, url.Values{"ref": {ref}, "platform": {"windows/amd64"}}, nil)
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/import?ref=") {
		t.Errorf("empty selection = %d location=%q, want 303 back to /import", w.Code, w.Header().Get("Location"))
	}
}
