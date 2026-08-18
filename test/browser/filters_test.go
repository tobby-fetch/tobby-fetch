//go:build browser

// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package browser

import (
	"fmt"
	"testing"

	"github.com/chromedp/chromedp"

	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// contentReady is true once the Content screen's filter form is in the
// document — the boosted-navigation landing marker for /content. The
// header's own search form also posts to /content, hence the class.
const contentReady = `location.pathname === "/content" && ` +
	`document.querySelector('form.t-content-filters[action="/content"]') !== null`

// tasksReady is the same marker for the Tasks screen.
const tasksReady = `location.pathname === "/tasks" && ` +
	`document.querySelector('form.t-content-filters[action="/tasks"]') !== null`

// TestContentFiltersEveryKindReachesTheServer locks B-011 on the Content
// screen — the case this whole level was justified by.
//
// The bug: `hx-trigger="… change from:find input[type='checkbox']"` reads
// as "listen to the checkboxes" and means "listen to the FIRST checkbox".
// Four of the five kind badges toggled, looked selected, and requested
// nothing. Browse was correct throughout; the browser never called it, so
// no server test could see it.
//
// The assertion is therefore not about results — those are covered, faster
// and more precisely, in internal/ui. It is about the request happening at
// all, observed through the URL hx-push-url writes once the swap settled:
// a pushed `kind=` parameter is proof the browser went to the server and
// came back.
func TestContentFiltersEveryKindReachesTheServer(t *testing.T) {
	inst := newInstance(t, withContent())
	s := newSession(t)
	inst.signIn(t, s, "/content")
	s.wait(t, "the Content filter form is ready", contentReady)

	// All five, one at a time. Under B-011 only the first one passed, and
	// a suite that tested "a checkbox" instead of "every checkbox" would
	// have shipped the bug.
	for _, kind := range []string{"ContainerImage", "HelmChart", "OCIArtifact", "FileSet", "Recipe"} {
		box := `input[name="kind"][value="` + kind + `"]`
		// The checkbox is visually hidden: what an operator clicks is the
		// badge label beside it, so that is what gets clicked here.
		badge := box + ` + span`

		s.click(t, "selecting the "+kind+" badge", badge)
		// Split on purpose: this first wait failing means the click never
		// reached the control, the second one failing means it reached it
		// and the form stayed silent — which is B-011 exactly.
		s.wait(t, "the "+kind+" badge registers the click",
			fmt.Sprintf(`document.querySelector(%s).checked === true`, jsString(box)))
		s.wait(t, "the "+kind+" filter reaches the server (B-011)", kindInQuery(kind, true))

		s.click(t, "deselecting the "+kind+" badge", badge)
		s.wait(t, "the "+kind+" filter leaves the query (B-011)", kindInQuery(kind, false))
	}

	// The one `from:find` the static guard still tolerates, because the
	// selector matches a single element: the debounced search field.
	s.run(t, "typing in the content search",
		chromedp.SendKeys(`form.t-content-filters input[type="search"]`, "wordpress", chromedp.NodeVisible))
	s.wait(t, "the debounced search reaches the server",
		`new URLSearchParams(location.search).get("q") === "wordpress"`)
}

// kindInQuery builds the predicate "the canonical URL does (not) carry
// this kind".
func kindInQuery(kind string, want bool) string {
	return fmt.Sprintf(`new URLSearchParams(location.search).getAll("kind").includes(%s) === %t`,
		jsString(kind), want)
}

// TestTasksFiltersBothSelectsReachTheServer locks the second half of
// B-011: on the Tasks screen the status filter worked and the type filter
// was inert, because `from:find select` bound htmx to the first of the two
// selects. Both must submit.
func TestTasksFiltersBothSelectsReachTheServer(t *testing.T) {
	// No runner registered: the seeded tasks settle at once. Deliberate —
	// this scenario is about the filter controls, and a task still running
	// would arm the row polling and put a second moving part next to the
	// one under test.
	inst := newInstance(t, withQueue(nil))
	for _, seed := range []struct{ kind, ref string }{
		{tasks.TypeUnitImport, "docker.io/library/redis:7.2"},
		{tasks.TypeSync, "cookbook/wordpress:6.8.2"},
	} {
		if _, err := inst.Queue.Create(seed.kind, seed.ref, adminUser,
			[]tasks.Item{{Name: "linux/amd64", Digest: "sha256:0123"}}); err != nil {
			t.Fatalf("seeding a task: %v", err)
		}
	}
	inst.waitIdle(t)

	s := newSession(t)
	inst.signIn(t, s, "/tasks")
	s.wait(t, "the Tasks filter form is ready", tasksReady)

	s.chooseOption(t, "choosing the status filter", "#tasks-status", "running")
	s.wait(t, "the status filter reaches the server",
		`new URLSearchParams(location.search).get("status") === "running"`)

	// The one that was dead. Its change event bubbles to the form exactly
	// like the first select's; only the listener's placement decided
	// whether anything heard it.
	s.chooseOption(t, "choosing the type filter", "#tasks-type", "sync")
	s.wait(t, "the type filter reaches the server (B-011: this one was inert)",
		`new URLSearchParams(location.search).get("type") === "sync"`)
}
