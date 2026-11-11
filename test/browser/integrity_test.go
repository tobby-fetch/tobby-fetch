//go:build browser

// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package browser

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/integrity"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// integrityBodyArmed is true while the Store integrity screen's polled
// zone still carries its own polling attributes. The server decides, per
// render, whether to emit them (auto-terminating polling, UI-SPEC §8.3),
// so this predicate reads the client's copy of a server decision — the
// seam a browser test exists to check.
const integrityBodyArmed = `(() => {
	const zone = document.getElementById("integrity-body");
	return !!zone && zone.getAttribute("hx-get") !== null;
})()`

// TestIntegrityScreenShowsTheVerdictWhenTheWalkEnds is the browser half of
// R-31, here rather than in internal/ui because of the admission rule
// (R-38): is the handler right while the screen is broken? Yes on all
// three counts, and each is invisible server-side.
//
//  1. The screen must UPDATE BY ITSELF. A verification of a large store
//     takes minutes; the operator starts it and waits. If the polled zone
//     does not swap, the screen stays on "in progress" forever while the
//     server answers the verdict to anyone who reloads.
//  2. The polling must STOP once the verdict landed.
//  3. The zone must be REPLACED, not nested: `morph:outerHTML` is inert
//     without the shell's hx-ext="morph", and htmx then falls back to
//     innerHTML — the outer element keeps its polling attributes forever
//     (B-012, found exactly this way on the task detail).
//
// The walk is driven by the test: it must still be running when the page
// settles and finish while nobody touches the browser. A real walk of a
// disk would race the page load.
func TestIntegrityScreenShowsTheVerdictWhenTheWalkEnds(t *testing.T) {
	release := make(chan struct{})
	walk := tasks.Runner(func(ctx context.Context, task *tasks.Task, _ *slog.Logger, save func()) error {
		task.Verification = &integrity.Report{
			StartedAt: time.Now().UTC(),
			Progress:  &integrity.Progress{Stage: integrity.StageBlobs, Done: 40, Total: 100, Bytes: 4096},
		}
		save()
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		const blob = "docker/registry/v2/blobs/sha256/ab/abcd/data"
		task.Verification = &integrity.Report{
			StartedAt: time.Now().UTC().Add(-time.Minute), FinishedAt: time.Now().UTC(),
			Verdict: integrity.VerdictDefects, DefectCount: 1,
			Store:   integrity.Store{Root: "/srv/store"},
			Checked: integrity.Totals{Blobs: 100, Bytes: 8192, Manifests: 9, Recipes: 2},
			Defects: []integrity.Defect{{
				Code: taxonomy.CodeIntegrityBlobDigest, Path: blob,
				Params: map[string]string{"path": blob, "expected": "sha256:aa", "actual": "sha256:bb"},
			}},
		}
		return task.Verification.Principal()
	})

	inst := newInstance(t, withIntegrity(walk))
	s := newSession(t)
	inst.signIn(t, s, "/integrity")

	s.wait(t, "the screen offers the verification",
		`document.querySelector('form[action="/integrity/verify"]') !== null`)
	s.click(t, "starting the verification", `form[action="/integrity/verify"] button[type="submit"]`)
	s.wait(t, "the screen shows live progress and arms its own polling",
		integrityBodyArmed+` && document.querySelector("#integrity-body progress") !== null`)

	// From here the browser is left strictly alone: no click, no reload.
	close(release)

	s.wait(t, "the verdict lands without a refresh and names the damaged blob",
		`document.querySelector('[data-verdict="defects"]') !== null && `+
			`document.getElementById("integrity-body").textContent.includes("TBY-INT-001")`)
	s.wait(t, "the polling stopped by itself once the verdict landed (UI-SPEC §8.3)",
		`!(`+integrityBodyArmed+`)`)

	if n := s.evalInt(t, "counting the polled zones",
		`document.querySelectorAll("#integrity-body").length`); n != 1 {
		t.Errorf("the screen holds %d #integrity-body zones, want exactly 1: the polled response "+
			"was nested instead of replacing the zone (B-012)", n)
	}
	// The raw report is a file: hx-boost never looks at the download
	// attribute (B-013), so a boosted link would swap the JSON into the
	// page instead of saving it.
	if !s.evalBool(t, "checking the report link is not boosted",
		`(() => {
			const a = document.querySelector('a[href="/api/v1/store/verification"]');
			return !!a && a.getAttribute("hx-boost") === "false";
		})()`) {
		t.Error("the report download link is boosted or missing (B-013)")
	}
	// The screen reports; it never offers to repair anything.
	if s.evalBool(t, "looking for a repair control",
		`document.querySelector('form[action*="repair"], form[action*="delete"], form[action*="prune"]') !== null`) {
		t.Error("the Store integrity screen offers to repair or delete: a verification only reports")
	}
}
