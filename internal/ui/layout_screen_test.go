// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The interface half of FR-051 and FR-046. The API mirror is tested in
// internal/api and the behaviour itself in internal/interop; what these
// tests hold is that the screens reach the same service with the same
// rules — in particular that the typed confirmation is not something the
// browser surface can skip.

package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/interop"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// newLayoutUI wires a UI with the interoperability service on a real
// store, and returns the mux and an admin session.
func newLayoutUI(t *testing.T) (u *UI, st *store.Store, mux *http.ServeMux, session *http.Cookie) {
	t.Helper()
	st = openTestStore(t)
	accounts := testAccounts(t)
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(12 * time.Hour),
		Logger:   slog.New(slog.DiscardHandler),
	}
	u = New(authn, slog.New(slog.DiscardHandler), &Options{
		Version: "0.5.0-test", Mode: "mirror", Store: st,
		Interop: interop.New(st, nil, "", slog.New(slog.DiscardHandler)),
	})
	mux = mount(u)
	return u, st, mux, login(t, mux, "alexis", "pw-admin")
}

// post submits a form as the signed-in admin, carrying the session's
// anti-forgery token (NFR-012).
func postLayout(t *testing.T, u *UI, mux *http.ServeMux, session *http.Cookie, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	form.Set("csrf", csrfOf(t, u, session))
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(session)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestLayoutScreenEstimatesWithoutWriting: the estimate button is the
// side-effect-free operation of FR-055, reachable from the browser.
func TestLayoutScreenEstimatesWithoutWriting(t *testing.T) {
	u, st, mux, session := newLayoutUI(t)
	seedLayoutImage(t, st)

	dir := t.TempDir()
	w := postLayout(t, u, mux, session, "/admin/oci-layout/plan", url.Values{
		"output": {filepath.Join(dir, "payload.tar")},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("estimate = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "id=\"layout-output\"") {
		t.Error("the estimate did not come back on the export screen")
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, "*")); len(entries) != 0 {
		t.Errorf("the estimate wrote %v", entries)
	}
}

// TestStoreResetKeepsItsTypedConfirmationOnTheBrowserSurface is FR-046
// where it is easiest to get wrong: a screen that "already asked" with a
// dialog and posted without the phrase would satisfy nobody's audit.
func TestStoreResetKeepsItsTypedConfirmationOnTheBrowserSurface(t *testing.T) {
	u, st, mux, session := newLayoutUI(t)
	seedLayoutImage(t, st)

	w := postLayout(t, u, mux, session, "/admin/store/reset", url.Values{"confirmation": {"yes"}})
	entry, _ := taxonomy.Lookup(taxonomy.CodeResetConfirmation)
	if w.Code != entry.HTTPStatus {
		t.Fatalf("wrong confirmation = %d, want the TBY-STO-006 status: %s", w.Code, w.Body.String())
	}
	ctx := context.Background()
	repos, err := st.Repositories(ctx)
	if err != nil || len(repos) != 1 {
		t.Fatalf("the refused reset touched the store: %v, %v", repos, err)
	}

	w = postLayout(t, u, mux, session, "/admin/store/reset", url.Values{
		"confirmation": {interop.ConfirmationPhrase},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("reset = %d, want 200: %s", w.Code, w.Body.String())
	}
	repos, err = st.Repositories(ctx)
	if err != nil || len(repos) != 0 {
		t.Fatalf("the store was not emptied: %v, %v", repos, err)
	}
}

// seedLayoutImage stores one image so the screens have something to
// estimate and something to reset.
func seedLayoutImage(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	const repo = "docker.io/library/alpine"
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	layer := []byte("a layer")
	descs := make([]ocispec.Descriptor, 0, 2)
	for i, blob := range [][]byte{config, layer} {
		d := digest.FromBytes(blob)
		if err := st.WriteBlob(ctx, repo, d, bytes.NewReader(blob)); err != nil {
			t.Fatalf("writing blob: %v", err)
		}
		mediaType := ocispec.MediaTypeImageConfig
		if i == 1 {
			mediaType = ocispec.MediaTypeImageLayerGzip
		}
		descs = append(descs, ocispec.Descriptor{MediaType: mediaType, Digest: d, Size: int64(len(blob))})
	}
	payload, err := json.Marshal(ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    descs[0],
		Layers:    descs[1:],
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutManifest(ctx, repo, ocispec.MediaTypeImageManifest, payload, "3.22.1"); err != nil {
		t.Fatalf("storing the manifest: %v", err)
	}
}
