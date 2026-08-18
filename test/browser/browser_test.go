//go:build browser

// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package browser is the browser level of the test pyramid (R-38): the
// scenarios that lock the class of regression living in an attribute the
// CLIENT interprets — a bug that sits neither in the rendered HTML, which
// is correct, nor in the handler, which is correct too.
//
// Five of the eleven recorded bugs are of that class. B-011 is the
// archetype: Browse filtered correctly on the eleven combinations the Go
// tests exercised, and the screen still did nothing, because the browser
// never called the server. No amount of server-side testing can see that.
//
// Admission rule for anything added here — "is the handler right while the
// screen is broken?". If rendering the page server-side is enough to catch
// the regression, it belongs in internal/ui, where it costs milliseconds
// and points straight at the line. This suite stays deliberately narrow;
// every scenario names the bug or the requirement it locks.
//
// Anti-flaky discipline: every wait is on an OBSERVABLE STATE of the page,
// never on elapsed time. There is no sleep in this package, and CI runs it
// with -count=2 like the rest of the pyramid.
//
// The suite is behind the `browser` build tag, so `go test ./...` keeps its
// pace; CI runs it explicitly (job `e2e-browser`).
//
// Chrome is taken FROM THE ENVIRONMENT and never downloaded: NFR-019
// forbids any unconfigured outbound connection, at build time as much as at
// run time. With no browser installed the suite skips with an explicit
// message. Set TOBBY_E2E_REQUIRE_CHROME=1 — as the CI job does — to turn
// that skip into a failure, so a runner that lost its browser can never
// look green by testing nothing.
package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const (
	// chromeEnv points at an explicit browser binary, for an environment
	// where the lookup below finds nothing.
	chromeEnv = "TOBBY_E2E_CHROME"
	// requireEnv turns "no browser here" from a skip into a failure. CI
	// sets it: a green run must mean the scenarios ran.
	requireEnv = "TOBBY_E2E_REQUIRE_CHROME"
	// chromeBinEnv is the de-facto variable CI images already export, and
	// the reason this suite usually needs no configuration at all.
	chromeBinEnv = "CHROME_BIN"
)

const (
	// waitBudget bounds one wait on an observable state. Generous on
	// purpose: the slowest thing awaited here is an `every 2s` poll cycle
	// on a loaded runner, and a budget that is too tight is precisely how
	// a browser suite becomes flaky.
	waitBudget = 30 * time.Second
	// pollInterval is how often the page-side predicate is re-evaluated.
	// Explicit rather than the default animation-frame cadence, which a
	// backgrounded headless renderer can throttle.
	pollInterval = 40 * time.Millisecond
	// sessionBudget is the backstop for one whole scenario.
	sessionBudget = 5 * time.Minute
)

// chromePath is the browser binary this run drives, empty when the
// environment has none. Resolved once, in TestMain.
var chromePath string

// chromeSearched records where the lookup looked, so a skip says something
// actionable instead of "not found".
var chromeSearched string

// allocCtx owns the single browser process shared by the scenarios; each
// test gets its own tab from it (see newSession).
var allocCtx context.Context

// chromeCandidates are the names and locations a Chrome-family browser is
// looked up under, in order. The list is fixed and auditable on purpose:
// chromedp never downloads a browser and neither may this suite (NFR-019).
var chromeCandidates = []string{
	"google-chrome",
	"google-chrome-stable",
	"chromium",
	"chromium-browser",
	"chrome",
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
	"/usr/bin/google-chrome",
	"/usr/bin/google-chrome-stable",
	"/usr/bin/chromium",
	"/usr/bin/chromium-browser",
	"/snap/bin/chromium",
	`C:\Program Files\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
}

func TestMain(m *testing.M) {
	chromePath, chromeSearched = findChrome()
	os.Exit(run(m))
}

// run wraps m.Run so the allocator's cancel actually runs — os.Exit skips
// deferred calls.
func run(m *testing.M) int {
	if chromePath == "" {
		return m.Run()
	}
	ctx, cancel := chromedp.NewExecAllocator(context.Background(), allocatorOptions(chromePath)...)
	defer cancel()
	allocCtx = ctx
	return m.Run()
}

// findChrome resolves the browser binary without ever fetching one, and
// returns what it tried so a skip can say it.
func findChrome() (path, searched string) {
	if p := os.Getenv(chromeEnv); p != "" {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, p
		}
		return "", fmt.Sprintf("%s=%q, which is not an executable file", chromeEnv, p)
	}
	if p := os.Getenv(chromeBinEnv); p != "" {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, p
		}
	}
	for _, c := range chromeCandidates {
		if strings.ContainsRune(c, filepath.Separator) {
			if info, err := os.Stat(c); err == nil && !info.IsDir() {
				return c, c
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p, p
		}
	}
	return "", strings.Join(chromeCandidates, ", ")
}

// allocatorOptions configures the browser process.
func allocatorOptions(path string) []chromedp.ExecAllocatorOption {
	return append(chromedp.DefaultExecAllocatorOptions[:],
		// Explicit binary: nothing here may fall back to a download.
		chromedp.ExecPath(path),
		// NFR-019 made mechanical: the browser can resolve nothing but the
		// loopback instance under test. A scenario that started depending
		// on an outside host fails here rather than passing on a machine
		// that happens to have internet.
		chromedp.Flag("host-resolver-rules", "MAP * ~NOTFOUND, EXCLUDE 127.0.0.1, EXCLUDE localhost"),
		// Throwaway headless browser driving a loopback listener: the
		// sandbox costs a root-owned CI container the whole suite and buys
		// nothing here.
		chromedp.NoSandbox,
		// A window big enough that the header, its nav and the filter row
		// do not wrap: B-005 measures the header's height.
		chromedp.WindowSize(1440, 900),
	)
}

// requireChrome skips — or fails, under requireEnv — when the environment
// has no browser. Never a silent pass.
func requireChrome(t *testing.T) {
	t.Helper()
	if chromePath != "" {
		return
	}
	msg := fmt.Sprintf("no Chrome-family browser in this environment, so the R-38 browser "+
		"scenarios cannot run. Chrome is taken from the environment and NEVER downloaded "+
		"(NFR-019): install one, or point %s (or %s) at it. Looked for: %s",
		chromeEnv, chromeBinEnv, chromeSearched)
	if os.Getenv(requireEnv) != "" {
		t.Fatalf("%s is set: %s", requireEnv, msg)
	}
	t.Skip(msg)
}

// session is one browser tab, plus the page-side failures collected from
// it. A JavaScript exception is otherwise invisible to a Go test — and in
// this suite a broken listener IS the bug under test, so every failure
// message carries what the page complained about.
type session struct {
	ctx context.Context

	mu     sync.Mutex
	faults []string
}

// newSession opens a tab with a clean cookie jar.
func newSession(t *testing.T) *session {
	t.Helper()
	requireChrome(t)

	ctx, cancelTab := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelTab)
	ctx, cancelBudget := context.WithTimeout(ctx, sessionBudget)
	t.Cleanup(cancelBudget)

	s := &session{ctx: ctx}
	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventExceptionThrown:
			s.fault("uncaught JavaScript exception: " + e.ExceptionDetails.Error())
		case *runtime.EventConsoleAPICalled:
			if e.Type == runtime.APITypeError {
				s.fault("console.error: " + consoleText(e))
			}
		}
	})

	// Cookies live per host, not per port, so a session cookie from a
	// previous scenario's listener would reach this one's. Start clean.
	//
	// The language is pinned too: the interface negotiates it (FR-063), and
	// a suite whose behaviour depended on the developer's locale would be
	// the wrong kind of surprise.
	if err := chromedp.Run(ctx,
		network.ClearBrowserCookies(),
		network.SetExtraHTTPHeaders(network.Headers{"Accept-Language": "en-US,en;q=0.9"}),
	); err != nil {
		t.Fatalf("starting the browser (%s): %v", chromePath, err)
	}
	return s
}

// consoleText flattens a console call's arguments.
func consoleText(e *runtime.EventConsoleAPICalled) string {
	parts := make([]string, 0, len(e.Args))
	for _, a := range e.Args {
		parts = append(parts, string(a.Value))
	}
	return strings.Join(parts, " ")
}

func (s *session) fault(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, msg)
}

// diag reports the page state a failure needs: where the browser is, and
// anything the page threw on the way.
func (s *session) diag() string {
	var where string
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Evaluate(`location.href`, &where)); err != nil {
		where = "unknown (" + err.Error() + ")"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := "\n  page: " + where
	for _, f := range s.faults {
		out += "\n  " + f
	}
	return out
}

// run executes browser actions, failing the test with page diagnostics.
//
// Bounded per call, not per scenario: chromedp's query actions retry until
// their context dies, so an unbounded one would swallow the whole session
// budget and report "deadline exceeded" from wherever the clock ran out
// rather than from the selector that never appeared.
func (s *session) run(t *testing.T, what string, actions ...chromedp.Action) {
	t.Helper()
	ctx, cancel := context.WithTimeout(s.ctx, waitBudget)
	defer cancel()
	if err := chromedp.Run(ctx, actions...); err != nil {
		t.Fatalf("%s: %v%s", what, err, s.diag())
	}
}

// click sends a real mouse click on sel and confirms that the element
// actually received it, retrying only when the browser demonstrably
// delivered nothing.
//
// Real input, not a synthesized DOM event: half this suite exists because
// a listener was bound to the wrong node, and only an event travelling the
// real capture and bubble path can tell the difference.
//
// The confirmation is the part that earns its keep. A dispatched mouse
// event is hit-tested by the compositor, whose picture of the page lags
// the main thread the DOM lives on; on a page that has just swapped — and
// every page here has, the shell arms a nav-badge swap on load — a click
// at the right coordinates is occasionally routed to the document root and
// silently dropped. Retrying blind would be wrong, because half of these
// controls are toggles and the second click would undo the first. So the
// page reports what it received, and a retry happens only when nothing
// inside the target was clicked at all.
func (s *session) click(t *testing.T, what, sel string) {
	t.Helper()
	deadline := time.Now().Add(waitBudget)
	for attempt := 1; ; attempt++ {
		s.run(t, what+": bringing the target into view",
			chromedp.ScrollIntoView(sel, chromedp.NodeVisible))
		s.wait(t, what+": the shell is idle and the target is reachable",
			idle+" && "+hitTestable(sel))
		if !s.evalBool(t, what+": watching the target", watchClicks(sel)) {
			t.Fatalf("%s: nothing matches %s%s", what, sel, s.diag())
		}
		s.run(t, what, chromedp.Click(sel, chromedp.NodeVisible))

		// Delivered, on the target: done.
		if s.pollFor(clickDelivered, deliveryBudget) {
			return
		}
		// A click landed somewhere else entirely, or nowhere: the target
		// state cannot have changed, so dispatching again is safe.
		if time.Now().After(deadline) {
			t.Fatalf("%s: the browser never delivered a click to %s after %d attempts "+
				"(%d landed elsewhere)%s", what, sel, attempt,
				s.evalInt(t, "counting stray clicks", `window.__tobbyClick.elsewhere`), s.diag())
		}
	}
}

// idle is true when nothing the shell started on its own is still in
// flight: no htmx request, and no view transition covering the page.
//
// Both matter for input. The shell turns on htmx's global view
// transitions, and every page arms a nav-badge swap on load; while the
// resulting transition runs, its overlay owns the viewport and a click at
// the right coordinates reaches nothing at all. An operator never notices
// — it lasts a few hundred milliseconds — but a test that clicks into that
// window loses the click and comes back as "flaky" rather than as the
// timing fact it is.
const idle = `(() => {
	if (document.querySelector(".htmx-request") !== null) return false;
	return !document.getAnimations().some(a => a.effect && a.effect.pseudoElement &&
		String(a.effect.pseudoElement).startsWith("::view-transition"));
})()`

// deliveryBudget bounds the wait for one dispatched click to show up in the
// page. A delivered click registers within a frame; this is the point past
// which it is certain none arrived, not a guess at how long to wait.
const deliveryBudget = 3 * time.Second

// clickDelivered reads the counter armed by watchClicks — or notices that
// it is gone, which is the counter's own way of saying the click landed:
// the counter lives in the document, and the only thing that replaces the
// document here is a click that submitted a form or followed an
// unboosted link.
const clickDelivered = `(() => {
	const c = window.__tobbyClick;
	return !c || c.onTarget > 0;
})()`

// watchClicks arms a capturing counter on the document, telling clicks that
// reached sel apart from clicks that went anywhere else. Re-armed before
// every attempt so the counts belong to that attempt alone.
func watchClicks(sel string) string {
	return fmt.Sprintf(`(() => {
		const el = document.querySelector(%s);
		if (!el) return false;
		if (window.__tobbyClickOff) window.__tobbyClickOff();
		window.__tobbyClick = {onTarget: 0, elsewhere: 0};
		const on = (e) => {
			if (el === e.target || el.contains(e.target)) window.__tobbyClick.onTarget++;
			else window.__tobbyClick.elsewhere++;
		};
		document.addEventListener("click", on, true);
		window.__tobbyClickOff = () => document.removeEventListener("click", on, true);
		return true;
	})()`, jsString(sel))
}

// pollFor reports whether expr became truthy within budget. Unlike wait it
// never fails the test: the caller decides what a timeout means. A poll
// that loses its execution context to a page load is restarted, exactly as
// in wait.
func (s *session) pollFor(expr string, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithDeadline(s.ctx, deadline.Add(time.Second))
		err := chromedp.Run(ctx, chromedp.Poll(expr, nil,
			chromedp.WithPollingInterval(pollInterval),
			chromedp.WithPollingTimeout(time.Until(deadline))))
		cancel()
		if err == nil {
			return true
		}
		if !navigationRace(err) {
			return false
		}
	}
	return false
}

// hitTestable builds the predicate "a click at this element's centre lands
// on the element itself, or on something inside it".
func hitTestable(sel string) string {
	return fmt.Sprintf(`(() => {
		const el = document.querySelector(%s);
		if (!el) return false;
		const r = el.getBoundingClientRect();
		if (r.width === 0 || r.height === 0) return false;
		const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
		return !!hit && (hit === el || el.contains(hit));
	})()`, jsString(sel))
}

// wait blocks until the page-side predicate expr is truthy.
//
// This is the only waiting primitive of the suite: an observable state,
// never a duration. A navigation destroys the execution context the poll
// runs in, which is expected — the poll is simply restarted in the new
// document until the budget runs out.
func (s *session) wait(t *testing.T, what, expr string) {
	t.Helper()
	deadline := time.Now().Add(waitBudget)
	var last error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithDeadline(s.ctx, deadline)
		err := chromedp.Run(ctx, chromedp.Poll(expr, nil,
			chromedp.WithPollingInterval(pollInterval),
			chromedp.WithPollingTimeout(time.Until(deadline))))
		cancel()
		if err == nil {
			return
		}
		last = err
		if !navigationRace(err) {
			break
		}
	}
	t.Fatalf("timed out after %s waiting for %s\n  predicate: %s\n  last error: %v%s",
		waitBudget, what, expr, last, s.diag())
}

// navigationRace reports whether err is the poll losing its execution
// context to a page load — retryable, unlike a real failure.
func navigationRace(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	msg := err.Error()
	for _, s := range []string{
		"Execution context was destroyed",
		"Cannot find context with specified id",
		"Inspected target navigated or closed",
		"Execution context is not available",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// eval reads a value out of the page into res.
func (s *session) eval(t *testing.T, what, expr string, res any) {
	t.Helper()
	s.run(t, what, chromedp.Evaluate(expr, res))
}

// await evaluates an expression that yields a promise and reads what it
// resolves to — how the clipboard and the frame-settling helper are read.
func (s *session) await(t *testing.T, what, expr string, res any) {
	t.Helper()
	s.run(t, what, chromedp.Evaluate(expr, res, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	}))
}

func (s *session) evalInt(t *testing.T, what, expr string) int {
	t.Helper()
	var n int
	s.eval(t, what, expr, &n)
	return n
}

func (s *session) evalBool(t *testing.T, what, expr string) bool {
	t.Helper()
	var b bool
	s.eval(t, what, expr, &b)
	return b
}

func (s *session) evalString(t *testing.T, what, expr string) string {
	t.Helper()
	var v string
	s.eval(t, what, expr, &v)
	return v
}

func (s *session) evalFloat(t *testing.T, what, expr string) float64 {
	t.Helper()
	var v float64
	s.eval(t, what, expr, &v)
	return v
}

// settle waits for two animation frames. Everything a click scheduled —
// the listeners' microtasks, an htmx swap, style and layout — has run by
// then. It matters for the assertions that count things: B-001's duplicate
// toasts all land in the same microtask flush as the first one, so a count
// read after this is the final count, and "exactly one" means it.
//
// Still an event, not a sleep: the promise resolves when the compositor
// says so.
func (s *session) settle(t *testing.T) {
	t.Helper()
	var done bool
	s.await(t, "waiting for two animation frames",
		`new Promise(r => requestAnimationFrame(() => requestAnimationFrame(() => r(true))))`, &done)
}

// chooseOption picks an option of a native <select> and emits the change
// the control emits itself.
//
// A native dropdown is an OS widget no CDP mouse event can open, so this is
// what every browser-automation library does for a select. The synthesized
// event is a faithful stand-in for exactly one reason: it bubbles, and
// B-011 is about WHERE the listener sits. `from:find select` bound htmx to
// the FIRST select of the tasks filter form, so a change rising from the
// second one passed it by — which is what this reproduces.
func (s *session) chooseOption(t *testing.T, what, sel, value string) {
	t.Helper()
	ok := s.evalBool(t, what, fmt.Sprintf(`(() => {
		const el = document.querySelector(%q);
		if (!el) return false;
		el.value = %q;
		if (el.value !== %q) return false;
		el.dispatchEvent(new Event("change", {bubbles: true}));
		return true;
	})()`, sel, value, value))
	if !ok {
		t.Fatalf("%s: no <select> matching %q accepted the value %q%s", what, sel, value, s.diag())
	}
}

// grantClipboard authorizes clipboard access for the instance's origin.
// The copy chips go through navigator.clipboard, and the toast is raised in
// its resolution handler (B-001): without the grant the promise rejects and
// the scenario would test the failure path.
func (s *session) grantClipboard(t *testing.T, origin string) {
	t.Helper()
	s.run(t, "granting clipboard access", cdpbrowser.GrantPermissions([]cdpbrowser.PermissionType{
		cdpbrowser.PermissionTypeClipboardReadWrite,
		cdpbrowser.PermissionTypeClipboardSanitizedWrite,
	}).WithOrigin(origin))
}

// focusPage brings the tab to the front. navigator.clipboard.readText only
// answers a focused document.
func (s *session) focusPage(t *testing.T) {
	t.Helper()
	s.run(t, "focusing the page", page.BringToFront())
}

// copyChip clicks a copy control, having first made sure the clipboard can
// actually be written to.
//
// The shell swallows a rejected clipboard promise on purpose — no toast,
// no unhandled rejection — so an unfocused document produces exactly the
// same screen as a missing listener. Asserting the precondition keeps the
// scenario's failure meaning what it says.
func (s *session) copyChip(t *testing.T, what, sel string) {
	t.Helper()
	s.focusPage(t)
	s.wait(t, "the document holds focus, so the clipboard is writable",
		`document.hasFocus() === true`)
	s.click(t, what, sel)
}

// readClipboard returns what the last copy chip actually put on the
// clipboard — the one thing about a copy button no server-side test can
// check.
func (s *session) readClipboard(t *testing.T) string {
	t.Helper()
	s.focusPage(t)
	var text string
	s.await(t, "reading the clipboard", `navigator.clipboard.readText()`, &text)
	return text
}
