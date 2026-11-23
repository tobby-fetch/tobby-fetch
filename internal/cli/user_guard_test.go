// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/server"
	"github.com/tobby-fetch/tobby-fetch/internal/statedir"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// runWithStdin runs the command line with stdin, stdout and stderr wired
// to buffers.
func runWithStdin(stdin string, args ...string) (string, error) {
	root := New()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// bootTLSInstance starts a real instance on the self-signed fallback — the
// listener on which a /healthz probe through http.DefaultClient fails,
// which is how the previous guard failed open (B-093) — and returns once
// it has published its https URL in the serving marker.
func bootTLSInstance(t *testing.T, cfg *config.Config) string {
	t.Helper()
	cfg.Mode = config.ModeMirror
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Server.TLS.Enabled = true
	cfg.Shutdown.GracePeriod = config.Duration(2 * time.Second)
	srvCh := make(chan *server.Server, 1)
	serveHook = func(s *server.Server) { srvCh <- s }
	t.Cleanup(func() { serveHook = nil })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("runServe returned %v, want nil", err)
		}
	})
	select {
	case <-srvCh:
	case err := <-done:
		t.Fatalf("runServe exited before serving: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("runServe never built its server")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if m, ok := readServingMarker(cfg.State.Root); ok && strings.HasPrefix(m.URL, "https://") {
			return m.URL
		}
		if time.Now().After(deadline) {
			t.Fatal("the instance never published its https URL")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestHostAccountCommandsRefuseALiveTLSInstance: with an instance serving
// self-signed TLS on the state directory, `tobby user passwd` and
// `tobby user add` refuse and write nothing (B-082), and `tobby store
// verify` still refuses its store (B-093) — the guard is the instance's
// lock, not a probe of its URL. Once the instance stops, both work again.
func TestHostAccountCommandsRefuseALiveTLSInstance(t *testing.T) {
	cfg := serveConfig(t)
	accountsPath := statedir.Path(cfg.State.Root, statedir.Accounts)
	url := bootTLSInstance(t, &cfg)

	before, err := os.ReadFile(filepath.Clean(accountsPath))
	if err != nil {
		t.Fatal(err)
	}
	out, perr := runWithStdin("rotated\n", "user", "passwd", "admin",
		"--state-root", cfg.State.Root, "--password-stdin")
	if !isTaxonomy(perr, taxonomy.CodeConfigInvalid) ||
		!strings.Contains(perr.Error(), url) || !strings.Contains(perr.Error(), "Stop the instance") {
		t.Errorf("user passwd on a live instance = %v (%s), want a TBY-CFG-001 refusal naming %s and the way out", perr, out, url)
	}
	_, aerr := runWithStdin("pw\n", "user", "add", "late",
		"--state-root", cfg.State.Root, "--password-stdin")
	if !isTaxonomy(aerr, taxonomy.CodeConfigInvalid) || !strings.Contains(aerr.Error(), "/api/v1/accounts") {
		t.Errorf("user add on a live instance = %v, want a TBY-CFG-001 refusal naming the API", aerr)
	}
	after, err := os.ReadFile(filepath.Clean(accountsPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a refused command rewrote accounts.yaml")
	}

	// The store verification guard, on the same TLS instance.
	verifyCfg := writeConfig(t, t.TempDir(), "storage:\n  root: "+strconv.Quote(cfg.Storage.Root)+
		"\nstate:\n  root: "+strconv.Quote(cfg.State.Root)+"\n")
	_, _, verr := runSplit(t, "store", "verify", "--config", verifyCfg)
	if !isTaxonomy(verr, taxonomy.CodeConfigInvalid) || !strings.Contains(verr.Error(), "--instance "+url) {
		t.Errorf("store verify on a store served over self-signed TLS = %v, want a refusal naming --instance %s", verr, url)
	}
}

// TestHostAccountCommandsIgnoreAStaleMarker: a marker a crashed instance
// left behind — content, no lock — blocks nothing.
func TestHostAccountCommandsIgnoreAStaleMarker(t *testing.T) {
	cfg := serveConfig(t)
	stale, err := json.Marshal(servingMarker{
		StorageRoot: cfg.Storage.Root, URL: "https://127.0.0.1:1", PID: 999999,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statedir.Path(cfg.State.Root, statedir.Serving), stale, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runWithStdin("rotated\n", "user", "passwd", "admin",
		"--state-root", cfg.State.Root, "--password-stdin"); err != nil {
		t.Fatalf("user passwd beside a stale marker = %v, want success", err)
	}
	s, err := auth.Open(cfg.State.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifyPassword("admin", "rotated", time.Now()); !ok {
		t.Error("the rotated password does not verify")
	}
	if got := servedAt(&cfg); got != "" {
		t.Errorf("servedAt beside a stale marker = %q, want \"\"", got)
	}

	// store verify gets past the guard and stops at what it really finds:
	// no store there.
	verifyCfg := writeConfig(t, t.TempDir(), "storage:\n  root: "+strconv.Quote(filepath.Join(t.TempDir(), "absent"))+
		"\nstate:\n  root: "+strconv.Quote(cfg.State.Root)+"\n")
	_, _, verr := runSplit(t, "store", "verify", "--config", verifyCfg)
	if verr == nil || strings.Contains(verr.Error(), "being served") {
		t.Errorf("store verify beside a stale marker = %v, want past the serving guard", verr)
	}
}

// TestServeRefusesAHeldStateDirectory: a second holder of the state
// directory — another instance, or a `tobby user` command mid-write — makes
// a start wait, then refuse, rather than load accounts that are about to
// change behind it.
func TestServeRefusesAHeldStateDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the hold timeout")
	}
	cfg := serveConfig(t)
	lock, err := auth.LockState(cfg.State.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	if _, err := holdServing(&cfg); !isTaxonomy(err, taxonomy.CodeConfigInvalid) {
		t.Errorf("holdServing on a held state directory = %v, want TBY-CFG-001", err)
	}
}

// TestUnknownAccountPasswdIsTaxonomized: a password change for an account
// that does not exist answers TBY-AUTH-010 before asking for anything.
func TestUnknownAccountPasswdIsTaxonomized(t *testing.T) {
	cfg := serveConfig(t)
	if _, err := runWithStdin("pw\n", "user", "passwd", "ghost",
		"--state-root", cfg.State.Root, "--password-stdin"); !isTaxonomy(err, taxonomy.CodeAccountUnknown) {
		t.Errorf("user passwd ghost = %v, want TBY-AUTH-010", err)
	}
	if _, err := runWithStdin("pw\n", "user", "add", "admin",
		"--state-root", cfg.State.Root, "--password-stdin"); !isTaxonomy(err, taxonomy.CodeAccountExists) {
		t.Errorf("user add admin twice = %v, want TBY-AUTH-009", err)
	}
}
