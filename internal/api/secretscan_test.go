// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
	"github.com/tobby-fetch/tobby-fetch/internal/tlsadmin"
)

// Planted-secret scan of the API surface (NFR-015).
//
// The acceptance is a scan with KNOWN planted secrets, and it covers API
// responses, not only logs and configuration dumps: a response is the one
// output channel a remote caller reads directly, and the lowest role that
// reads it is the one that matters. The scan is deliberately whole-body
// rather than field-by-field — a field added later is covered the day it
// is added, which is the only way a test like this stays true.

// plantedSecrets are the values that must appear in no response, ever.
// Distinctive on purpose: a false negative here is a real leak that a
// short or common string would hide.
var plantedSecrets = map[string]string{
	"account password": "planted-account-pw-4f21ab",
	"proxy password":   "planted-proxy-pw-9c07de",
}

// TestPlantedSecretsNeverReachAPIResponses is the NFR-015 acceptance on
// the response channel. Every surface a viewer can reach is scanned, plus
// the admin ones — an admin may read secrets they configured, but never
// get them back from an instance that promised to store hashes only.
func TestPlantedSecretsNeverReachAPIResponses(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("closing store: %v", cerr)
		}
	})

	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := accounts.AddAccount("root", auth.RoleAdmin, plantedSecrets["account password"], now); err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("lecteur", auth.RoleViewer, "pw-view", now); err != nil {
		t.Fatal(err)
	}
	// The clear token secret is shown exactly once, at creation (FR-072).
	// It must never come back from any listing afterwards.
	tokenSecret, _, err := accounts.CreateToken("robot", auth.RoleViewer, now)
	if err != nil {
		t.Fatal(err)
	}

	// A real outbound transport carrying a real proxy credential: the
	// redaction under test is netx's, not a stand-in's (FR-080).
	egress, err := netx.New(&config.Network{Proxy: config.Proxy{
		URL:      "http://proxy.example.com:3128",
		Username: "tobby",
		Password: config.NewSecret(plantedSecrets["proxy password"]),
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(egress.CloseIdleConnections)

	queue, err := tasks.Open(t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	// A FAILED task, produced by a real runner: its item error is the
	// least-examined exposure surface — a taxonomized cause, its
	// parameters, and whatever a later fix decides to add beside them,
	// all served to the viewer role.
	queue.Register(tasks.TypeSync, func(_ context.Context, tk *tasks.Task, _ *slog.Logger, save func()) error {
		tk.Items = []tasks.Item{{
			Name: "wordpress@6.8.2/app", Status: tasks.StatusFailed,
			Error: tasks.FromTaxonomy(taxonomy.New(taxonomy.CodeRegistryAuth,
				taxonomy.Params{"host": "registry.example.com"})),
		}}
		save()
		return taxonomy.New(taxonomy.CodeRegistryAuth, taxonomy.Params{"host": "registry.example.com"})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue.Start(ctx)
	task, err := queue.Create(tasks.TypeSync, "oci://cookbook.example/retriever:1", "root", nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, ok := queue.Get(task.ID)
		if ok && (snap.Status == tasks.StatusFailed || snap.Status == tasks.StatusDone) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fixture task never finished: the failure surface is not being scanned")
		}
		time.Sleep(10 * time.Millisecond)
	}

	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(time.Hour),
		Logger:   slog.New(slog.DiscardHandler),
	}
	a := api.New(authn, slog.New(slog.DiscardHandler))
	api.RegisterContent(a, st, nil)
	api.RegisterTasks(a, queue, st, time.Second, nil)
	api.RegisterAccounts(a, accounts)
	api.RegisterRecipes(a, &api.RecipeOptions{Store: st, Queue: queue, Source: testSource})
	api.RegisterNetwork(a, &api.NetworkOptions{Egress: tlsadmin.Egress(egress)})
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())

	secrets := map[string]string{}
	for k, v := range plantedSecrets {
		secrets[k] = v
	}
	secrets["static token secret"] = tokenSecret

	for _, probe := range []struct{ path, user, pass string }{
		// Viewer-reachable surfaces first: the lowest role that can read
		// a response is the one a leak is measured against.
		{"/api/v1/tasks", "lecteur", "pw-view"},
		{"/api/v1/tasks/" + task.ID, "lecteur", "pw-view"},
		{"/api/v1/content", "lecteur", "pw-view"},
		{"/api/v1/recipes", "lecteur", "pw-view"},
		// Admin surfaces: configuring a secret is not being handed it back.
		{"/api/v1/accounts", "root", plantedSecrets["account password"]},
		{"/api/v1/tokens", "root", plantedSecrets["account password"]},
		{"/api/v1/network", "root", plantedSecrets["account password"]},
		{"/api/v1/retriever", "root", plantedSecrets["account password"]},
	} {
		w := call(t, mux, http.MethodGet, probe.path, probe.user, probe.pass, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", probe.path, w.Code, w.Body.String())
		}
		body := w.Body.String()
		for what, secret := range secrets {
			if strings.Contains(body, secret) {
				t.Errorf("GET %s leaks the planted %s (NFR-015)", probe.path, what)
			}
		}
	}
}
