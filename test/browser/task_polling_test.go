//go:build browser

// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package browser

import (
	"context"
	"log/slog"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// taskBodyArmed is true while the detail's polled zone still carries its
// own polling attributes. The server decides, per render, whether to emit
// them (auto-terminating load-polling, UI-SPEC §8.3) — so this predicate
// reads the client's copy of a server decision, which is precisely the
// seam a browser test exists to check.
const taskBodyArmed = `(() => {
	const zone = document.getElementById("task-body");
	return !!zone && zone.getAttribute("hx-get") !== null;
})()`

// TestTaskDetailUpdatesItselfThenStops locks B-002.
//
// After an import, HX-Redirect lands the operator on the task detail. The
// statuses were frozen there: the logs streamed, but nothing else moved
// until a manual refresh, because only the log sentinel polled. The fix
// gave the body zone the same auto-terminating polling as the listing.
//
// Two halves, and both need a browser. That the zone updates on its own
// cannot be seen from a handler that answers correctly every time it is
// asked; that the polling STOPS is a property of a page left alone, which
// no request-level test observes at all.
func TestTaskDetailUpdatesItselfThenStops(t *testing.T) {
	// A runner the test drives: the task must still be running when the
	// page loads and must finish while nobody touches the browser. A real
	// import would race the page load, and a race is how a browser suite
	// starts getting deleted for flakiness.
	release := make(chan struct{})
	runner := func(ctx context.Context, task *tasks.Task, _ *slog.Logger, save func()) error {
		task.Items[0].Status = tasks.StatusRunning
		save()
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		for i := range task.Items {
			task.Items[i].Status = tasks.StatusDone
		}
		save()
		return nil
	}

	inst := newInstance(t, withQueue(runner))
	task, err := inst.Queue.Create(tasks.TypeUnitImport, "docker.io/library/redis:7.2", adminUser,
		[]tasks.Item{
			{Name: "linux/amd64", Digest: "sha256:1111"},
			{Name: "linux/arm64", Digest: "sha256:2222"},
		})
	if err != nil {
		t.Fatalf("creating the task: %v", err)
	}
	inst.waitStatus(t, task.ID, tasks.StatusRunning)

	s := newSession(t)
	inst.signIn(t, s, "/tasks/"+task.ID)

	s.wait(t, "the detail arms its own polling on arrival", taskBodyArmed)
	s.wait(t, "the first item shows as running",
		`document.querySelector("#task-body .t-badge--running") !== null`)

	// From here the browser is left strictly alone: no click, no reload.
	// Whatever changes below changed by itself.
	close(release)
	inst.waitIdle(t)

	s.wait(t, "the item statuses updated without a refresh (B-002)", `
		document.querySelectorAll("#task-body .t-badge--done").length >= 2 &&
		document.querySelector("#task-body .t-badge--running") === null`)
	s.wait(t, "the polling stopped by itself once the task settled (UI-SPEC §8.3)",
		`!(`+taskBodyArmed+`)`)

	// The zone is swapped in place, not stuffed into itself. A swap style
	// the client does not understand silently degrades to innerHTML, which
	// nests a second #task-body inside the first: duplicate ids, a stale
	// outer element that keeps its original polling attributes forever,
	// and one more poller per cycle. The screen looks almost right, which
	// is why only a browser catches it.
	if n := s.evalInt(t, "counting the polled zones",
		`document.querySelectorAll("#task-body").length`); n != 1 {
		t.Errorf("the detail holds %d #task-body zones, want exactly 1: the polled response "+
			"was nested instead of replacing the zone (B-002)", n)
	}
}
