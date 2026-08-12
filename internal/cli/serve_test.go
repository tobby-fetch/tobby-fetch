// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// serveConfig builds a serving configuration with one admin account in a
// fresh state directory — the R-01 precondition of every serve test.
func serveConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Storage.Root = filepath.Join(t.TempDir(), "store")
	cfg.State.Root = filepath.Join(t.TempDir(), "state")
	store, err := auth.Open(cfg.State.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddAccount("admin", auth.RoleAdmin, "test-password", time.Now()); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestRunServeLifecycle drives the full serve path: storage prepared,
// registry mounted, probes live, then a context cancellation standing in
// for SIGTERM (FR-093) — the same code path the signal handler takes.
func TestRunServeLifecycle(t *testing.T) {
	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Shutdown.GracePeriod = config.Duration(2 * time.Second)

	// Capture stdout? runServe logs to os.Stdout by design; the lifecycle
	// assertions below go through HTTP and the return value instead.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, &cfg) }()

	// The listener address is dynamic; poll the storage root as the signal
	// that startup reached the serving phase, then probe via the registry
	// base endpoint on a fixed port is impossible — so use a fixed port.
	select {
	case err := <-done:
		t.Fatalf("runServe exited early: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	if _, err := os.Stat(cfg.Storage.Root); err != nil {
		t.Errorf("storage root was not created: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe returned %v, want nil after graceful stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("graceful stop timed out")
	}
}

// TestRunServeServesRegistry starts serve on a fixed loopback port and
// checks the mounted endpoints end to end.
func TestRunServeServesRegistry(t *testing.T) {
	cfg := serveConfig(t)
	cfg.Mode = config.ModePassthrough
	cfg.Server.Addr = "127.0.0.1:18099"
	cfg.Shutdown.GracePeriod = config.Duration(2 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, &cfg) }()

	base := "http://127.0.0.1:18099"
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(base + "/readyz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("instance never became ready")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The OCI Distribution base endpoint answers on the shared listener
	// (FR-040) — authenticated: anonymous gets the Basic challenge, the
	// local account passes (ADR-0009, FR-076).
	resp, err := http.Get(base + "/v2/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous /v2/ = %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "Basic") {
		t.Errorf("missing Basic challenge on /v2/")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("admin", "test-password")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("authenticated /v2/ = %d, want 200", resp.StatusCode)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("runServe returned %v, want nil", err)
	}
}

func TestEnsureWritableDirRejectsReadOnly(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	err := ensureWritableDir(dir)
	if err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Errorf("ensureWritableDir on read-only dir = %v, want not-writable error", err)
	}
}

// TestServeRefusesWithoutAccount is the R-01 acceptance: a fresh instance
// with authentication enabled refuses to start, with the taxonomy code and
// the policy exit class — never an open UI.
func TestServeRefusesWithoutAccount(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = config.ModeMirror
	cfg.Storage.Root = filepath.Join(t.TempDir(), "store")
	cfg.State.Root = filepath.Join(t.TempDir(), "state")
	cfg.Server.Addr = "127.0.0.1:0"

	err := runServe(context.Background(), &cfg)
	var te *taxonomy.Error
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeNoAccount {
		t.Fatalf("runServe = %v, want %s", err, taxonomy.CodeNoAccount)
	}
	if got := exitCodeFor(err); got != taxonomy.ExitPolicy {
		t.Errorf("exit code = %d, want %d (policy refusal)", got, taxonomy.ExitPolicy)
	}
}
