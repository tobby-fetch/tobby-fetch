//go:build browser

// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package browser

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/ui"
)

// The single account every scenario signs in with. The UI is never exposed
// open, not even in a test (R-01), so each scenario pays the sign-in like
// an operator does — and gets the real session cookie and CSRF token the
// header forms need.
const (
	adminUser = "alexis"
	// adminPhrase is the throwaway account's sign-in phrase. It never
	// leaves the process: the account store lives in the test's temporary
	// directory and dies with it.
	adminPhrase = "e2e-throwaway-instance"
)

// t0 dates the account, matching the internal/ui fixtures.
var t0 = time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)

// instance is a throwaway Tobby web interface on loopback: real account
// store, real OCI store, real queue when a scenario needs one. Nothing is
// mocked — the point of this level is that the server is right and the
// screen is not, so a stubbed server would test nothing.
type instance struct {
	URL   string
	Store *store.Store
	Queue *tasks.Queue

	// RecipeDoc is the published recipe document, when one was seeded.
	RecipeDoc []byte
}

// spec is what newInstance was asked to provide.
type spec struct {
	content bool
	recipe  bool
	queue   bool
	runner  tasks.Runner
}

type option func(*spec)

// withContent seeds repositories of several kinds, so the Content screen
// has something to filter.
func withContent() option { return func(s *spec) { s.content = true } }

// withRecipe publishes a real recipe artifact through the publishing path
// (R-36), so the document screen reads what the command actually writes.
func withRecipe() option { return func(s *spec) { s.recipe = true } }

// withQueue attaches a task queue. A non-nil runner replaces the real
// import runner: B-002 needs a task whose progress the test decides, not
// one that races the browser.
func withQueue(runner tasks.Runner) option {
	return func(s *spec) { s.queue, s.runner = true, runner }
}

func newInstance(t *testing.T, opts ...option) *instance {
	t.Helper()
	var sp spec
	for _, o := range opts {
		o(&sp)
	}

	root := t.TempDir()
	logger := slog.New(slog.DiscardHandler)
	st, err := store.Open(context.Background(), root, logger)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing the store: %v", err)
		}
	})

	inst := &instance{Store: st}
	if sp.content {
		seedContent(t, st)
	}
	if sp.recipe {
		inst.RecipeDoc = seedRecipe(t, st)
	}
	if sp.queue {
		q, err := tasks.Open(root, logger)
		if err != nil {
			t.Fatalf("opening the queue: %v", err)
		}
		if sp.runner != nil {
			q.Register(tasks.TypeUnitImport, sp.runner)
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		q.Start(ctx)
		inst.Queue = q
	}

	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening the account store: %v", err)
	}
	if err := accounts.AddAccount(adminUser, auth.RoleAdmin, adminPhrase, t0); err != nil {
		t.Fatalf("creating the account: %v", err)
	}
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(12 * time.Hour),
		Logger:   logger,
	}

	mux := http.NewServeMux()
	ui.New(authn, logger, &ui.Options{
		Version: "0.3.0-e2e", Mode: "mirror", Store: st, Queue: inst.Queue,
	}).Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	inst.URL = srv.URL
	return inst
}

// signIn drives the real sign-in form and lands on path.
func (inst *instance) signIn(t *testing.T, s *session, path string) {
	t.Helper()
	s.run(t, "opening the sign-in page",
		chromedp.Navigate(inst.URL+"/login?next="+url.QueryEscape(path)))
	s.run(t, "filling the sign-in form",
		chromedp.WaitVisible(`#username`, chromedp.ByID),
		chromedp.SendKeys(`#username`, adminUser, chromedp.ByID),
		chromedp.SendKeys(`#password`, adminPhrase, chromedp.ByID),
	)
	s.click(t, "submitting the sign-in form", `form.t-form button[type="submit"]`)
	s.wait(t, "landing on "+path+" as a signed-in operator",
		`location.pathname === `+jsString(path)+` && document.readyState === "complete"`)
}

// waitStatus blocks until the queue publishes the wanted task status. A
// Go-side wait on real state, so the browser scenario starts from a known
// screen instead of racing the worker.
func (inst *instance) waitStatus(t *testing.T, id string, want tasks.Status) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if task, ok := inst.Queue.Get(id); ok && task.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := "absent"
	if task, ok := inst.Queue.Get(id); ok {
		got = string(task.Status)
	}
	t.Fatalf("task %s never reached status %q (last: %s)", id, want, got)
}

// waitIdle blocks until no task is active any more, so a scenario that is
// not about polling does not have to share the screen with it.
func (inst *instance) waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if inst.Queue.ActiveCount() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the queue still has %d active tasks", inst.Queue.ActiveCount())
}

// jsString quotes a Go string as a JavaScript literal. JSON string syntax
// is a subset of JavaScript's, so a marshalled string is safe to inline in
// a predicate.
func jsString(v string) string {
	out, err := json.Marshal(v)
	if err != nil { // unreachable for a string
		return `""`
	}
	return string(out)
}

// seedContent pushes, as a standard OCI client and never a protocol mock,
// a multi-arch image, a helm chart and a plain image.
func seedContent(t *testing.T, st *store.Store) {
	t.Helper()
	srv := httptest.NewServer(st.APIHandler())
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	idx := v1.ImageIndex(empty.Index)
	for _, p := range []struct{ os, arch string }{{"linux", "amd64"}, {"linux", "arm64"}} {
		img, err := random.Image(512, 1)
		if err != nil {
			t.Fatalf("building the fixture image: %v", err)
		}
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{
			Add:        img,
			Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: p.os, Architecture: p.arch}},
		})
	}
	idx = mutate.IndexMediaType(idx, types.OCIImageIndex)
	ref, err := name.ParseReference(addr + "/docker.io/bitnami/wordpress:6.4.2")
	if err != nil {
		t.Fatalf("parsing the fixture reference: %v", err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatalf("pushing the fixture index: %v", err)
	}

	chart, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("building the fixture chart: %v", err)
	}
	chartRef, err := name.ParseReference(addr + "/docker.io/bitnamicharts/wordpress:6.4.2")
	if err != nil {
		t.Fatalf("parsing the chart reference: %v", err)
	}
	if err := remote.Write(chartRef,
		mutate.ConfigMediaType(chart, "application/vnd.cncf.helm.config.v1+json")); err != nil {
		t.Fatalf("pushing the fixture chart: %v", err)
	}

	plain, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("building the fixture image: %v", err)
	}
	plainRef, err := name.ParseReference(addr + "/registry.k8s.io/coredns/coredns:1.11.1")
	if err != nil {
		t.Fatalf("parsing the image reference: %v", err)
	}
	if err := remote.Write(plainRef, plain); err != nil {
		t.Fatalf("pushing the fixture image: %v", err)
	}
}

// The recipe fixture's coordinates in the store.
const (
	recipeRepo = "cookbook/wordpress"
	recipeTag  = "6.8.2"
)

// seedRecipe publishes a recipe artifact through the real publishing path
// and returns the exact document bytes, which R-37 asserts against.
func seedRecipe(t *testing.T, st *store.Store) []byte {
	t.Helper()
	srv := httptest.NewServer(st.APIHandler())
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	doc := []byte(`apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe
metadata:
  name: wordpress
  version: 6.8.2
spec:
  ingredients:
    - name: wordpress
      kind: ContainerImage
      ref: docker.io/bitnami/wordpress
      version: 6.8.2
      digest: sha256:8acca98ed81b53b482870d6b2081e60d2aa77293895c90c97d2b0e76f469ffb1
`)
	p, err := engine.NewPublisher(config.Registries{Insecure: []string{addr}}, nil)
	if err != nil {
		t.Fatalf("building the publisher: %v", err)
	}
	if _, err := p.PublishRecipe(t.Context(), addr+"/"+recipeRepo+":"+recipeTag, doc); err != nil {
		t.Fatalf("publishing the recipe fixture: %v", err)
	}
	return doc
}
