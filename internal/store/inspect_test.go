// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestInspectionLeavesUploadsAlone: a store opened for inspection — what
// `store verify` and the media commands do — starts no upload purger, so
// an upload session older than the library's one-week horizon is still
// there afterwards: the store is byte-identical (R-31, FR-054). The
// library's purger waits a random whole number of minutes (0 to 59)
// before its first pass; opening the store a thousand times makes a
// zero-minute start — an immediate purge — all but certain, so the test
// fails whenever the purger is running.
func TestInspectionLeavesUploadsAlone(t *testing.T) {
	root := t.TempDir()
	lg := slog.New(slog.DiscardHandler)
	if _, err := OpenWith(context.Background(), root, lg, Options{Inspect: true}); err != nil {
		t.Fatal(err)
	}
	up := filepath.Join(root, "docker", "registry", "v2", "repositories", "demo", "_uploads", "0d6a2c1e-0000-4000-8000-000000000001")
	if err := os.MkdirAll(up, 0o750); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(up, "startedat"), []byte(started), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(up, "data"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for range 1000 {
		if _, err := OpenWith(ctx, root, lg, Options{Inspect: true}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(up); err != nil {
		t.Fatalf("opening the store for inspection removed an upload session: %v", err)
	}
}
