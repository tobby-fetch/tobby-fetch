// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Content browsing endpoints (FR-060): the strict mirror of the /content
// screens (FR-061, R-06). Query parameters are the exact UI parameters —
// both surfaces parse them with store.ParseBrowseQuery — and sub-resources
// use the same "/-/" separator (ADR-0015 §3). Payloads carry raw bytes
// only; localization is a UI concern (ADR-0015 §7).

package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// RegisterContent mounts the content endpoints on the API surface. Reading
// the store needs the viewer role (ADR-0009).
func RegisterContent(a *API, st *store.Store) {
	c := &contentAPI{api: a, store: st}
	a.Handle("GET /api/v1/content", a.RequireRole(auth.RoleViewer, c.list))
	a.Handle("GET /api/v1/content/{repo...}", a.RequireRole(auth.RoleViewer, c.detail))
}

type contentAPI struct {
	api   *API
	store *store.Store
}

// contentRepoItem is one row of the repository listing.
type contentRepoItem struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Tags      int    `json:"tags"`
	SizeBytes int64  `json:"sizeBytes"`
}

// contentListResponse mirrors the /content listing page.
type contentListResponse struct {
	Repositories []contentRepoItem `json:"repositories"`
	Page         int               `json:"page"`
	TotalPages   int               `json:"totalPages"`
	Total        int               `json:"total"`
}

// list serves GET /api/v1/content?q=&kind=&prefix=&page= (FR-061: the
// exact parameters of the /content screen).
func (c *contentAPI) list(w http.ResponseWriter, r *http.Request) {
	query := store.ParseBrowseQuery(r.URL.Query())
	page, err := c.store.Browse(r.Context(), query)
	if err != nil {
		c.problem(w, r, err)
		return
	}
	resp := contentListResponse{
		Repositories: make([]contentRepoItem, 0, len(page.Repos)),
		Page:         page.Page,
		TotalPages:   page.TotalPages,
		Total:        page.Total,
	}
	for _, repo := range page.Repos {
		resp.Repositories = append(resp.Repositories, contentRepoItem{
			Name: repo.Name, Kind: string(repo.Kind), Tags: repo.Tags, SizeBytes: repo.Size,
		})
	}
	c.api.JSON(w, http.StatusOK, resp)
}

// contentTag is one tag of the repository detail.
type contentTag struct {
	Tag       string `json:"tag"`
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"sizeBytes"`
	Platforms int    `json:"platforms"`
}

// contentRepoResponse mirrors the repository detail page.
type contentRepoResponse struct {
	Name string       `json:"name"`
	Kind string       `json:"kind"`
	Tags []contentTag `json:"tags"`
}

// contentPlatform is one platform entry of the manifest detail; Present is
// false for a sparse-index entry absent from the store (FR-022).
type contentPlatform struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Variant   string `json:"variant,omitempty"`
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"sizeBytes"`
	Present   bool   `json:"present"`
}

// contentManifestResponse mirrors the manifest detail page.
type contentManifestResponse struct {
	Repo        string            `json:"repo"`
	Tag         string            `json:"tag,omitempty"`
	Digest      string            `json:"digest"`
	MediaType   string            `json:"mediaType"`
	Kind        string            `json:"kind"`
	SizeBytes   int64             `json:"sizeBytes"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Platforms   []contentPlatform `json:"platforms"`
}

// detail serves GET /api/v1/content/{repo...} and, after the mandatory
// "/-/" separator (ADR-0015 §3), the manifest detail
// GET /api/v1/content/{repo...}/-/tags/{tag}.
func (c *contentAPI) detail(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("repo")
	repo, sub, found := strings.Cut(rest, "/-/")
	if !found {
		c.repoDetail(w, r, rest)
		return
	}
	tag, ok := strings.CutPrefix(sub, "tags/")
	if !ok || tag == "" || strings.Contains(tag, "/") || repo == "" {
		c.api.Problem(w, r, taxonomy.New(taxonomy.CodeNotFound, nil))
		return
	}
	c.manifestDetail(w, r, repo, tag)
}

func (c *contentAPI) repoDetail(w http.ResponseWriter, r *http.Request, name string) {
	info, err := c.store.RepoInfo(r.Context(), name)
	if err != nil {
		c.problem(w, r, err)
		return
	}
	resp := contentRepoResponse{
		Name: info.Name,
		Kind: string(info.Kind),
		Tags: make([]contentTag, 0, len(info.Tags)),
	}
	for _, tag := range info.Tags {
		resp.Tags = append(resp.Tags, contentTag{
			Tag: tag.Tag, Digest: tag.Digest, SizeBytes: tag.Size, Platforms: tag.Platforms,
		})
	}
	c.api.JSON(w, http.StatusOK, resp)
}

func (c *contentAPI) manifestDetail(w http.ResponseWriter, r *http.Request, name, tag string) {
	info, err := c.store.ManifestInfo(r.Context(), name, tag)
	if err != nil {
		c.problem(w, r, err)
		return
	}
	resp := contentManifestResponse{
		Repo:        info.Repo,
		Tag:         info.Tag,
		Digest:      info.Digest,
		MediaType:   info.MediaType,
		Kind:        string(info.Kind),
		SizeBytes:   info.Size,
		Annotations: info.Annotations,
		Platforms:   make([]contentPlatform, 0, len(info.Platforms)),
	}
	for _, p := range info.Platforms {
		resp.Platforms = append(resp.Platforms, contentPlatform{
			OS: p.OS, Arch: p.Arch, Variant: p.Variant,
			Digest: p.Digest, SizeBytes: p.Size, Present: p.Present,
		})
	}
	c.api.JSON(w, http.StatusOK, resp)
}

// problem maps an accessor error onto its taxonomy entry: ErrNotFound is
// the shared 404 (TBY-SRV-002, same entry as the UI — FR-061), everything
// else a store read failure (TBY-STO-001).
func (c *contentAPI) problem(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		c.api.Problem(w, r, taxonomy.New(taxonomy.CodeNotFound, nil))
		return
	}
	c.api.Problem(w, r, taxonomy.New(taxonomy.CodeStoreRead,
		taxonomy.Params{"detail": err.Error()}).WithCause(err))
}
