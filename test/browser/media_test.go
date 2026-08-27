//go:build browser

// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package browser

import (
	"context"
	"testing"

	"github.com/chromedp/chromedp"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/mediagate"
)

// mediaBodyArmed is true while the Media screen's polled zone still
// carries its own polling attributes. The server decides, per render,
// whether to emit them (auto-terminating load-polling, UI-SPEC §8.3), so
// this predicate reads the client's copy of a server decision — the seam
// a browser test exists to check.
const mediaBodyArmed = `(() => {
	const zone = document.getElementById("media-body");
	return !!zone && zone.getAttribute("hx-get") !== null;
})()`

// pushControlPresent is the R-02 acceptance as the DOM sees it: the Push
// control is not disabled, it does not exist.
const pushControlPresent = `document.querySelector('form[action="/media/import"]') !== null`

// TestMediaScreenUnlocksPushWhenVerificationLands is the browser half of
// R-02, and it is here rather than in internal/ui because of the
// admission rule (R-38): is the handler right while the screen is broken?
//
// Yes, on all three counts below, and every one of them is invisible
// server-side.
//
//  1. The guided sequence must UNLOCK BY ITSELF. Verifying a medium takes
//     minutes; the operator starts it and waits. If the polled zone does
//     not update, the screen stays on step 1 forever while the server
//     answers a perfectly correct step 3 to anyone who reloads — and an
//     operator who does not know to reload concludes the medium failed.
//  2. The polling must STOP. A page left alone after the verdict must
//     stop asking, which is a property of an idle page and not of any
//     single response.
//  3. The zone must be REPLACED, not nested. `morph:outerHTML` is inert
//     without the shell's hx-ext="morph"; htmx then falls back to
//     innerHTML, stuffs #media-body inside itself, and the outer element
//     keeps its polling attributes forever (B-012, found exactly this way
//     on the task detail). The fragments are byte-correct either way.
func TestMediaScreenUnlocksPushWhenVerificationLands(t *testing.T) {
	// A verification the test drives: it must still be running when the
	// page settles and must finish while nobody touches the browser. A
	// real walk of the disk would race the page load, and a race is how a
	// browser suite starts getting deleted for flakiness.
	release := make(chan struct{})
	verify := mediagate.Verify(func(ctx context.Context, _ mediagate.Options,
		progress func(media.Progress),
	) (*media.Report, error) {
		progress(media.Progress{
			Stage: media.StageRecipes, Recipe: "wordpress@6.8.2",
			Bytes: 512, TotalBytes: 2048, Files: 3, TotalFiles: 12,
		})
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &media.Report{
			Verdict: media.VerdictPushable,
			Checked: media.Totals{Files: 12, Bytes: 2048},
			Recipes: []media.RecipeVerdict{{
				Name: "wordpress", Version: "6.8.2", Pushable: true,
				KeyFingerprint: "SHA256:zoneA", Files: 12, Bytes: 2048,
			}},
		}, nil
	})

	inst := newInstance(t, withMedium(verify))
	s := newSession(t)
	inst.signIn(t, s, "/media")

	s.wait(t, "the Media screen offers Verify and withholds Push",
		`document.querySelector('form[action="/media/verify"]') !== null && !(`+pushControlPresent+`)`)

	s.click(t, "starting the verification", `form[action="/media/verify"] button[type="submit"]`)
	s.wait(t, "the screen shows live progress and arms its own polling",
		mediaBodyArmed+` && document.querySelector("#media-body progress") !== null`)

	// From here the browser is left strictly alone: no click, no reload.
	// Whatever changes below changed by itself.
	close(release)

	s.wait(t, "the verdict lands and Push becomes reachable without a refresh (R-02)",
		pushControlPresent)
	s.wait(t, "the polling stopped by itself once the verdict landed (UI-SPEC §8.3)",
		`!(`+mediaBodyArmed+`)`)

	if n := s.evalInt(t, "counting the polled zones",
		`document.querySelectorAll("#media-body").length`); n != 1 {
		t.Errorf("the screen holds %d #media-body zones, want exactly 1: the polled response "+
			"was nested instead of replacing the zone (B-012)", n)
	}

	// The download of the raw report is a file, and hx-boost never looks
	// at the download attribute (B-013): a boosted link would swap the
	// JSON into the page instead of saving it. Checked in the browser
	// because the server's headers were correct in that bug too.
	if !s.evalBool(t, "checking the report link is not boosted",
		`(() => {
			const a = document.querySelector('a[href="/api/v1/media/verification"]');
			return !!a && a.getAttribute("hx-boost") === "false";
		})()`) {
		t.Error("the report download link is boosted or missing (B-013)")
	}
}

// mediaScreenReady is the boosted-navigation landing marker for /media.
const mediaScreenReady = `location.pathname === "/media" && ` +
	`document.getElementById("media-body") !== null`

// TestMediaScreenIsReachableFromEveryScreen: an operator holding a disk
// should not have to know what kind of instance they are looking at to
// find the screen that walks them through the transfer (R-02). The nav
// entry is boosted like every other, so this checks the navigation
// actually lands rather than that the anchor exists — the latter is a
// server-side fact, already covered.
func TestMediaScreenIsReachableFromEveryScreen(t *testing.T) {
	inst := newInstance(t, withMedium(nil))
	s := newSession(t)
	inst.signIn(t, s, "/")

	s.click(t, "following the Media entry of the shell", `nav.t-nav a[href="/media"]`)
	s.wait(t, "landing on the Media screen", mediaScreenReady)
	s.run(t, "checking the entry is marked current",
		chromedp.WaitVisible(`nav.t-nav a[href="/media"][aria-current="page"]`, chromedp.ByQuery))
}
