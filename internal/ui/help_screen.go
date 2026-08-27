// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The help surface (R-03 then R-05, UI-SPEC §5.10).
//
// /help is two things stacked on one page. Its head is the navigation of
// the embedded operations guides — the documentation of both modes and its
// troubleshooting half, carried inside the binary because the destination
// zone has no route to a website (NFR-003, amendment 2026-08-11). Its tail
// is the troubleshooting index, generated from the taxonomy catalog at
// request time: one anchored section per code, what/cause/action in the
// viewer's language, so every /help#<code> anchor emitted by error blocks,
// banners and RFC 9457 documents resolves against the catalog this binary
// actually carries (FR-065) rather than against a copy able to drift.
//
// The guides themselves live at /help/<section>/<page> and their
// screenshots at /help/-/assets/<name>.

package ui

import (
	"net/http"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/help"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// helpEntry is one catalog entry localized for the troubleshooting index.
type helpEntry struct {
	Code  string
	What  string
	Cause string
	// Action is the corrective action, executable with the means of the
	// current milestone (roadmap DoD).
	Action string
}

// helpData feeds the /help home: the guide navigation and the code index.
type helpData struct {
	Sections []help.NavSection
	Entries  []helpEntry
}

// helpPageData feeds one guide page.
type helpPageData struct {
	help.Rendered
	// Section is the identifier of the page's section, translated by the
	// template through the UI catalogs (FR-063) — no section label travels
	// in the corpus.
	Section string
}

// helpEntries renders the whole catalog in lang with generic placeholder
// parameters: each declared parameter renders as "<name>", so the message
// templates stay readable ("Credentials for <host> are missing or
// expired") without a live error instance.
func helpEntries(lang string) []helpEntry {
	all := taxonomy.All()
	out := make([]helpEntry, 0, len(all))
	for _, en := range all {
		params := make(taxonomy.Params, len(en.Params))
		for _, p := range en.Params {
			params[p] = "<" + p + ">"
		}
		m := taxonomy.Localize(lang, taxonomy.New(en.Code, params))
		out = append(out, helpEntry{
			Code: string(en.Code), What: m.What, Cause: m.Cause, Action: m.Action,
		})
	}
	return out
}

// helpScreen serves GET /help for every signed-in role.
func (u *UI) helpScreen(w http.ResponseWriter, r *http.Request) {
	lang := requestLang(r)
	u.render.Page(w, r, "help", &helpData{
		Sections: help.Load().Nav(lang),
		Entries:  helpEntries(lang),
	})
}

// helpPage serves GET /help/{page...}: one guide of the embedded corpus.
//
// The path is not a file name and never becomes one. It is looked up in
// the fixed key set of the embedded corpus, so "../", an absolute path or
// an encoded escape resolve to no page and get the taxonomized 404 like
// any other unknown address (NFR-011, UI-SPEC §5.13).
func (u *UI) helpPage(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSuffix(r.PathValue("page"), "/")
	lang := requestLang(r)
	page, ok := help.Load().Render(lang, key, helpLabels(u.render.view(r, nil)))
	if !ok {
		u.notFound(w, r)
		return
	}
	u.render.Page(w, r, "help-page", &helpPageData{Rendered: page, Section: page.Page.Section})
}

// helpAsset serves GET /help/-/assets/{name}: the screenshots the guides
// reference, embedded alongside them (NFR-003 — a guide whose illustrations
// live on a website is illustrated only where it is least needed).
//
// Same ETag revalidation as the rest of the embedded assets: no-cache plus
// a strong tag, so an upgraded binary can never be read through a stale
// picture.
func (u *UI) helpAsset(w http.ResponseWriter, r *http.Request) {
	body, etag, ok := help.Load().Asset(r.PathValue("name"))
	if !ok {
		u.notFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(body)
}

// helpLabels resolves the few chrome strings the corpus renderer needs
// from the UI catalogs. The renderer holds no French of its own: every
// visible string is a catalog key (FR-063, ADR-0015 §7).
func helpLabels(v *View) *help.Labels {
	version, updated := help.Load().StatusMeta()
	return &help.Labels{
		StatusUpdated:   v.T("help.status_updated", "Version", version, "Date", updated),
		StatusFeature:   v.T("help.status_feature"),
		StatusStatus:    v.T("help.status_status"),
		StatusMilestone: v.T("help.status_milestone"),
		StatusAvailable: v.T("help.status_available"),
		StatusPartial:   v.T("help.status_partial"),
		StatusUpcoming:  v.T("help.status_upcoming"),
	}
}
