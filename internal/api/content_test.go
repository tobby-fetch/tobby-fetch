// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// newContentAPI seeds a real store through the OCI wire protocol and
// mounts the content endpoints behind real Basic authentication (FR-076):
// a viewer and an operator account exist.
func newContentAPI(t *testing.T) (*http.ServeMux, v1.Hash) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing store: %v", err)
		}
	})
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
	idx = mutate.Annotations(idx, map[string]string{"org.example.note": "seeded"}).(v1.ImageIndex)
	digest, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
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
	chartRef, err := name.ParseReference(addr + "/docker.io/bitnamicharts/wordpress:1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(chartRef,
		mutate.ConfigMediaType(chart, "application/vnd.cncf.helm.config.v1+json")); err != nil {
		t.Fatal(err)
	}

	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("lecteur", auth.RoleViewer, "pw-view", time.Now()); err != nil {
		t.Fatal(err)
	}
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(time.Hour),
		Logger:   slog.New(slog.DiscardHandler),
	}
	a := api.New(authn, slog.New(slog.DiscardHandler))
	api.RegisterContent(a, st)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())
	return mux, digest
}

// apiGet performs a Basic-authenticated GET as the viewer account (reads
// need viewer, ADR-0009).
func apiGet(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	r.SetBasicAuth("lecteur", "pw-view")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestContentListEndpoint covers GET /api/v1/content: raw bytes only
// (ADR-0015 §7), the UI's exact q/kind/prefix/page parameters (FR-061),
// pagination envelope.
func TestContentListEndpoint(t *testing.T) {
	mux, _ := newContentAPI(t)

	w := apiGet(t, mux, "/api/v1/content")
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Repositories []struct {
			Name      string `json:"name"`
			Kind      string `json:"kind"`
			Tags      int    `json:"tags"`
			SizeBytes int64  `json:"sizeBytes"`
		} `json:"repositories"`
		Page       int `json:"page"`
		TotalPages int `json:"totalPages"`
		Total      int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 2 || resp.Page != 1 || resp.TotalPages != 1 || len(resp.Repositories) != 2 {
		t.Fatalf("envelope = %+v", resp)
	}
	for _, repo := range resp.Repositories {
		if repo.SizeBytes <= 0 || repo.Tags != 1 {
			t.Errorf("repository %s: sizeBytes=%d tags=%d", repo.Name, repo.SizeBytes, repo.Tags)
		}
	}
	if resp.Repositories[0].Kind != "ContainerImage" || resp.Repositories[1].Kind != "HelmChart" {
		t.Errorf("kinds = %s, %s", resp.Repositories[0].Kind, resp.Repositories[1].Kind)
	}

	// Filters: same parameters as the screen (R-06).
	w = apiGet(t, mux, "/api/v1/content?kind=HelmChart&q=wordpress")
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 || resp.Repositories[0].Name != "docker.io/bitnamicharts/wordpress" {
		t.Errorf("filtered = %+v", resp)
	}

	// Out-of-range page mirrors the UI: empty list, intact envelope.
	w = apiGet(t, mux, "/api/v1/content?page=9")
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Repositories) != 0 || resp.Total != 2 {
		t.Errorf("page=9 = %+v", resp)
	}
}

// TestContentDetailEndpoints covers the repository and manifest details,
// with the mandatory "/-/" separator (ADR-0015 §3).
func TestContentDetailEndpoints(t *testing.T) {
	mux, digest := newContentAPI(t)

	w := apiGet(t, mux, "/api/v1/content/docker.io/bitnami/wordpress")
	if w.Code != http.StatusOK {
		t.Fatalf("repo detail = %d: %s", w.Code, w.Body.String())
	}
	var repo struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
		Tags []struct {
			Tag       string `json:"tag"`
			Digest    string `json:"digest"`
			SizeBytes int64  `json:"sizeBytes"`
			Platforms int    `json:"platforms"`
		} `json:"tags"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &repo); err != nil {
		t.Fatal(err)
	}
	if repo.Name != "docker.io/bitnami/wordpress" || repo.Kind != "ContainerImage" {
		t.Errorf("repo = %+v", repo)
	}
	if len(repo.Tags) != 1 || repo.Tags[0].Digest != digest.String() ||
		repo.Tags[0].Platforms != 2 || repo.Tags[0].SizeBytes <= 0 {
		t.Errorf("tags = %+v", repo.Tags)
	}

	w = apiGet(t, mux, "/api/v1/content/docker.io/bitnami/wordpress/-/tags/6.4.2")
	if w.Code != http.StatusOK {
		t.Fatalf("manifest detail = %d: %s", w.Code, w.Body.String())
	}
	var man struct {
		Digest      string            `json:"digest"`
		MediaType   string            `json:"mediaType"`
		Kind        string            `json:"kind"`
		SizeBytes   int64             `json:"sizeBytes"`
		Annotations map[string]string `json:"annotations"`
		Platforms   []struct {
			OS        string `json:"os"`
			Arch      string `json:"arch"`
			Digest    string `json:"digest"`
			SizeBytes int64  `json:"sizeBytes"`
			Present   bool   `json:"present"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &man); err != nil {
		t.Fatal(err)
	}
	if man.Digest != digest.String() || man.MediaType != string(types.OCIImageIndex) || man.SizeBytes <= 0 {
		t.Errorf("manifest = %+v", man)
	}
	if man.Annotations["org.example.note"] != "seeded" {
		t.Error("annotations missing")
	}
	if len(man.Platforms) != 2 {
		t.Fatalf("platforms = %+v", man.Platforms)
	}
	for _, p := range man.Platforms {
		if p.OS != "linux" || !p.Present || p.SizeBytes <= 0 {
			t.Errorf("platform = %+v", p)
		}
	}
}

// TestContentProblemDocuments: unknown resources answer the same taxonomy
// entry as the UI (TBY-SRV-002) as RFC 9457, never HTML (FR-061).
func TestContentProblemDocuments(t *testing.T) {
	mux, _ := newContentAPI(t)

	for _, target := range []string{
		"/api/v1/content/ghost.example.com/nothing",
		"/api/v1/content/docker.io/bitnami/wordpress/-/tags/9.9.9",
		"/api/v1/content/docker.io/bitnami/wordpress/-/nope/x",
	} {
		w := apiGet(t, mux, target)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", target, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
			t.Errorf("%s content-type = %s", target, ct)
		}
		if !strings.Contains(w.Body.String(), "TBY-SRV-002") {
			t.Errorf("%s body misses the taxonomy code", target)
		}
	}

	// Unauthenticated requests never reach the store.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/content", http.NoBody)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d, want 401", w.Code)
	}
}
