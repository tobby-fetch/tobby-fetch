// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Tests for the FileSets screen (FR-047 inventory, FR-048 packing): the
// manual-import marking that tells a packed file set from a Recipe one,
// the absence of any upload control, the confinement of the form to
// files.packRoots, and the success state that says the content is
// unsigned and that serving it is a separate step.

package ui

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/fileserve"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// fileSetsCatalog is the inventory read surface over a real store, with
// the provenance rule the production adapter applies (internal/cli/
// serve.go): the recipe graph wins, then the recorded ledger.
type fileSetsCatalog struct{ st *store.Store }

func (c fileSetsCatalog) Repositories(ctx context.Context) ([]string, error) {
	return c.st.Repositories(ctx)
}

func (c fileSetsCatalog) Tags(ctx context.Context, repo string) ([]string, error) {
	return c.st.Tags(ctx, repo)
}

func (c fileSetsCatalog) Provenance(repo string) string {
	if len(c.st.ManagingRecipes(repo)) > 0 {
		return fileserve.FromRecipe
	}
	p, ok := c.st.ProvenanceOf(repo)
	switch {
	case !ok:
		return fileserve.FromSeed
	case p.Origin == store.OriginLocalPack:
		return fileserve.FromManualImport
	case p.Class == store.ProvenanceUnitImport:
		return fileserve.FromUnitImport
	default:
		return fileserve.FromRecipe
	}
}

// newFileSetsUI wires a UI whose FileSets surface packs only under roots.
// Passing no root is the default posture: the form is inert (FR-075).
func newFileSetsUI(t *testing.T, roots ...string) *UI {
	t.Helper()
	st := openTestStore(t)
	u := newTestUIWithStore(t, false, st)
	u.fileSets = &fileserve.Surface{
		Catalog: fileSetsCatalog{st: st},
		Packer: fileserve.NewPacker(st, "", slog.New(slog.DiscardHandler),
			fileserve.WithPackRoots(roots)),
		Declared: []fileserve.Declared{
			{Name: "site", Ref: "registry.example.com/filesets/site-config", Version: "2.3.1"},
		},
		PackEnabled: len(roots) > 0,
	}
	return u
}

// postPack performs an authenticated POST /filesets/pack with the
// session's CSRF token.
func postPack(t *testing.T, u *UI, mux *http.ServeMux, c *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	sess, ok := u.authn.Sessions.Get(c.Value, time.Now())
	if !ok {
		t.Fatal("no live session for the cookie")
	}
	form.Set("csrf", sess.CSRF)
	r := httptest.NewRequest(http.MethodPost, "/filesets/pack", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// sourceTree writes a small tree under a root the caller can allow.
func sourceTree(t *testing.T) (root, source string) {
	t.Helper()
	root = t.TempDir()
	source = filepath.Join(root, "apt")
	if err := os.MkdirAll(source, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "Release"), []byte("Suite: stable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, source
}

// TestFileSetsScreenOffersNoUploadControl is the SRS §5.2 boundary made
// into a test: the screen that answers "I have files to serve" must never
// grow a file input. FR-048 writes through the OCI import path only.
func TestFileSetsScreenOffersNoUploadControl(t *testing.T) {
	u := newFileSetsUI(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	r := httptest.NewRequest(http.MethodGet, "/filesets", http.NoBody)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("/filesets = %d", w.Code)
	}
	body := w.Body.String()
	for _, forbidden := range []string{`type="file"`, "multipart/form-data", "enctype"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the FileSets screen carries %q: FR-048 opens no upload surface (SRS §5.2)", forbidden)
		}
	}
	// And it says so, rather than leaving the absence to be noticed.
	if !strings.Contains(body, "no upload here") {
		t.Error("the screen does not state that it takes a path rather than a file")
	}
}

// TestFileSetsScreenMarksManualImports is the FR-048 listing requirement:
// a packed file set is distinguishable from a Recipe-delivered one, and
// says it is unsigned.
func TestFileSetsScreenMarksManualImports(t *testing.T) {
	root, source := sourceTree(t)
	u := newFileSetsUI(t, root)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	if w := postPack(t, u, mux, c, url.Values{
		"source": {source}, "name": {"debs"}, "version": {"1.0.0"},
	}); w.Code != http.StatusOK {
		t.Fatalf("pack = %d: %s", w.Code, w.Body.String())
	}

	r := httptest.NewRequest(http.MethodGet, "/filesets", http.NoBody)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	body := w.Body.String()
	for _, want := range []string{
		"localhost/filesets/debs",                   // the packed one
		"registry.example.com/filesets/site-config", // the declared one
		"manual-import",                             // the FR-048 marking
		"unsigned, packaged on this host",           // and what it means
		`t-badge t-badge--outdated`,                 // told apart at a glance
		"declared, waiting for its content",         // the declared one has none yet
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the listing misses %q", want)
		}
	}
}

// TestFileSetsPackSuccessStateSaysUnsignedAndHowToServeIt: the two facts
// an operator must not miss — Tobby signed nothing (ADR-0007), and
// nothing is served until the configuration says so (FR-047).
func TestFileSetsPackSuccessStateSaysUnsignedAndHowToServeIt(t *testing.T) {
	root, source := sourceTree(t)
	u := newFileSetsUI(t, root)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := postPack(t, u, mux, c, url.Values{
		"source": {source}, "name": {"debs"}, "version": {"1.0.0"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("pack = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		"sha256:",
		"holds no private key",
		"ref: localhost/filesets/debs",
		"name: debs",
		"version: 1.0.0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the success state misses %q:\n%s", want, body)
		}
	}
}

// TestFileSetsPackConfinedToPackRoots is the FR-075 posture on this
// surface: with no configured root the form is inert and every path is
// refused, and a path outside the roots is refused by name.
func TestFileSetsPackConfinedToPackRoots(t *testing.T) {
	_, source := sourceTree(t)

	t.Run("no configured root", func(t *testing.T) {
		u := newFileSetsUI(t)
		mux := mount(u)
		c := login(t, mux, "alexis", "pw-admin")

		r := httptest.NewRequest(http.MethodGet, "/filesets", http.NoBody)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if !strings.Contains(w.Body.String(), "disabled") || !strings.Contains(w.Body.String(), "files.packRoots") {
			t.Error("the form is not rendered inert with its explanation")
		}

		w = postPack(t, u, mux, c, url.Values{
			"source": {source}, "name": {"debs"}, "version": {"1.0.0"},
		})
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-FIL-003") {
			t.Fatalf("pack = %d, want 403 TBY-FIL-003:\n%s", w.Code, w.Body.String())
		}
	})

	t.Run("a path outside the roots", func(t *testing.T) {
		u := newFileSetsUI(t, t.TempDir())
		mux := mount(u)
		c := login(t, mux, "alexis", "pw-admin")
		w := postPack(t, u, mux, c, url.Values{
			"source": {source}, "name": {"debs"}, "version": {"1.0.0"},
		})
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-FIL-003") {
			t.Fatalf("pack = %d, want 403 TBY-FIL-003", w.Code)
		}
	})
}

// TestFileSetsPackRefusalKeepsTheSubmission: a refused tree comes back as
// its catalog entry with the fields preserved, so the operator edits
// rather than retypes.
func TestFileSetsPackRefusalKeepsTheSubmission(t *testing.T) {
	root, source := sourceTree(t)
	if err := os.Symlink("/etc/passwd", filepath.Join(source, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	u := newFileSetsUI(t, root)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := postPack(t, u, mux, c, url.Values{
		"source": {source}, "name": {"debs"}, "version": {"1.0.0"},
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("pack = %d, want 422", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"TBY-FIL-002", source, `value="debs"`, `value="1.0.0"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal state misses %q", want)
		}
	}
}

// TestFileSetsScreenIsReadableByAViewerWhoCannotPack: the listing is a
// listing, the action is admin — and the disallowed action is disabled
// with its explanation, never hidden.
func TestFileSetsScreenIsReadableByAViewerWhoCannotPack(t *testing.T) {
	root, _ := sourceTree(t)
	u := newFileSetsUI(t, root)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	r := httptest.NewRequest(http.MethodGet, "/filesets", http.NoBody)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("/filesets as viewer = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "administrator role") {
		t.Error("the viewer is not told why the action is unavailable")
	}
	if !strings.Contains(body, "disabled") {
		t.Error("the submit button is not disabled for a viewer")
	}

	// And the route itself refuses, not only the button.
	w = postPack(t, u, mux, c, url.Values{"source": {"/tmp"}, "name": {"x"}, "version": {"1"}})
	if w.Code != http.StatusForbidden {
		t.Fatalf("POST /filesets/pack as viewer = %d, want 403", w.Code)
	}
}
