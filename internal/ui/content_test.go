// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// seedContentStore pushes, as a standard OCI client (never a protocol
// mock), a multi-arch image, a helm chart, and a plain image — the fixture
// of every content-screen test.
func seedContentStore(t *testing.T) *store.Store {
	t.Helper()
	st := openTestStore(t)
	srv := httptest.NewServer(st.APIHandler())
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	idx := v1.ImageIndex(empty.Index)
	for _, p := range []struct{ os, arch string }{{"linux", "amd64"}, {"linux", "arm64"}} {
		img, err := random.Image(512, 1)
		if err != nil {
			t.Fatal(err)
		}
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{
			Add:        img,
			Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: p.os, Architecture: p.arch}},
		})
	}
	idx = mutate.IndexMediaType(idx, types.OCIImageIndex)
	ref, err := name.ParseReference(addr + "/docker.io/bitnami/wordpress:6.4.2")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}

	chart, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	chartRef, err := name.ParseReference(addr + "/docker.io/bitnamicharts/wordpress:6.4.2")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(chartRef,
		mutate.ConfigMediaType(chart, "application/vnd.cncf.helm.config.v1+json")); err != nil {
		t.Fatal(err)
	}

	plain, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	plainRef, err := name.ParseReference(addr + "/registry.k8s.io/coredns/coredns:1.11.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(plainRef, plain); err != nil {
		t.Fatal(err)
	}
	return st
}

// get performs an authenticated GET and returns the recorder.
func get(t *testing.T, mux *http.ServeMux, c *http.Cookie, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	r.AddCookie(c)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestContentListRendersSeededStore: /content lists the seeded
// repositories grouped by source host, with kind badges (FR-062).
func TestContentListRendersSeededStore(t *testing.T) {
	u := newTestUIWithStore(t, false, seedContentStore(t))
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := get(t, mux, c, "/content", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/content = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		"docker.io/bitnami/wordpress",
		"docker.io/bitnamicharts/wordpress",
		"registry.k8s.io/coredns/coredns",
		">docker.io</h2>", // host group heading
		">registry.k8s.io</h2>",
		"t-badge--image", "t-badge--chart",
		`data-copy="docker.io/bitnami/wordpress"`, // full path always copiable
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/content misses %q", want)
		}
	}
}

// TestContentSearchFilters: ?q= filters server-side; the htmx submit gets
// only the results fragment on the same canonical URL (ADR-0015 §1).
func TestContentSearchFilters(t *testing.T) {
	u := newTestUIWithStore(t, false, seedContentStore(t))
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := get(t, mux, c, "/content?q=coredns", nil)
	body := w.Body.String()
	if !strings.Contains(body, "registry.k8s.io/coredns/coredns") {
		t.Error("search misses the matching repository")
	}
	if strings.Contains(body, "docker.io/bitnami/wordpress") {
		t.Error("search leaks a non-matching repository")
	}

	// Fragment negotiation: HX-Request without HX-Boosted swaps only the
	// results region — no document, no form re-render.
	w = get(t, mux, c, "/content?q=coredns", map[string]string{"HX-Request": "true"})
	frag := w.Body.String()
	if strings.Contains(frag, "<!DOCTYPE html>") || strings.Contains(frag, "<form") {
		t.Error("htmx fragment carries the document or the form")
	}
	if !strings.Contains(frag, `id="content-results"`) || !strings.Contains(frag, "coredns") {
		t.Error("htmx fragment misses the results region")
	}

	// Kind and prefix filters share the same canonical parameters (R-06).
	w = get(t, mux, c, "/content?kind=HelmChart", nil)
	if body := w.Body.String(); !strings.Contains(body, "bitnamicharts") || strings.Contains(body, "coredns") {
		t.Error("kind filter wrong")
	}
	w = get(t, mux, c, "/content?prefix=registry.k8s.io", nil)
	if body := w.Body.String(); !strings.Contains(body, "coredns") || strings.Contains(body, "bitnami") {
		t.Error("prefix filter wrong")
	}

	// No result: explicit state with a reset action, never a blank page.
	w = get(t, mux, c, "/content?q=nothing-matches", nil)
	if body := w.Body.String(); !strings.Contains(body, `href="/content"`) || w.Code != http.StatusOK {
		t.Errorf("empty result state: code=%d body misses the reset link", w.Code)
	}
}

// TestContentRepoAndManifestPages: the detail pages render tags and
// platforms, and the "/-/" separator parses deterministically
// (ADR-0015 §3).
func TestContentRepoAndManifestPages(t *testing.T) {
	u := newTestUIWithStore(t, false, seedContentStore(t))
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := get(t, mux, c, "/content/docker.io/bitnami/wordpress", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("repo page = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		"6.4.2", "sha256:",
		"docker pull example.com/docker.io/bitnami/wordpress:6.4.2", // httptest host
		"/content?prefix=docker.io",                                 // navigable breadcrumb
		">2/2</td>",                                                 // platforms shown present/total, never total alone (B-007)
	} {
		if !strings.Contains(body, want) {
			t.Errorf("repo page misses %q", want)
		}
	}

	// Chart repository: helm pull command contextualized by kind.
	w = get(t, mux, c, "/content/docker.io/bitnamicharts/wordpress", nil)
	if !strings.Contains(w.Body.String(), "helm pull oci://example.com/docker.io/bitnamicharts/wordpress --version 6.4.2") {
		t.Error("chart repo page misses the helm pull command")
	}

	w = get(t, mux, c, "/content/docker.io/bitnami/wordpress/-/tags/6.4.2", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("manifest page = %d", w.Code)
	}
	body = w.Body.String()
	for _, want := range []string{
		"linux/amd64", "linux/arm64",
		// html/template escapes "+" in text nodes.
		"application/vnd.oci.image.index.v1&#43;json",
		"docker pull example.com/docker.io/bitnami/wordpress@sha256:",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("manifest page misses %q", want)
		}
	}
}

// TestContentNotFoundTaxonomized: unknown repositories and tags answer the
// taxonomized 404 (TBY-SRV-002), and a missing "/-/" separator never
// resolves a sub-resource.
func TestContentNotFoundTaxonomized(t *testing.T) {
	u := newTestUIWithStore(t, false, seedContentStore(t))
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	for _, target := range []string{
		"/content/ghost.example.com/nothing",
		"/content/docker.io/bitnami/wordpress/-/tags/9.9.9",
		"/content/docker.io/bitnami/wordpress/-/unknown/x",
	} {
		w := get(t, mux, c, target, nil)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "TBY-SRV-002") {
			t.Errorf("%s: code=%d, want taxonomized 404", target, w.Code)
		}
	}

	// Without "/-/", "tags" stays a legal repository path segment: the
	// route resolves as an (unknown) repository, never as a sub-resource.
	w := get(t, mux, c, "/content/docker.io/bitnami/wordpress/tags/6.4.2", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("path without /-/ = %d, want 404 as an unknown repository", w.Code)
	}
}

// TestContentUIAPIParity is the R-06/FR-061 invariant: the same q on the
// screen URL and on the API mirror selects the same repositories.
func TestContentUIAPIParity(t *testing.T) {
	st := seedContentStore(t)
	u := newTestUIWithStore(t, false, st)
	uiMux := mount(u)
	c := login(t, uiMux, "alexis", "pw-admin")

	restAPI := api.New(u.authn, slog.New(slog.DiscardHandler))
	api.RegisterContent(restAPI, st, nil)
	apiMux := http.NewServeMux()
	apiMux.Handle("/api/v1/", restAPI.Handler())

	const query = "?q=wordpress&kind=HelmChart"

	r := httptest.NewRequest(http.MethodGet, "/api/v1/content"+query, http.NoBody)
	r.SetBasicAuth("lecteur", "pw-view") // viewer suffices for reading
	wAPI := httptest.NewRecorder()
	apiMux.ServeHTTP(wAPI, r)
	if wAPI.Code != http.StatusOK {
		t.Fatalf("api = %d: %s", wAPI.Code, wAPI.Body.String())
	}
	var resp struct {
		Repositories []struct {
			Name string `json:"name"`
		} `json:"repositories"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(wAPI.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 || resp.Repositories[0].Name != "docker.io/bitnamicharts/wordpress" {
		t.Fatalf("api selection = %+v", resp)
	}

	wUI := get(t, uiMux, c, "/content"+query, nil)
	body := wUI.Body.String()
	for _, repo := range resp.Repositories {
		if !strings.Contains(body, ">"+repo.Name+"<") && !strings.Contains(body, `data-copy="`+repo.Name+`"`) {
			t.Errorf("UI misses API-selected repository %s", repo.Name)
		}
	}
	for _, absent := range []string{"docker.io/bitnami/wordpress", "registry.k8s.io/coredns/coredns"} {
		if strings.Contains(body, `data-copy="`+absent+`"`) {
			t.Errorf("UI shows %s, absent from the API selection", absent)
		}
	}
}

// TestContentEmptyStoreGatedByRole: the fresh-instance state offers the
// import CTA to operator+ and an explanation to a viewer — never a dead
// button (UI-SPEC §5.3).
func TestContentEmptyStoreGatedByRole(t *testing.T) {
	u := newTestUI(t, false) // empty store
	mux := mount(u)

	c := login(t, mux, "alexis", "pw-admin")
	w := get(t, mux, c, "/content", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `href="/import"`) {
		t.Errorf("admin empty state: code=%d, misses the import CTA", w.Code)
	}

	cv := login(t, mux, "lecteur", "pw-view")
	w = get(t, mux, cv, "/content", nil)
	body := w.Body.String()
	if strings.Contains(body, `href="/import"`) {
		t.Error("viewer empty state offers a dead import CTA")
	}
	if !strings.Contains(body, "operator") {
		t.Error("viewer empty state misses the role explanation")
	}
}

// TestDashboardStoreTile: the async tile serves the real counters, and the
// role of every count is the accessor's (FR-062).
func TestDashboardStoreTile(t *testing.T) {
	u := newTestUIWithStore(t, false, seedContentStore(t))
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := get(t, mux, c, "/?tile=store", map[string]string{"HX-Request": "true"})
	if w.Code != http.StatusOK {
		t.Fatalf("store tile = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "3 repositories") || !strings.Contains(body, "3 tags") {
		t.Errorf("store tile misses the real counters: %s", body)
	}
	if !strings.Contains(body, `id="tile-store"`) {
		t.Error("store tile misses its stable swap id")
	}
}
