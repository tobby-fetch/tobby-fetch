//go:build browser

// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package browser

import (
	"context"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// slipNoticeEN and slipNoticeFR are the mention R-07 makes mandatory.
const (
	slipNoticeEN = "Unsigned aid document — not proof of authenticity"
	slipNoticeFR = "Document d'aide non signé — ne vaut pas preuve d'authenticité"
)

// TestTransferSlipOpensAndPrints locks R-07 at the only level that can
// check it.
//
// internal/ui and internal/slip prove the document is right: both notices
// are there, in both languages, and the policy header names the one inline
// stylesheet. What a server-side test cannot see is whether a browser,
// enforcing that policy, still applies the stylesheet — a hash that drifts
// from the bytes served (an escaping change in the template engine is
// enough) silently turns the document into unstyled text under a strict
// CSP, with every server-side assertion still green. Nor can it see that
// the Media screen's links open a document rather than swapping one into
// the shell (the B-013 class), or that the print rules survive Chrome's
// print pipeline.
func TestTransferSlipOpensAndPrints(t *testing.T) {
	inst := newInstance(t, withMedium(nil))
	s := newSession(t)
	inst.signIn(t, s, "/media")

	// The entry points: a new tab and files, never boosted, or htmx would
	// swap a whole document into the shell.
	if !s.evalBool(t, "checking the slip links of the Media screen", `(() => {
		const open = document.querySelector('a[href="/media/slip"]');
		const html = document.querySelector('a[href="/media/slip?download=1"]');
		const text = document.querySelector('a[href="/media/slip?format=text&download=1"]');
		return !!open && open.target === "_blank" && open.getAttribute("hx-boost") === "false" &&
			!!html && html.hasAttribute("download") && html.getAttribute("hx-boost") === "false" &&
			!!text && text.hasAttribute("download") && text.getAttribute("hx-boost") === "false";
	})()`) {
		t.Fatal("the Media screen does not offer the slip as an unboosted page and two downloads (R-07, B-013)")
	}

	// Saving as text produces a FILE named after the medium.
	dir := t.TempDir()
	dl := s.captureDownloads(t, dir)
	s.click(t, "saving the slip as text", `a[href="/media/slip?format=text&download=1"]`)
	if name := dl.wait(t); !strings.HasPrefix(name, "transfer-slip-") || !strings.HasSuffix(name, ".txt") {
		t.Errorf("the text slip downloaded as %q, want transfer-slip-<medium>.txt", name)
	}

	// Open the document itself: the link's target, in this tab.
	s.run(t, "opening the transfer slip", chromedp.Navigate(inst.URL+"/media/slip?lang=en"))
	s.wait(t, "the slip is loaded", `document.getElementById("notice-bottom") !== null`)

	// Under its own strict policy the page must still be styled: the
	// notice is a 3px bordered box only if the inline sheet applied.
	if !s.evalBool(t, "checking the inline stylesheet was applied under the CSP",
		`getComputedStyle(document.getElementById("notice-top")).borderTopWidth === "3px"`) {
		t.Error("the notice is unstyled: the browser refused the slip's inline stylesheet (policy hash drift)")
	}
	if got := s.evalString(t, "reading the top notice",
		`document.querySelector("#notice-top strong").textContent`); got != slipNoticeEN {
		t.Errorf("top notice = %q, want %q", got, slipNoticeEN)
	}

	// Head and foot: the first is in sight on load, the second is
	// reachable at the end, and neither is hidden.
	if !s.evalBool(t, "checking the top notice is in sight on load", `(() => {
		const r = document.getElementById("notice-top").getBoundingClientRect();
		return r.height > 0 && r.top >= 0 && r.top < window.innerHeight;
	})()`) {
		t.Error("the unsigned notice is not in sight when the slip opens")
	}
	s.evalBool(t, "scrolling to the foot", `(window.scrollTo(0, document.body.scrollHeight), true)`)
	if !s.evalBool(t, "checking the closing notice is rendered at the foot", `(() => {
		const r = document.getElementById("notice-bottom").getBoundingClientRect();
		return r.height > 0 && r.bottom <= window.innerHeight + 1;
	})()`) {
		t.Error("the closing unsigned notice is not at the foot of the slip")
	}

	// The slip is standalone: it must load nothing.
	if n := s.evalInt(t, "counting the resources the slip loaded",
		`performance.getEntriesByType("resource").length`); n != 0 {
		t.Errorf("the slip loaded %d external resource(s): it must be self-contained", n)
	}

	// Print: the print rules apply, and Chrome produces a PDF.
	s.run(t, "emulating the print medium",
		command(emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{Media: "print"}))
	if !s.evalBool(t, "checking the print rules apply",
		`getComputedStyle(document.body).paddingTop === "0px" && matchMedia("print").matches`) {
		t.Error("the @media print rules of the slip do not apply")
	}
	var pdf []byte
	s.run(t, "printing the slip to PDF", chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		res, err := cdp.Call(ctx, tg, page.PrintToPDF, page.PrintToPDFParams{PreferCSSPageSize: new(true)})
		if err == nil {
			pdf = res.Data
		}
		return err
	}))
	if len(pdf) < 2048 || string(pdf[:4]) != "%PDF" {
		t.Errorf("printing the slip produced %d bytes starting %q, want a PDF", len(pdf), string(pdf[:min(4, len(pdf))]))
	}
	s.run(t, "restoring the screen medium",
		command(emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{Media: ""}))

	// And in French.
	s.run(t, "opening the French slip", chromedp.Navigate(inst.URL+"/media/slip?lang=fr"))
	s.wait(t, "the French slip is loaded", `document.documentElement.lang === "fr"`)
	for _, id := range []string{"notice-top", "notice-bottom"} {
		if got := s.evalString(t, "reading "+id,
			`document.querySelector("#`+id+` strong").textContent`); got != slipNoticeFR {
			t.Errorf("%s = %q, want %q", id, got, slipNoticeFR)
		}
	}
}
