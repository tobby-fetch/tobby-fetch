// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package help

import (
	"fmt"
	"html"
	"strconv"
	"strings"
)

// The feature-status table. website/README-docs.md makes status.yaml the
// single source of truth for every badge on the site, and forbids a second
// hand-maintained table; the corpus therefore carries the data file itself
// and renders it here, the way the site's own component does. A second
// table transcribed into Markdown would be exactly the drift that rule
// exists to prevent.

// statusData mirrors website/src/data/status.yaml.
type statusData struct {
	Updated  string          `yaml:"updated"`
	Version  string          `yaml:"version"`
	Features []statusFeature `yaml:"features"`
}

// statusFeature is one row: the label is bilingual in the data file, so
// the table needs no translation pass of its own.
type statusFeature struct {
	ID        string `yaml:"id"`
	EN        string `yaml:"en"`
	FR        string `yaml:"fr"`
	Status    string `yaml:"status"`
	Milestone int    `yaml:"milestone"`
	Security  bool   `yaml:"security"`
	Note      struct {
		EN string `yaml:"en"`
		FR string `yaml:"fr"`
	} `yaml:"note"`
}

// label returns the row's label in lang, falling back to English the way
// the data file's own convention does.
func (f *statusFeature) label(lang string) string {
	if lang == "fr" && f.FR != "" {
		return f.FR
	}
	return f.EN
}

func (f *statusFeature) note(lang string) string {
	if lang == "fr" {
		return f.Note.FR
	}
	return f.Note.EN
}

// Labels carries the chrome strings the renderer needs. They are resolved
// by the caller from the UI catalogs rather than held here: every visible
// string is a catalog key (FR-063, ADR-0015 §7), and this package must not
// become a second place where French lives.
type Labels struct {
	// StatusUpdated dates the table ("Status as of v0.4.2 (2026-08-25)").
	StatusUpdated string
	// Column headers.
	StatusFeature   string
	StatusStatus    string
	StatusMilestone string
	// Status values, keyed by the vocabulary of status.yaml.
	StatusAvailable string
	StatusPartial   string
	StatusUpcoming  string
}

// StatusMeta returns the version and date the embedded status data was
// written for — the two values the caller needs to build StatusUpdated
// from its own catalog.
func (c *Corpus) StatusMeta() (version, updated string) {
	return c.status.Version, c.status.Updated
}

// statusLabel maps a status.yaml value onto its localized word.
func (r *renderer) statusLabel(status string) string {
	switch status {
	case "available":
		return r.labels.StatusAvailable
	case "partial":
		return r.labels.StatusPartial
	case "upcoming":
		return r.labels.StatusUpcoming
	}
	return status
}

// statusTable renders the feature table, optionally filtered to the rows
// the security one-pager shows.
func (r *renderer) statusTable(securityOnly bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<p class="t-doc-status-updated">%s</p>`, html.EscapeString(r.labels.StatusUpdated))
	b.WriteString(`<div class="t-doc-tablewrap"><table class="t-table t-doc-table"><thead><tr>`)
	for _, h := range []string{r.labels.StatusFeature, r.labels.StatusStatus, r.labels.StatusMilestone} {
		fmt.Fprintf(&b, "<th>%s</th>", html.EscapeString(h))
	}
	b.WriteString("</tr></thead><tbody>")
	for i := range r.corpus.status.Features {
		f := &r.corpus.status.Features[i]
		if securityOnly && !f.Security {
			continue
		}
		b.WriteString(`<tr><td><span class="t-doc-status-id" translate="no">`)
		b.WriteString(html.EscapeString(f.ID))
		b.WriteString(`</span> `)
		b.WriteString(html.EscapeString(f.label(r.lang)))
		if note := f.note(r.lang); note != "" {
			fmt.Fprintf(&b, `<span class="t-doc-status-note">%s</span>`, html.EscapeString(note))
		}
		fmt.Fprintf(&b, `</td><td><span class="t-badge t-badge--status-%s">%s</span></td>`,
			html.EscapeString(f.Status), html.EscapeString(r.statusLabel(f.Status)))
		milestone := "—"
		if f.Milestone > 0 {
			milestone = "J" + strconv.Itoa(f.Milestone)
		}
		fmt.Fprintf(&b, `<td class="t-doc-status-milestone">%s</td></tr>`, milestone)
	}
	b.WriteString("</tbody></table></div>\n")
	return b.String()
}
