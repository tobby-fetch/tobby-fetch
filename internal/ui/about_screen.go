// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// About screen (UI-SPEC §5.11): build identity (version, commit, date),
// the read-only operating mode, the exposed machine surfaces including the
// server.ReservedPrefixes contract (ADR-0015 §2), the milestone roadmap
// (announced here, never as disabled navigation entries), and the license
// with the embedded THIRD-PARTY-NOTICES served as plain text (ADR-0010).

package ui

import (
	"net/http"

	"github.com/tobby-fetch/tobby-fetch/internal/buildinfo"
	"github.com/tobby-fetch/tobby-fetch/internal/server"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// aboutData feeds the /about page. The endpoint table and the roadmap are
// static template content (i18n keys); only the build identity and the
// reserved-prefix contract travel as data.
type aboutData struct {
	Version  string
	Commit   string
	Date     string
	Prefixes []string
}

// aboutScreen serves GET /about for every signed-in role. The version is
// the one injected at assembly (the same value the shell shows); commit
// and date come straight from the stamped build metadata.
func (u *UI) aboutScreen(w http.ResponseWriter, r *http.Request) {
	u.render.Page(w, r, "about", &aboutData{
		Version:  u.render.Version,
		Commit:   buildinfo.Commit(),
		Date:     buildinfo.Date(),
		Prefixes: server.ReservedPrefixes,
	})
}

// thirdPartyNotices serves GET /about/third-party: the THIRD-PARTY-NOTICES
// file embedded with the vendored assets (ADR-0010), as plain text.
func (u *UI) thirdPartyNotices(w http.ResponseWriter, r *http.Request) {
	raw, err := staticFS.ReadFile("static/thirdparty/THIRD-PARTY-NOTICES")
	if err != nil {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(raw)
}
