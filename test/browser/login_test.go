//go:build browser

// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package browser

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/federation"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// downFederation is an OIDC sign-in whose provider does not answer, as
// the background probe sees it (R-20).
func downFederation(t *testing.T) *federation.Service {
	t.Helper()
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	fed, err := federation.New(&federation.Options{
		OIDC: config.OIDC{Enabled: true, DisplayName: "Corp directory", Issuer: dead.URL, ClientID: "tobby",
			ClientSecretFile: secret, RedirectURL: "https://tobby.test/auth/oidc/callback",
			Roles: config.RoleMapping{Viewer: []string{"staff"}}},
		Client: &http.Client{Timeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	fed.Probe(context.Background())
	return fed
}

// TestLoginPageWithEveryMethod is the multi-method sign-in page in a real
// browser (6.3, R-20): the provider button and the local form are both
// usable; with the provider down the page says so with TBY-AUTH-016 —
// visible, not hidden behind a disabled control — and the local form
// still signs in. The button itself lands back on the page, explained,
// rather than on a browser error page.
func TestLoginPageWithEveryMethod(t *testing.T) {
	inst := newInstance(t, withFederation(downFederation(t)))
	s := newSession(t)
	s.run(t, "opening the sign-in page", chromedp.Navigate(inst.URL+"/login?next=%2Ftasks"))
	s.wait(t, "the page offers the provider and the local form",
		`document.querySelector("[data-federated-login]") !== null && document.querySelector("#password") !== null`)
	s.wait(t, "the outage is stated, visibly, with its code",
		`(() => { const b = document.querySelector("[data-idp-unavailable]");
		  return b !== null && b.offsetParent !== null && b.textContent.includes("TBY-AUTH-016") &&
		         b.textContent.includes("Corp directory"); })()`)
	if !s.evalBool(t, "the local form says its use is recorded",
		`document.querySelector("[data-local-under-federation]") !== null`) {
		t.Error("the local form does not say its use under federation is recorded")
	}

	s.click(t, "pressing the provider button", "[data-federated-login]")
	s.wait(t, "back on the sign-in page with the refusal explained",
		`location.pathname === "/auth/oidc/login" && document.querySelector(".t-error-block") !== null &&
		 document.body.textContent.includes("TBY-AUTH-016") && document.querySelector("#password") !== null`)

	inst.signIn(t, s, "/tasks")
}

// TestExpiredSessionReturnsToThePage is the R-14 journey: an idle
// session ends, the next click lands on the sign-in page with
// TBY-AUTH-005, and signing in again returns to the page that was asked
// for.
func TestExpiredSessionReturnsToThePage(t *testing.T) {
	inst := newInstance(t, withQueue(nil), withIdleTimeout(time.Second))
	s := newSession(t)
	inst.signIn(t, s, "/tasks")
	time.Sleep(1500 * time.Millisecond) // the idle timeout is the subject under test
	s.run(t, "navigating after the idle timeout", chromedp.Navigate(inst.URL+"/content"))
	s.wait(t, "the sign-in page explains the expiry",
		`location.pathname === "/login" && document.querySelector(".t-error-block") !== null &&
		 document.body.textContent.includes("TBY-AUTH-005")`)
	s.run(t, "signing in again",
		chromedp.SendKeys(chromedp.ID(`#username`), adminUser),
		chromedp.SendKeys(chromedp.ID(`#password`), adminPhrase))
	s.click(t, "submitting the sign-in form", `form.t-form button[type="submit"]`)
	s.wait(t, "back on the page that was asked for", `location.pathname === "/content"`)
}

// TestPollingTabExpires is the FR-072 amendment in a browser: a task page
// left open polls every two seconds, and still its session ends after the
// idle timeout — the polls are the page's, not the person's. The poll that
// finds the session dead takes the whole tab to the sign-in page with
// TBY-AUTH-005, set to return to the task page itself.
func TestPollingTabExpires(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	runner := func(ctx context.Context, task *tasks.Task, _ *slog.Logger, save func()) error {
		task.Items[0].Status = tasks.StatusRunning
		save()
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ctx.Err()
	}
	inst := newInstance(t, withQueue(runner), withIdleTimeout(5*time.Second))
	task, err := inst.Queue.Create(tasks.TypeUnitImport, "docker.io/library/redis:7.2", adminUser,
		[]tasks.Item{{Name: "linux/amd64", Digest: "sha256:1111"}})
	if err != nil {
		t.Fatal(err)
	}
	inst.waitStatus(t, task.ID, tasks.StatusRunning)

	s := newSession(t)
	inst.signIn(t, s, "/tasks/"+task.ID)
	s.wait(t, "the page polls on its own", taskBodyArmed)
	// Left strictly alone from here: only the page's own polls run.
	if !s.pollFor(`location.pathname === "/login" && document.body.textContent.includes("TBY-AUTH-005")`, 20*time.Second) {
		t.Fatal("a tab that only polls never expired: the polls renewed the session")
	}
	next := s.evalString(t, "reading the return target", `document.querySelector('input[name="next"]').value`)
	if next != "/tasks/"+task.ID {
		t.Errorf("the sign-in page returns to %q, want the task page %q (not a fragment URL)", next, "/tasks/"+task.ID)
	}
}
