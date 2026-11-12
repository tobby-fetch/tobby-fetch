//go:build browser

// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package browser

import (
	"fmt"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
	"github.com/tobby-fetch/tobby-fetch/internal/ui"
)

// TestScanDegradedBannerStaysInSightAndLinksItsEntry is the browser half
// of FR-031's degraded mode: "never silent". The server renders the banner
// on every page — a server test proves that — so what is left to prove is
// what only a browser shows: the banner is actually in the reader's sight
// at the top of the shell, stays there after an htmx-boosted navigation
// (it lives outside every swap target, ADR-0015), and its code link puts
// the reader on the TBY-SCN-001 troubleshooting entry rather than at the
// top of an index of codes (the B-004/B-013 archetype).
func TestScanDegradedBannerStaysInSightAndLinksItsEntry(t *testing.T) {
	degraded := func() ui.ScanState {
		return ui.ScanState{Cause: taxonomy.New(taxonomy.CodeScannerUnavailable,
			taxonomy.Params{"detail": "no trivy executable on the PATH"})}
	}
	inst := newInstance(t, withQueue(nil), withScan(degraded))
	s := newSession(t)
	inst.signIn(t, s, "/")

	const banner = `.t-banner--danger`
	inSight := fmt.Sprintf(`(() => {
		const b = document.querySelector(%s);
		if (!b) { return false; }
		const r = b.getBoundingClientRect();
		return r.height > 0 && r.top >= 0 && r.bottom <= window.innerHeight &&
			b.textContent.includes("no trivy executable on the PATH");
	})()`, jsString(banner))
	s.wait(t, "the degraded-mode banner is in sight on the dashboard", inSight)

	// A boosted navigation swaps the page body only: the banner, in the
	// sticky header, must survive it.
	s.click(t, "navigating to the task list", `a[href="/tasks"]`)
	s.wait(t, "the task list is shown", `location.pathname === "/tasks"`)
	s.wait(t, "the banner is still in sight after the boosted navigation", inSight)

	const link = `.t-banner--danger a[href="/help#TBY-SCN-001"]`
	s.wait(t, "the banner links its troubleshooting entry", fmt.Sprintf(`document.querySelector(%s) !== null`, jsString(link)))
	s.click(t, "following the banner's help link", link)
	s.wait(t, "the help page is loaded on its anchor", helpReady+` && location.hash === "#TBY-SCN-001"`)
	if why := s.evalString(t, "checking the reader landed on the entry", landedOn("#TBY-SCN-001")); why != "" {
		t.Errorf("the reader did not land on TBY-SCN-001 (%s)", why)
	}
}
