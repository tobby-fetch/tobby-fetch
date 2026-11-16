// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The consumption aids screens (FR-065, 7.1): the exports of the mapping
// table and the K3s/RKE2 registries.yaml snippet, on the recipe mapping
// screen for one recipe and on /consumption for the whole store. The strict
// UI half of GET /api/v1/mapping, /api/v1/registries.yaml and their
// per-recipe forms (FR-061): same service, same bytes. Read-only, viewer.

package ui

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/consume"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Paths of the whole-store aids, on the "/-/" separator (ADR-0015 §3).
const (
	PathConsumptionExport     = "/consumption/-/export"
	PathConsumptionRegistries = "/consumption/-/registries.yaml"
)

// aidFormat is one export link.
type aidFormat struct {
	Format string
	Href   string
	// LabelKey is the localization key of the link.
	LabelKey string
}

// aidsView feeds the shared aids partial.
type aidsView struct {
	// Configured is false when no registry is known: the screen then states
	// why instead of offering links that would all fail.
	Configured bool
	Registry   string
	Formats    []aidFormat
	// Snippet is the registries.yaml text shown on the screen, and
	// SnippetHref its download.
	Snippet     string
	SnippetHref string
	// Failure is why the aids could not be generated although a registry is
	// configured (a record the configuration does not explain).
	Failure *ErrView
	// Rows is the table, set on /consumption only.
	Rows []consume.Row
}

// newAidsView builds the aids of the selected recipes. withRows adds the
// table itself.
func (u *UI) newAidsView(r *http.Request, sel []consume.Selector, exportPath, registriesPath string, withRows bool) *aidsView {
	v := &aidsView{SnippetHref: registriesPath}
	for _, f := range consume.Formats() {
		v.Formats = append(v.Formats, aidFormat{
			Format: string(f), Href: exportPath + "?format=" + string(f), LabelKey: "aids.export_" + string(f),
		})
	}
	if u.aids == nil || u.aids.Registry() == "" {
		return v
	}
	v.Configured = true
	v.Registry = u.aids.Registry()
	fail := func(err error) *aidsView {
		var te *taxonomy.Error
		if !errors.As(err, &te) {
			te = taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
		}
		v.Failure = errView(requestLang(r), te)
		return v
	}
	yaml, err := u.aids.RegistriesYAML(sel)
	if err != nil {
		return fail(err)
	}
	v.Snippet = string(yaml)
	if withRows {
		t, err := u.aids.Table(sel)
		if err != nil {
			return fail(err)
		}
		v.Rows = t.Entries
	}
	return v
}

// consumptionData feeds /consumption.
type consumptionData struct {
	Aids *aidsView
}

// consumptionScreen serves GET /consumption: the aids for every recipe.
func (u *UI) consumptionScreen(w http.ResponseWriter, r *http.Request) {
	u.render.Page(w, r, "consumption", &consumptionData{
		Aids: u.newAidsView(r, nil, PathConsumptionExport, PathConsumptionRegistries, true),
	})
}

// serveAid writes one generated aid, or its taxonomized refusal.
func (u *UI) serveAid(w http.ResponseWriter, r *http.Request, sel []consume.Selector, snippet bool, name string) {
	if u.aids == nil {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeAidsNoRegistry, nil))
		return
	}
	var (
		out         []byte
		err         error
		contentType string
	)
	if snippet {
		out, err = u.aids.RegistriesYAML(sel)
		contentType = consume.SnippetMediaType
		name += "-registries.yaml"
	} else {
		f, perr := consume.ParseFormat(r.URL.Query().Get("format"))
		if perr != nil {
			u.render.Error(w, r, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
				"file": "query", "path": "format", "constraint": "must be csv, json or markdown",
			}))
			return
		}
		var t *consume.Table
		if t, err = u.aids.Table(sel); err == nil {
			out, err = t.Render(f)
		}
		contentType = f.MediaType()
		name += "-mapping." + map[consume.Format]string{consume.FormatCSV: "csv", consume.FormatJSON: "json", consume.FormatMarkdown: "md"}[f]
	}
	if err != nil {
		var te *taxonomy.Error
		if !errors.As(err, &te) {
			te = taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
		}
		u.render.Error(w, r, te)
		return
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Disposition", `attachment; filename="`+aidFilename(name)+`"`)
	_, _ = w.Write(out) //nolint:gosec // G705: served as a download with its own media type, never as HTML
}

// aidFilename keeps a download name to characters no header or filesystem
// reads as anything but a name.
func aidFilename(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, s)
}

func (u *UI) consumptionExport(w http.ResponseWriter, r *http.Request) {
	u.serveAid(w, r, nil, false, "store")
}

func (u *UI) consumptionRegistries(w http.ResponseWriter, r *http.Request) {
	u.serveAid(w, r, nil, true, "store")
}

func (u *UI) recipeExport(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("recipe")
	u.serveAid(w, r, []consume.Selector{{Name: name}}, false, name)
}

func (u *UI) recipeRegistries(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("recipe")
	u.serveAid(w, r, []consume.Selector{{Name: name}}, true, name)
}

// recipeAidPaths are the export and snippet paths of one recipe.
func recipeAidPaths(name string) (export, registries string) {
	base := "/recipes/" + url.PathEscape(name) + "/mapping/-/"
	return base + "export", base + "registries.yaml"
}
