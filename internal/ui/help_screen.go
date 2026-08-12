// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Troubleshooting stub (R-03, UI-SPEC §5.10): /help is auto-generated from
// the taxonomy catalog — one anchored section per code, what/cause/action
// in the viewer's language — so every /help#<code> anchor emitted by error
// blocks, banners, and RFC 9457 documents resolves from day one. The full
// documentation of milestone 5 (R-05) inserts around these anchors without
// breaking them.

package ui

import (
	"net/http"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// helpEntry is one catalog entry localized for the stub.
type helpEntry struct {
	Code  string
	What  string
	Cause string
	// Action is the corrective action, executable with the means of the
	// current milestone (roadmap DoD).
	Action string
}

// helpData feeds the /help page.
type helpData struct {
	Entries []helpEntry
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
	u.render.Page(w, r, "help", &helpData{Entries: helpEntries(requestLang(r))})
}
