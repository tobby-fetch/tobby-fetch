// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package fileserve

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The directory handles a served FileSet holds, and when they are let go
// of (B-024).
//
// None of this is visible on Unix: a directory can be removed while it is
// open, so a handle nobody closes costs a descriptor and nothing else.
// Windows makes the same handle the reason the cache cannot be deleted,
// which is why these tests exist and why they assert on the handle rather
// than on the removal — a removal assertion would pass on Unix whatever
// the code did.

// TestCloseReleasesEveryServedRoot: an instance being reconfigured or
// shut down has to be able to let go of its cache directory.
func TestCloseReleasesEveryServedRoot(t *testing.T) {
	b := newFakeBlobs()
	one := singleLayerSet(t, b, "one", "example.com/one", file("a.txt", "A"))
	two := singleLayerSet(t, b, "two", "example.com/two", file("b.txt", "B"))
	s, _ := newTestServer(t, b, Limits{})
	mustSync(t, s, one, two)

	roots := make([]*os.Root, 0, 2)
	for _, ss := range s.served {
		roots = append(roots, ss.root)
	}
	if len(roots) != 2 {
		t.Fatalf("%d filesets served, want 2", len(roots))
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for i, r := range roots {
		// os.Root.Close is idempotent and answers nil either way, so the
		// question is put to the handle instead: a closed root refuses to
		// open anything through it.
		if _, err := r.Open("."); !errors.Is(err, os.ErrClosed) {
			t.Errorf("root %d was still open after Close: opening through it = %v, want %v", i, err, os.ErrClosed)
		}
	}
	if got := s.Enabled(); len(got) != 0 {
		t.Errorf("Enabled() = %v after Close, want nothing served", got)
	}
	if rec := get(t, s, "/files/one/a.txt"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d after Close, want 404", rec.Code)
	}

	// Close is not a one-way door: Sync brings the server back, which is
	// what a reconfiguration does.
	mustSync(t, s, one)
	wantBody(t, get(t, s, "/files/one/a.txt"), http.StatusOK, "A")
}

// TestReleasingAServedDigestIsScopedToThatDigest is the decision Sync
// makes before it re-extracts into a directory it may still be serving
// from.
//
// The order inside Sync — release, then clear, then rename — cannot be
// observed from the outside on Unix, because removing a directory out
// from under an open handle simply works there. What CAN be tested
// everywhere is the decision itself, and it is the half that is easy to
// get wrong in either direction: releasing nothing leaves the handle in
// the way of the removal Windows refuses (B-024), and releasing
// unconditionally takes a FileSet off the air while its replacement
// extracts, which TestConcurrentServeAndSync catches as 404s under churn.
func TestReleasingAServedDigestIsScopedToThatDigest(t *testing.T) {
	b := newFakeBlobs()
	set := singleLayerSet(t, b, "scoped", "example.com/s", file("a.txt", "A"))
	s, _ := newTestServer(t, b, Limits{})
	mustSync(t, s, set)
	held := s.served[set.Name].root

	// Another digest of the same FileSet lives in another directory: the
	// served one is not in its way and must keep answering.
	s.releaseServedDigest(set.Name, "sha256:"+strings.Repeat("f", 64))
	if _, err := held.Open("."); err != nil {
		t.Fatalf("a release for a different digest closed the served root: %v", err)
	}
	wantBody(t, get(t, s, "/files/scoped/a.txt"), http.StatusOK, "A")

	// Its own digest is the collision: the caller is about to delete this
	// very directory.
	s.releaseServedDigest(set.Name, set.ManifestDigest)
	if _, err := held.Open("."); !errors.Is(err, os.ErrClosed) {
		t.Errorf("the root was still open after its own digest was released: %v, want %v", err, os.ErrClosed)
	}
	if _, ok := s.served[set.Name]; ok {
		t.Error("the released fileset is still in the served map")
	}
}

// TestSyncRepairsAnInterruptedExtraction: the released FileSet comes back
// on the next synchronization, which is what makes the release safe.
func TestSyncRepairsAnInterruptedExtractionAfterTheRelease(t *testing.T) {
	b := newFakeBlobs()
	set := singleLayerSet(t, b, "interrupted", "example.com/i", file("a.txt", "A"))
	s, cacheDir := newTestServer(t, b, Limits{})
	mustSync(t, s, set)
	// Removing the marker is what makes the next Sync re-extract into the
	// same directory rather than into a new digest's.
	if err := os.Remove(filepath.Join(setDir(cacheDir, set), markerFile)); err != nil {
		t.Fatal(err)
	}
	mustSync(t, s, set)
	wantBody(t, get(t, s, "/files/interrupted/a.txt"), http.StatusOK, "A")
}

// The release above is scoped to the digest being re-extracted rather
// than applied to every re-extraction, because a FileSet moving to a NEW
// version extracts into a different directory and must keep answering
// from the old one until the new tree is in place. That continuity is
// what TestConcurrentServeAndSync asserts, under churn; scoping it wrong
// turns that test red immediately.
