// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"net/http"

	"github.com/tobby-fetch/tobby-fetch/internal/slip"
)

// mediaSlip serves GET /media/slip: the transfer slip (R-07), the Media
// screen's content as a printable document. By default it is the
// self-contained HTML page, opened in its own tab for the browser's print
// dialog; ?format=text is the same document in plain text, ?download=1
// saves either instead of showing it, and ?lang= overrides the interface
// language, so a slip can be handed to a person who does not read it.
//
// It is the mirror of GET /api/v1/media/slip (FR-061) and draws through the
// same slip.Instance, so the two cannot tell different stories. It hashes
// nothing and runs no verification: the verdict is the last one reached.
//
// The page is NOT wrapped in the interface's shell. It is a document that
// leaves the screen — printed, saved, mailed inside a zone — and must read
// the same anywhere, so it carries its own stylesheet and its own policy
// and loads nothing. The unsigned-aid notice opens and closes it
// (ADR-0006, decision 10).
func (u *UI) mediaSlip(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	switch format {
	case "":
		format = slip.FormatHTML
	case slip.FormatHTML, slip.FormatText:
	default:
		http.Error(w, "format must be html or text", http.StatusBadRequest)
		return
	}
	lang := requestLang(r)
	switch q.Get("lang") {
	case "":
	case "en", "fr":
		lang = q.Get("lang")
	default:
		http.Error(w, "lang must be en or fr", http.StatusBadRequest)
		return
	}

	sl := u.slipInstance().Slip()
	body, contentType, err := sl.Render(format, lang)
	if err != nil {
		u.logger.Error("rendering the transfer slip", "error", err)
		http.Error(w, "the transfer slip could not be drawn", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-store")
	if format == slip.FormatHTML {
		// Replaces the shell's policy: the page has no script and no
		// external source, only its own inline stylesheet.
		h.Set("Content-Security-Policy", slip.CSP())
	}
	if q.Get("download") == "1" {
		h.Set("Content-Disposition", `attachment; filename="`+sl.Filename(format)+`"`)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body) //nolint:gosec // G705: body is the slip package's rendering, escaped by html/template
}

// slipInstance gathers what this UI holds that the slip is drawn from.
func (u *UI) slipInstance() *slip.Instance {
	inst := &slip.Instance{Version: u.render.Version, Queue: u.queue, Now: u.now()}
	if u.store != nil {
		inst.Root = u.store.Root()
	}
	if u.mediaZone != nil {
		inst.Zone = u.mediaZone.Zone()
		if rec, ok := u.mediaZone.LastMediaImport(); ok {
			inst.LastImport = &rec
		}
	}
	if u.mediaGate != nil {
		inst.Report = u.mediaGate.Status().Report
	}
	if u.render.Scan != nil {
		inst.Scan = func() slip.ScanContext {
			s := u.render.Scan()
			return slip.ScanContext{Blocking: s.Blocking, Cause: s.Cause, Database: s.Database, MaxAge: s.MaxAge}
		}
	}
	return inst
}
