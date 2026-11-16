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
// This package carries the shapes only, reserved at the opening of the
// milestone. Building the rows from the store, rendering the three table
// formats and the snippet, and exposing them on the recipe mapping screen
// (/recipes/{recipe}/mapping), the API and the command line are track A.
package consume

import "fmt"

// Row is one line of the mapping table: one ingredient of one recipe, as
// it is named at its source and at the destination.
type Row struct {
	Recipe     string `json:"recipe"`
	Ingredient string `json:"ingredient"`
	// Kind is the declared ingredient kind (RECIPE-SPEC §7).
	Kind string `json:"kind"`
	// Nominal is the reference the recipe names, host and repository
	// ("docker.io/bitnami/wordpress").
	Nominal string `json:"nominal"`
	// Relocated is the repository at the destination, per the relocation
	// convention (FR-035, RECIPE-SPEC §11.5).
	Relocated string `json:"relocated"`
	Tag       string `json:"tag"`
	Digest    string `json:"digest"`
}

// Destination is what the aids point at.
type Destination struct {
	// Registry is the bare host the zone's clients pull from.
	Registry string
	// BasePath is the optional relocation prefix at the destination.
	BasePath string
	// CAFile is the CA path on the cluster nodes, written as-is into the
	// snippet (consumption.caFile); empty writes no tls block.
	CAFile string
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
