//go:build browser

// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package browser

import (
	"fmt"
	"math"
	"testing"
)

// headerHeight measures the sticky header box.
const headerHeight = `document.querySelector(".t-header").getBoundingClientRect().height`

// TestCopyChipRaisesExactlyOneToast locks B-001.
//
// The layout's inline script wires listeners on `document` and
// `document.body`, both of which survive an hx-boost swap — while the
// script itself is re-executed by every boosted navigation. Without the
// `window.__tobbyWired` guard the delegated copy listener stacked up, and
// one click on a chip raised as many toasts as pages visited.
//
// Nothing about that is visible server-side: the rendered HTML is the same
// on the first navigation and on the fifth. It takes a browser that has
// actually navigated, and a real click.
func TestCopyChipRaisesExactlyOneToast(t *testing.T) {
	inst := newInstance(t, withContent(), withQueue(nil))
	s := newSession(t)
	s.grantClipboard(t, inst.URL)
	inst.signIn(t, s, "/content")
	s.wait(t, "the Content screen is ready", contentReady)

	// Four boosted navigations before the click: with the guard removed
	// the script would have wired itself five times over, so a stale build
	// fails by a wide, unambiguous margin.
	for _, hop := range []struct{ link, ready string }{
		{`.t-nav a[href="/tasks"]`, tasksReady},
		{`.t-nav a[href="/content"]`, contentReady},
		{`.t-nav a[href="/tasks"]`, tasksReady},
		{`.t-nav a[href="/content"]`, contentReady},
	} {
		s.click(t, "boosted navigation via "+hop.link, hop.link)
		s.wait(t, "the boosted navigation settled", hop.ready)
	}

	// The guard itself, stated: a boosted swap must not have re-armed it.
	if !s.evalBool(t, "reading the idempotence guard", `window.__tobbyWired === true`) {
		t.Error("window.__tobbyWired is not set after four boosted navigations: " +
			"the shell listeners are unguarded and stack up (B-001)")
	}

	const chip = `#content-results button[data-copy]`
	want := s.evalString(t, "reading the chip's payload",
		fmt.Sprintf(`document.querySelector(%s).getAttribute("data-copy")`, jsString(chip)))
	if want == "" {
		t.Fatal("no copy chip on the Content listing: the fixture did not render")
	}

	s.copyChip(t, "copying a repository path", chip)
	s.wait(t, "the copy toast appeared", `document.querySelectorAll("#toasts .t-toast").length >= 1`)
	// Duplicate listeners all fire inside the same click dispatch and
	// resolve in the same microtask flush, so after two frames the count
	// is final — "exactly one" is an assertion, not a race.
	s.settle(t)
	if n := s.evalInt(t, "counting the toasts",
		`document.querySelectorAll("#toasts .t-toast").length`); n != 1 {
		t.Errorf("one copy raised %d toasts, want exactly 1: the shell script wired a new "+
			"copy listener on each boosted navigation (B-001)", n)
	}

	// The chip's contract is the COMPLETE value, not the truncated one the
	// cell displays. Only the clipboard can tell.
	if got := s.readClipboard(t); got != want {
		t.Errorf("the clipboard holds %q, the chip advertises %q: the copy chip does not "+
			"carry the complete value (UI-SPEC §7)", got, want)
	}
}

// TestThemeToggleReachesTheDocumentElement locks B-004.
//
// `data-theme` lives on <html>, and hx-boost only ever replaces <body>: a
// boosted theme form swapped the body and left the attribute — the toggle
// looked dead. The fix is `hx-boost="false"` on the preference forms
// (ADR-0015 §7), and its effect exists nowhere but in the browser: the
// server renders the very same markup either way.
func TestThemeToggleReachesTheDocumentElement(t *testing.T) {
	inst := newInstance(t, withContent())
	s := newSession(t)
	inst.signIn(t, s, "/")

	before := s.evalString(t, "reading the current theme", `document.documentElement.dataset.theme`)
	if before != "dark" && before != "light" {
		t.Fatalf("the document element carries no theme (data-theme=%q)", before)
	}
	after := "light"
	if before == "light" {
		after = "dark"
	}

	// Toggle from a boosted page, not from a freshly loaded one: that is
	// the state the bug needed.
	s.click(t, "boosted navigation to /content", `.t-nav a[href="/content"]`)
	s.wait(t, "the boosted navigation settled", contentReady)

	s.click(t, "toggling the theme", `form[action="/theme"] button`)
	s.wait(t, "the theme reached <html> (B-004)",
		fmt.Sprintf(`document.documentElement.dataset.theme === %s`, jsString(after)))
	// And the toggle came back to the page it was clicked from, rather
	// than dumping the operator on the dashboard.
	s.wait(t, "the toggle returned to /content", contentReady)
}

// TestUserMenuPopsUnderTheHeader locks B-005: the <details> of the user
// menu used to push the layout, growing the sticky header and shoving the
// page down. The fix is a positioned pop-under, and "does it displace
// anything" is a question only a laid-out document can answer.
func TestUserMenuPopsUnderTheHeader(t *testing.T) {
	inst := newInstance(t)
	s := newSession(t)
	inst.signIn(t, s, "/")

	closed := s.evalFloat(t, "measuring the closed header", headerHeight)
	if closed <= 0 {
		t.Fatalf("the header measures %.1fpx: the shell did not lay out", closed)
	}

	s.click(t, "opening the user menu", `.t-usermenu summary`)
	s.wait(t, "the user menu is open and painted", `(() => {
		const menu = document.querySelector(".t-usermenu");
		const panel = document.querySelector(".t-usermenu > form");
		return !!menu && menu.open === true && !!panel &&
			panel.getBoundingClientRect().height > 0;
	})()`)
	s.settle(t)

	if open := s.evalFloat(t, "measuring the open header", headerHeight); math.Abs(open-closed) > 0.5 {
		t.Errorf("opening the user menu grew the header from %.1fpx to %.1fpx: the panel is "+
			"in the flow instead of popping under (B-005)", closed, open)
	}
	// Stated positively: the panel hangs BELOW the header rather than
	// making room for itself inside it.
	overhang := s.evalFloat(t, "measuring the panel's overhang", `
		document.querySelector(".t-usermenu > form").getBoundingClientRect().bottom -
		document.querySelector(".t-header").getBoundingClientRect().bottom`)
	if overhang <= 0 {
		t.Errorf("the user menu panel ends %.1fpx inside the header box: it is still part of "+
			"the header's layout (B-005)", -overhang)
	}
}
