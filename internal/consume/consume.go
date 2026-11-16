// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package consume is the vocabulary of the consumption aids (FR-065,
// amendment 2026-11-16; public feature 7.1, milestone 7): the
// source→destination mapping table, exportable, and the K3s/RKE2
// registries.yaml mirror snippet that redirects nominal image references
// to the zone registry.
//
// Both aids are computed from what the store holds (FR-026, FR-035), for
// one destination — destination.registry with destination.basePath, or
// consumption.registry for an instance that serves the zone itself (UC3)
// — so the table matches what is pushed. Neither ever carries a
// credential (NFR-015), neither carries a timestamp (same store, same
// configuration, same bytes), and Tobby never writes them to a node.
//
// Service builds both aids from the recorded recipe graph; the recipe
// mapping screen (/recipes/{recipe}/mapping), the API and the command line
// (`tobby consumption`) are three projections of it (FR-061).
package consume

import "fmt"

// Row is one line of the mapping table: one ingredient of one recipe, as
// it is named at its source and at the destination. Every export format
// carries exactly these columns, in this order.
type Row struct {
	Recipe        string `json:"recipe"`
	RecipeVersion string `json:"recipeVersion"`
	Ingredient    string `json:"ingredient"`
	// Kind is the declared ingredient kind (RECIPE-SPEC §7).
	Kind string `json:"kind"`
	// Source is the nominal reference the recipe names, canonical host
	// included, with the tag ("docker.io/bitnami/wordpress:6.5.2"), or the
	// digest when the ingredient has no tag.
	Source string `json:"source"`
	// Destination is the reference at the destination registry, per the
	// relocation convention (FR-035, RECIPE-SPEC §11.5):
	// "reg.zone.example/docker.io/bitnami/wordpress:6.5.2".
	Destination string `json:"destination"`
	Digest      string `json:"digest"`

	// Nominal and Relocated are the repositories behind Source and
	// Destination, without tag or registry host: what the registries.yaml
	// snippet is built from. Not exported as columns.
	Nominal   string `json:"-"`
	Relocated string `json:"-"`
	Tag       string `json:"-"`
}

// Format is an export format of the mapping table.
type Format string

// The three export formats of the mapping table.
const (
	FormatCSV      Format = "csv"
	FormatJSON     Format = "json"
	FormatMarkdown Format = "markdown"
)

// Formats lists the table formats, in the order the UI offers them.
func Formats() []Format { return []Format{FormatCSV, FormatJSON, FormatMarkdown} }

// ParseFormat reads a format name as the API and the CLI accept it
// ("?format=csv", "--format markdown").
func ParseFormat(s string) (Format, error) {
	for _, f := range Formats() {
		if string(f) == s {
			return f, nil
		}
	}
	return "", fmt.Errorf("unknown table format %q: valid formats are csv, json and markdown", s)
}

// MediaType is the Content-Type an export of the format is served with.
func (f Format) MediaType() string {
	switch f {
	case FormatCSV:
		return "text/csv; charset=utf-8"
	case FormatMarkdown:
		return "text/markdown; charset=utf-8"
	default:
		return "application/json"
	}
}

// SnippetMediaType is the Content-Type of the registries.yaml snippet.
const SnippetMediaType = "application/yaml; charset=utf-8"
