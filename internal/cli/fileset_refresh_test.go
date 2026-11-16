// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/fileserve"
	"github.com/tobby-fetch/tobby-fetch/internal/mediagate"
)

// resolutionCounter counts the "fileset resolution failed" records the
// refresh writes each time it resolves a declared FileSet that has no
// content: one record per resolution.
type resolutionCounter struct{ n atomic.Int64 }

func (c *resolutionCounter) Enabled(context.Context, slog.Level) bool { return true }
func (c *resolutionCounter) Handle(_ context.Context, r slog.Record) error { //nolint:gocritic // slog.Handler's signature
	if r.Message == "fileset resolution failed" {
		c.n.Add(1)
	}
	return nil
}
func (c *resolutionCounter) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *resolutionCounter) WithGroup(string) slog.Handler      { return c }

// TestAMissResolvesAtMostOncePerCooldown: the lazy resolution of a miss
// is the one trigger a client controls, so it is bounded — a client
// polling a declared FileSet whose content has not arrived costs one
// resolution per cooldown, and a name nobody declared costs nothing at
// all (B-058).
func TestAMissResolvesAtMostOncePerCooldown(t *testing.T) {
	st := openFileSetStore(t)
	counter := &resolutionCounter{}
	logger := slog.New(counter)
	fsrv := fileserve.NewServer(storeBlobs{st: st}, t.TempDir(), fileserve.Limits{}, logger)
	t.Cleanup(func() { _ = fsrv.Close() })
	cfg := fileSetConfig(config.FileSetServe{Name: "awaited", Ref: "registry.example.com/filesets/absent"})
	gate := mediagate.Open(context.Background(), t.TempDir(), "", logger)

	r := newFileSetRefresher(context.Background(), fsrv, st, cfg, gate, logger)
	clock := time.Unix(1_700_000_000, 0)
	r.now = func() time.Time { return clock }
	h := r.Files(fsrv.Handler())

	get := func(path string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody))
		return rec.Code
	}
	for range 50 {
		if code := get("/files/awaited/a.txt"); code != http.StatusNotFound {
			t.Fatalf("a FileSet with no content answered %d, want 404", code)
		}
		get("/files/never-declared/a.txt")
	}
	if n := counter.n.Load(); n != 1 {
		t.Fatalf("50 misses within the cooldown resolved %d times, want 1", n)
	}
	clock = clock.Add(fileSetMissCooldown)
	get("/files/awaited/a.txt")
	if n := counter.n.Load(); n != 2 {
		t.Fatalf("a miss after the cooldown resolved %d times in all, want 2", n)
	}
}

// TestManifestWriteParsesTheRepository pins the one place the registry
// hook reads a path: only manifest writes and deletes count, and the
// repository is everything between /v2/ and the LAST /manifests/ —
// nested names are first-class (ADR-0013).
func TestManifestWriteParsesTheRepository(t *testing.T) {
	for _, tc := range []struct {
		method, path, repo string
		ok                 bool
	}{
		{http.MethodPut, "/v2/registry.example.com/filesets/site-config/manifests/1.0.0", "registry.example.com/filesets/site-config", true},
		{http.MethodDelete, "/v2/a/manifests/b/manifests/sha256:00", "a/manifests/b", true},
		{http.MethodGet, "/v2/a/manifests/1.0.0", "", false},
		{http.MethodPut, "/v2/a/blobs/uploads/x", "", false},
		{http.MethodPut, "/v2//manifests/1.0.0", "", false},
	} {
		req := httptest.NewRequestWithContext(context.Background(), tc.method, tc.path, http.NoBody)
		repo, ok := manifestWrite(req)
		if repo != tc.repo || ok != tc.ok {
			t.Errorf("%s %s = (%q, %v), want (%q, %v)", tc.method, tc.path, repo, ok, tc.repo, tc.ok)
		}
	}
}

// slowBlobs makes every extraction take long enough for concurrent
// requests to overlap it deterministically.
type slowBlobs struct{ storeBlobs }

func (b slowBlobs) Blob(ctx context.Context, repo, dgst string) (io.ReadCloser, error) {
	time.Sleep(300 * time.Millisecond)
	return b.storeBlobs.Blob(ctx, repo, dgst)
}

// TestConcurrentMissesShareOneResolution: requests arriving together for
// a declared FileSet that is present but not yet extracted — the instant
// after a push, or the instant the media gate opens — all wait for the
// same resolution and are all served. Before B-061 only the first one
// waited; the others answered a transient 404 (and the cooldown kept them
// from starting their own).
func TestConcurrentMissesShareOneResolution(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger func(r *fileSetRefresher)
	}{
		// Nothing running yet: the first miss starts the resolution.
		{name: "misses start it", trigger: func(*fileSetRefresher) {}},
		// A background refresh is already extracting (a push, the gate
		// opening): the misses join it.
		{name: "misses join a background refresh", trigger: func(r *fileSetRefresher) { r.kick() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := openFileSetStore(t)
			src := t.TempDir()
			if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("served"), 0o600); err != nil {
				t.Fatal(err)
			}
			logger := slog.New(slog.DiscardHandler)
			res, err := fileserve.NewPacker(st, "", logger, fileserve.WithPackRoots([]string{src})).
				Pack(context.Background(), fileserve.PackRequest{Source: src, Name: "docs", Version: "1.0.0"})
			if err != nil {
				t.Fatal(err)
			}
			fsrv := fileserve.NewServer(slowBlobs{storeBlobs{st: st}}, t.TempDir(), fileserve.Limits{}, logger)
			t.Cleanup(func() { _ = fsrv.Close() })
			cfg := fileSetConfig(config.FileSetServe{Name: "docs", Ref: res.Reference, Version: "1.0.0"})
			gate := mediagate.Open(context.Background(), t.TempDir(), "", logger)
			r := newFileSetRefresher(context.Background(), fsrv, st, cfg, gate, logger)
			t.Cleanup(r.Close)
			h := r.Files(fsrv.Handler())

			tc.trigger(r)
			const n = 16
			codes := make([]int, n)
			var wg sync.WaitGroup
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/files/docs/a.txt", http.NoBody))
					codes[i] = rec.Code
				}()
			}
			wg.Wait()
			for i, c := range codes {
				if c != http.StatusOK {
					t.Errorf("request %d of %d concurrent ones answered %d, want 200 (B-061)", i+1, n, c)
				}
			}
		})
	}
}
