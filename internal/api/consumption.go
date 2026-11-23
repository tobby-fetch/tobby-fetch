// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Consumption aids (FR-065, 7.1): the mapping table in three formats and the
// K3s/RKE2 registries.yaml snippet, for one recipe or the whole store. The
// strict mirror of the recipe mapping screen's exports and of /consumption
// (FR-061). Read-only: viewer.

package api

import (
	"errors"
	"net/http"

	"github.com/tobby-fetch/tobby-fetch/internal/consume"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// aidsProblem answers a failed generation with its taxonomy entry.
func (rc *recipesAPI) aidsProblem(w http.ResponseWriter, r *http.Request, err error) {
	var te *taxonomy.Error
	if errors.As(err, &te) {
		rc.api.Problem(w, r, te)
		return
	}
	rc.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
}

// writeTable renders the table in the requested format. Callers have
// already checked the format.
func (rc *recipesAPI) writeTable(w http.ResponseWriter, r *http.Request, sel []consume.Selector, f consume.Format) {
	table, err := rc.opts.Aids.Table(sel)
	if err != nil {
		rc.aidsProblem(w, r, err)
		return
	}
	out, err := table.Render(f)
	if err != nil {
		rc.aidsProblem(w, r, err)
		return
	}
	w.Header().Set("Content-Type", f.MediaType())
	_, _ = w.Write(out)
}

// parseFormat reads ?format=, answering the validation problem itself.
func (rc *recipesAPI) parseFormat(w http.ResponseWriter, r *http.Request, def string) (consume.Format, bool) {
	v := r.URL.Query().Get("format")
	if v == "" {
		v = def
	}
	f, err := consume.ParseFormat(v)
	if err != nil {
		rc.api.Problem(w, r, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
			"file": "query", "path": "format", "constraint": "must be csv, json or markdown",
		}))
		return "", false
	}
	return f, true
}

// mappingAll serves GET /api/v1/mapping: the table of every recipe the
// store holds.
func (rc *recipesAPI) mappingAll(w http.ResponseWriter, r *http.Request) {
	f, ok := rc.parseFormat(w, r, "json")
	if !ok {
		return
	}
	rc.writeTable(w, r, nil, f)
}

// registriesYAML writes the snippet for the selected recipes.
func (rc *recipesAPI) registriesYAML(w http.ResponseWriter, r *http.Request, sel []consume.Selector) {
	out, err := rc.opts.Aids.RegistriesYAML(sel)
	if err != nil {
		rc.aidsProblem(w, r, err)
		return
	}
	w.Header().Set("Content-Type", consume.SnippetMediaType)
	_, _ = w.Write(out)
}

// registriesAll serves GET /api/v1/registries.yaml.
func (rc *recipesAPI) registriesAll(w http.ResponseWriter, r *http.Request) {
	rc.registriesYAML(w, r, nil)
}

// registriesRecipe serves GET /api/v1/recipes/{recipe}/registries.yaml,
// every recorded version of the recipe.
func (rc *recipesAPI) registriesRecipe(w http.ResponseWriter, r *http.Request) {
	rc.registriesYAML(w, r, []consume.Selector{{Name: r.PathValue("recipe")}})
}
