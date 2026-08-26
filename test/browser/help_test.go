//go:build browser

// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package browser

import (
	"fmt"
	"strings"
	"testing"
)

// helpReady is the landing marker of the /help home: the guide navigation
// and the anchored code index are both in the document.
const helpReady = `location.pathname === "/help" && ` +
	`document.querySelector(".t-help-grid") !== null && ` +
	`document.querySelector(".t-helpsec") !== null`

// landedOn is the predicate for "the browser performed the jump to this
// anchor": the page scrolled, and the anchored element is in the viewport.
//
// Not "its top is at zero": the shell's header is sticky and the sections
// carry scroll-margin-top for it, and the last entry of a long index
// cannot reach the top at all — the document runs out of scroll. What
// separates a jump from no jump is unambiguous all the same, since without
// it the reader sits at scrollY 0 with the target far below the fold.
func landedOn(sel string) string {
	return fmt.Sprintf(`(() => {
		const e = document.querySelector(%s);
		if (!e) { return "no element " + %s; }
		const top = Math.round(e.getBoundingClientRect().top);
		if (window.scrollY === 0) { return "the page never scrolled"; }
		if (top < -20 || top > window.innerHeight - 50) { return "off-screen at " + top + "px"; }
		return "";
	})()`, jsString(sel), jsString(sel))
}

// TestErrorHelpAnchorLandsOnItsSection locks the corrective half of the
// NFR-003 amendment: "error codes rendered in the UI carry a working link
// to their troubleshooting entry".
//
// Working means the reader arrives AT the entry. The server is right
// either way — the anchor is in the href and the section is in the
// document — so this is the class of defect the browser level exists for
// (R-38, B-004 and B-013 archetypes): hx-boost cancels the navigation and
// swaps the body itself, and the browser never performs the jump to the
// fragment. The reader lands at the top of an index of forty codes, which
// is indistinguishable from a broken link.
func TestErrorHelpAnchorLandsOnItsSection(t *testing.T) {
	inst := newInstance(t, withQueue(nil))
	s := newSession(t)
	// A task identifier nobody minted: the taxonomized error page for
	// TBY-TSK-001, error block and help link included (UI-SPEC §5.13).
	inst.signIn(t, s, "/tasks/no-such-task")
	s.wait(t, "the taxonomy error page is rendered",
		`document.querySelector(".t-error-block") !== null`)

	const link = `.t-error-block-foot a[href^="/help#"]`
	code := s.evalString(t, "reading the code the error block links",
		fmt.Sprintf(`document.querySelector(%s).getAttribute("href").split("#")[1]`, jsString(link)))
	if code == "" {
		t.Fatal("the error block carries no help anchor: the link to the troubleshooting entry is missing")
	}

	s.click(t, "following the help link of the error block", link)
	s.wait(t, "the help page is loaded on its anchor",
		helpReady+` && location.hash === "#`+code+`"`)

	// The section exists — that much a server test proves. What only a
	// browser can say is whether the reader is looking at it.
	section := "#" + code
	if why := s.evalString(t, "checking the reader landed on the anchored section", landedOn(section)); why != "" {
		t.Errorf("the reader did not land on the anchored section %s (%s): the jump to the fragment "+
			"did not happen and the reader is at the top of an index of forty codes instead "+
			"(hx-boost swallows the anchor — same remedy as B-004/B-013)", section, why)
	}
}

// TestGuideCrossPageAnchorLandsOnItsHeading is the same property inside
// the embedded corpus: a guide that points into a section of another guide
// must put the reader on that section. The links are written by the
// documentation, so nothing in a template can be inspected to know they
// behave — only a browser that followed one can.
func TestGuideCrossPageAnchorLandsOnItsHeading(t *testing.T) {
	inst := newInstance(t, withQueue(nil))
	s := newSession(t)
	inst.signIn(t, s, "/help/passthrough/deploy")
	s.wait(t, "the guide is rendered", `document.querySelector(".t-doc-body") !== null`)

	const link = `.t-doc-body a[href*="/help/"][href*="#"]`
	target := s.evalString(t, "reading the cross-page anchor the guide points at",
		fmt.Sprintf(`document.querySelector(%s).getAttribute("href")`, jsString(link)))
	if target == "" {
		t.Skip("this guide carries no cross-page anchor link any more")
	}

	s.click(t, "following the cross-page anchor "+target, link)
	s.wait(t, "the target guide is loaded on its anchor",
		`document.querySelector(".t-doc-body") !== null && location.href.endsWith(`+jsString(target)+`)`)

	_, frag, _ := strings.Cut(target, "#")
	if why := s.evalString(t, "checking the reader landed on the target heading", landedOn("#"+frag)); why != "" {
		t.Errorf("the reader did not land on the heading #%s (%s): the guide's cross-page anchor "+
			"drops the reader at the top of the page instead of on the section it names", frag, why)
	}
}

// TestEmbeddedGuidesFetchNothingExternal is the runtime half of NFR-019 on
// this surface: reading the documentation of an air-gapped instance must
// not open a single connection off the instance. A test over the rendered
// HTML can miss what a stylesheet, a font declaration or a lazily-loaded
// image would do; the browser's own network log cannot.
func TestEmbeddedGuidesFetchNothingExternal(t *testing.T) {
	inst := newInstance(t, withQueue(nil))
	s := newSession(t)
	inst.signIn(t, s, "/help")
	s.wait(t, "the help home is rendered", helpReady)

	// A page carrying a screenshot, a diagram, a table and external links.
	s.click(t, "opening an illustrated guide", `.t-help-section a[href="/help/try/install-and-start"]`)
	s.wait(t, "the guide is rendered",
		`location.pathname === "/help/try/install-and-start" && document.querySelector(".t-doc-shot") !== null`)
	s.settle(t)

	origin := s.evalString(t, "reading the page origin", `location.origin`)
	foreign := s.evalInt(t, "counting the resources fetched from elsewhere", fmt.Sprintf(
		`performance.getEntriesByType("resource").filter(e => !e.name.startsWith(%s)).length`,
		jsString(origin+"/")))
	if foreign > 0 {
		names := s.evalString(t, "naming them", fmt.Sprintf(
			`performance.getEntriesByType("resource").filter(e => !e.name.startsWith(%s)).map(e => e.name).join(", ")`,
			jsString(origin+"/")))
		t.Errorf("reading the embedded documentation fetched %d resource(s) from outside the "+
			"instance (NFR-019): %s", foreign, names)
	}
}
