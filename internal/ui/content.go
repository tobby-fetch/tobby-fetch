// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Content browsing screens (FR-062, R-06): /content, the repository
// detail, and the manifest detail. URLs carry exactly the parameters of
// the /api/v1/content mirror (FR-061, ADR-0015 §1); the repository /
// sub-resource split uses the deterministic "/-/" separator (ADR-0015 §3).

package ui

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// storeErr wraps a failed store read as its taxonomy entry (R-03).
func storeErr(err error) *taxonomy.Error {
	return taxonomy.New(taxonomy.CodeStoreRead,
		taxonomy.Params{"detail": err.Error()}).WithCause(err)
}

// contentGroup is one source-host group of the listing (ADR-0013: the
// relocated path's first segment is the canonical source host).
type contentGroup struct {
	Host  string
	Repos []store.RepoSummary
	Size  int64
}

// contentListData feeds the /content page.
type contentListData struct {
	Query      store.BrowseQuery
	Groups     []contentGroup
	Total      int
	Page       int
	TotalPages int
	// StoreEmpty separates the fresh-instance state (role-gated CTA) from
	// "no result for these filters" (UI-SPEC §5.3).
	StoreEmpty         bool
	PrevHref, NextHref string
}

// KindOn reports whether k is among the active kind filters (checkbox
// state).
func (d *contentListData) KindOn(k store.Kind) bool {
	for _, on := range d.Query.Kinds {
		if on == k {
			return true
		}
	}
	return false
}

// AllKinds lists the five kind badges of the filter row.
func (*contentListData) AllKinds() []store.Kind { return store.Kinds() }

// contentList serves GET /content: repositories grouped by source host,
// searched and filtered server-side — never client-side (POC lesson).
func (u *UI) contentList(w http.ResponseWriter, r *http.Request) {
	query := store.ParseBrowseQuery(r.URL.Query())
	page, err := u.store.Browse(r.Context(), query)
	if err != nil {
		u.contentError(w, r, "content-list", &contentListData{Query: query}, storeErr(err))
		return
	}

	data := &contentListData{
		Query:      query,
		Total:      page.Total,
		Page:       page.Page,
		TotalPages: page.TotalPages,
		StoreEmpty: page.Total == 0 && !query.HasFilter(),
	}
	for _, repo := range page.Repos {
		host := repo.Name
		if i := strings.IndexByte(host, '/'); i > 0 {
			host = host[:i]
		}
		if n := len(data.Groups); n == 0 || data.Groups[n-1].Host != host {
			data.Groups = append(data.Groups, contentGroup{Host: host})
		}
		g := &data.Groups[len(data.Groups)-1]
		g.Repos = append(g.Repos, repo)
		g.Size += repo.Size
	}
	if page.Page > 1 {
		data.PrevHref = contentHref(query, page.Page-1)
	}
	if page.Page < page.TotalPages {
		data.NextHref = contentHref(query, page.Page+1)
	}

	// A non-boosted htmx request (the filter form) swaps only the results
	// region, keeping the form and its focus untouched; boosted navigation
	// and plain requests get the page (ADR-0015 §1: same canonical URL).
	if isFragment(r) {
		u.render.Fragment(w, r, "content-list", "content-results", data)
		return
	}
	// The removal redirect (FR-044 amendment) lands here with ?deleted=…:
	// a success toast on the full page, display-only — the parameter is
	// not part of the FR-061 filter contract and never reaches the store.
	if del := r.URL.Query().Get("deleted"); del != "" {
		v := u.render.view(r, data)
		v.Toasts = append(v.Toasts, v.T("repo.deleted_toast", "Repo", del))
		u.render.render(w, r, "content-list", http.StatusOK, v)
		return
	}
	u.render.Page(w, r, "content-list", data)
}

// contentHref rebuilds the canonical listing URL for a page — the same
// parameter set the API mirror accepts (FR-061).
func contentHref(q store.BrowseQuery, page int) string {
	q.Page = page
	if enc := q.Values().Encode(); enc != "" {
		return "/content?" + enc
	}
	return "/content"
}

// contentDetail routes GET /content/{repo...}: the repository page, or the
// manifest page after the mandatory "/-/" separator (ADR-0015 §3 — "tags"
// is a legal repository segment, a lone "-" is not).
func (u *UI) contentDetail(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("repo")
	repo, sub, found := strings.Cut(rest, "/-/")
	if !found {
		u.contentRepo(w, r, rest)
		return
	}
	tag, ok := strings.CutPrefix(sub, "tags/")
	if !ok || tag == "" || strings.Contains(tag, "/") || repo == "" {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeNotFound, nil))
		return
	}
	u.contentManifest(w, r, repo, tag)
}

// crumb is one breadcrumb segment; the last one has no Href (UI-SPEC §7).
type crumb struct {
	Label string
	Href  string
}

// crumbs builds the navigable breadcrumb of a repository path: every
// intermediate segment targets /content?prefix=… — the same parameter the
// API accepts (FR-061) — and the last segment is never a link. On a
// manifest page (last = the tag) the repository's own segment links to the
// repository detail.
func crumbs(repo, last string) []crumb {
	segs := strings.Split(repo, "/")
	out := make([]crumb, 0, len(segs)+1)
	for i, seg := range segs {
		prefix := strings.Join(segs[:i+1], "/")
		out = append(out, crumb{Label: seg, Href: "/content?prefix=" + url.QueryEscape(prefix)})
	}
	if last == "" {
		out[len(out)-1].Href = ""
	} else {
		out[len(out)-1].Href = "/content/" + repo
		out = append(out, crumb{Label: last})
	}
	return out
}

// contentRepoData feeds the repository detail page.
type contentRepoData struct {
	Name        string
	Kind        store.Kind
	Tags        []store.TagInfo
	Size        int64
	Crumbs      []crumb
	PullCommand string
	// Provenance zone (FR-044 amendment, UI-SPEC §5.4): the recorded class
	// decides removability — unit-import only. ManagedBy names the managing
	// recipes so the disabled action explains itself, never hides.
	ProvClass store.ProvenanceClass
	ManagedBy []string
}

// CanDelete reports whether the removal action is live: unit-import
// provenance and no managing recipe (FR-044 amendment). The admin role
// gate is the template's IsAdmin.
func (d *contentRepoData) CanDelete() bool {
	return d.ProvClass == store.ProvenanceUnitImport && len(d.ManagedBy) == 0
}

// ManagedByList renders the managing recipes for the disabled-action
// explanation ("managed by X — it goes away by removing the recipe").
func (d *contentRepoData) ManagedByList() string {
	return strings.Join(d.ManagedBy, ", ")
}

// repoProvenance resolves the displayed provenance of one repository: the
// live recipe graph wins (a repo listed among any recipe's ingredients is
// recipe-managed), then the recorded ledger class, then seed — content
// pushed through /v2/ by a standard client leaves no record.
func (u *UI) repoProvenance(name string) (class store.ProvenanceClass, managedBy []string) {
	managed := u.store.ManagingRecipes(name)
	if len(managed) > 0 {
		return store.ProvenanceRecipe, managed
	}
	if p, ok := u.store.ProvenanceOf(name); ok {
		if p.Class == store.ProvenanceRecipe && p.Recipe != "" {
			return p.Class, []string{p.Recipe + "@" + p.RecipeVersion}
		}
		return p.Class, nil
	}
	return store.ProvenanceSeed, nil
}

// contentRepo serves the repository detail: navigable breadcrumb, kind
// badge, contextualized pull command on the request host, tag table
// (UI-SPEC §5.4).
func (u *UI) contentRepo(w http.ResponseWriter, r *http.Request, name string) {
	info, err := u.store.RepoInfo(r.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			u.render.Error(w, r, taxonomy.New(taxonomy.CodeNotFound, nil))
			return
		}
		u.contentError(w, r, "content-repo", &contentRepoData{Name: name, Crumbs: crumbs(name, "")}, storeErr(err))
		return
	}
	data := contentRepoData{
		Name:   name,
		Kind:   info.Kind,
		Tags:   info.Tags,
		Size:   info.Size,
		Crumbs: crumbs(name, ""),
	}
	data.ProvClass, data.ManagedBy = u.repoProvenance(name)
	if len(info.Tags) > 0 {
		data.PullCommand = pullByTag(info.Kind, r.Host, name, info.Tags[0].Tag)
	}
	u.render.Page(w, r, "content-repo", &data)
}

// contentPlatform decorates a store platform for the platform table.
type contentPlatform struct {
	store.PlatformInfo
	// Label is "os/arch[/variant]", empty for platform-less artifacts.
	Label string
	// ImportHref deep-links the import screen pre-filled on this platform
	// (UI-SPEC §5.5; the screen itself lands in another lot).
	ImportHref string
}

// platformLabel renders "os/arch[/variant]" for display.
func platformLabel(p *store.PlatformInfo) string {
	if p.OS == "" && p.Arch == "" {
		return ""
	}
	s := p.OS + "/" + p.Arch
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// contentManifestData feeds the manifest detail page.
type contentManifestData struct {
	Repo        string
	ShortName   string
	Tag         string
	Digest      string
	MediaType   string
	Kind        store.Kind
	IsIndex     bool
	Size        int64
	Annotations map[string]string
	Platforms   []contentPlatform
	Crumbs      []crumb
	PullCommand string
}

// PresentCount counts the locally held platforms of the detail — shown
// next to the total, never the total alone (B-007). The page renders the
// data by pointer so the template sees this method.
func (d *contentManifestData) PresentCount() int {
	n := 0
	for i := range d.Platforms {
		if d.Platforms[i].Present {
			n++
		}
	}
	return n
}

// contentManifest serves the manifest detail: pinned index digest,
// platform table with local presence (sparse index, FR-022), OCI
// annotations, pull-by-digest command (UI-SPEC §5.5).
func (u *UI) contentManifest(w http.ResponseWriter, r *http.Request, name, tag string) {
	info, err := u.store.ManifestInfo(r.Context(), name, tag)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			u.render.Error(w, r, taxonomy.New(taxonomy.CodeNotFound, nil))
			return
		}
		u.contentError(w, r, "content-manifest",
			contentManifestData{Repo: name, Tag: tag, Crumbs: crumbs(name, tag)}, storeErr(err))
		return
	}
	short := name
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		short = name[i+1:]
	}
	data := contentManifestData{
		Repo:        name,
		ShortName:   short,
		Tag:         tag,
		Digest:      info.Digest,
		MediaType:   info.MediaType,
		Kind:        info.Kind,
		IsIndex:     info.IsIndex,
		Size:        info.Size,
		Annotations: info.Annotations,
		Crumbs:      crumbs(name, tag),
		PullCommand: pullByDigest(info.Kind, r.Host, name, info.Digest, tag),
	}
	for _, p := range info.Platforms {
		cp := contentPlatform{PlatformInfo: p, Label: platformLabel(&p)}
		if !p.Present {
			v := url.Values{"ref": {name + ":" + tag}}
			if cp.Label != "" {
				v.Set("platform", strings.ReplaceAll(cp.Label, "/", "-"))
			}
			cp.ImportHref = "/import?" + v.Encode()
		}
		data.Platforms = append(data.Platforms, cp)
	}
	u.render.Page(w, r, "content-manifest", &data)
}

// pullByTag builds the copyable pull command of a repository page,
// contextualized on the request host and the artifact kind (UI-SPEC §5.4:
// docker for images, helm for charts, oras otherwise — same accounts as
// the UI, FR-076).
func pullByTag(kind store.Kind, host, repo, tag string) string {
	switch kind {
	case store.KindHelmChart:
		return "helm pull oci://" + host + "/" + repo + " --version " + tag
	case store.KindContainerImage:
		return "docker pull " + host + "/" + repo + ":" + tag
	default:
		return "oras pull " + host + "/" + repo + ":" + tag
	}
}

// pullByDigest builds the manifest page's pull command. Charts pull by
// version — helm has no digest form — images and artifacts pin the digest.
func pullByDigest(kind store.Kind, host, repo, dgst, tag string) string {
	switch kind {
	case store.KindHelmChart:
		return pullByTag(kind, host, repo, tag)
	case store.KindContainerImage:
		return "docker pull " + host + "/" + repo + "@" + dgst
	default:
		return "oras pull " + host + "/" + repo + "@" + dgst
	}
}

// contentDelete serves POST /content/{repo...}/-/delete (FR-044
// amendment, UI-SPEC §5.4): remove one unit-imported repository and
// garbage-collect. Runs behind the admin gate and the session CSRF check.
// Policy first: recipe-managed content answers TBY-POL-002 naming the
// managing recipes, seeded content TBY-POL-003 — removal covers
// unit-import provenance only. Audited (FR-094), then redirect to the
// listing with a confirmation toast.
func (u *UI) contentDelete(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("repo")
	repo, ok := strings.CutSuffix(rest, "/-/delete")
	if !ok || repo == "" {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeNotFound, nil))
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	denied := func(e *taxonomy.Error) {
		audit.Log(r.Context(), u.logger, &audit.Event{
			Actor: id.Name, Action: audit.ActionContentDelete, Target: repo,
			Outcome: audit.OutcomeDenied, Origin: auth.ClientOrigin(r),
		})
		u.render.Error(w, r, e)
	}
	class, managed := u.repoProvenance(repo)
	if class != store.ProvenanceUnitImport {
		// A policy answer must not leak on an unknown path: 404 first.
		if _, err := u.store.RepoInfo(r.Context(), repo); errors.Is(err, store.ErrNotFound) {
			u.render.Error(w, r, taxonomy.New(taxonomy.CodeNotFound, nil))
			return
		}
		if class == store.ProvenanceRecipe {
			denied(taxonomy.New(taxonomy.CodeRecipeManaged,
				taxonomy.Params{"repository": repo, "recipes": strings.Join(managed, ", ")}))
			return
		}
		// Seeded (or unrecorded) content: pushed through /v2/, outside the
		// FR-044 amendment's removal scope.
		denied(taxonomy.New(taxonomy.CodeSeedContent, taxonomy.Params{"repository": repo}))
		return
	}
	if err := u.store.DeleteRepository(r.Context(), repo, u.logger); err != nil {
		var rme *store.RecipeManagedError
		if errors.As(err, &rme) {
			// The store's own guard raced a fresh sync: same policy answer.
			denied(taxonomy.New(taxonomy.CodeRecipeManaged,
				taxonomy.Params{"repository": repo, "recipes": strings.Join(rme.Recipes, ", ")}))
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			u.render.Error(w, r, taxonomy.New(taxonomy.CodeNotFound, nil))
			return
		}
		audit.Log(r.Context(), u.logger, &audit.Event{
			Actor: id.Name, Action: audit.ActionContentDelete, Target: repo,
			Outcome: audit.OutcomeFailure, Origin: auth.ClientOrigin(r),
		})
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeStoreWrite,
			taxonomy.Params{"detail": err.Error()}).WithCause(err))
		return
	}
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: id.Name, Action: audit.ActionContentDelete, Target: repo,
		Outcome: audit.OutcomeSuccess, Origin: auth.ClientOrigin(r),
	})
	u.redirectTo(w, r, "/content?deleted="+url.QueryEscape(repo))
}

// contentError renders a screen in its store-error state: the taxonomized
// block inside the page shell, with the entry's real HTTP status; an htmx
// fragment gets the inline block alone (ADR-0015 §6).
func (u *UI) contentError(w http.ResponseWriter, r *http.Request, page string, data any, e *taxonomy.Error) {
	if isFragment(r) {
		u.render.Error(w, r, e)
		return
	}
	v := u.render.view(r, data)
	v.Err = errView(v.Lang, e)
	u.render.render(w, r, page, v.Err.Status, v)
}
