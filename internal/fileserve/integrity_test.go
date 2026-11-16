// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package fileserve

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A1 of the milestone-7 review: the extraction checks what it reads. The
// store is a directory, and a blob replaced between the verification that
// cleared it and the extraction — or before a re-extraction after a 404 —
// was extracted as found.
//
// Proved fallible: before the fix both swaps below were extracted and
// served.

func TestExtractionRefusesAManifestThatIsNotItsDigest(t *testing.T) {
	b := newFakeBlobs()
	set := singleLayerSet(t, b, "docs", "r/docs", file("index.html", "genuine"))
	// Same key, other bytes: what a rewritten blob file looks like to the
	// read path.
	b.manifests[set.Repo+"@"+set.ManifestDigest] = append(b.manifests[set.Repo+"@"+set.ManifestDigest], ' ')
	s, dir := newTestServer(t, b, Limits{})
	if err := s.Sync(context.Background(), []FileSet{set}); err == nil {
		t.Fatal("a manifest whose bytes are not its digest was extracted")
	}
	if isComplete(setDir(dir, set)) {
		t.Error("a refused extraction was committed")
	}
	noTmpLeftovers(t, dir)
}

func TestExtractionRefusesALayerThatIsNotItsDescriptor(t *testing.T) {
	for _, tc := range []struct {
		name string
		swap func(genuine []byte, other []byte) []byte
	}{
		{"other content, same size", func(_, other []byte) []byte { return other }},
		{"trailing bytes after the tar stream", func(genuine, _ []byte) []byte { return append(append([]byte{}, genuine...), 0, 0, 0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newFakeBlobs()
			set := singleLayerSet(t, b, "docs", "r/docs", file("index.html", "genuine"))
			other := b.addLayer(t, "r/other", "gzip", []tarEntry{file("index.html", "SWAPPED")})
			for k := range b.blobs {
				if strings.HasPrefix(k, "r/docs@") && k != "r/docs@"+cfgDigest() {
					b.blobs[k] = tc.swap(b.blobs[k], b.blobs["r/other@"+other.Digest.String()])
				}
			}
			s, dir := newTestServer(t, b, Limits{})
			if err := s.Sync(context.Background(), []FileSet{set}); err == nil {
				t.Fatal("a layer that is not its descriptor was extracted")
			}
			if isComplete(setDir(dir, set)) {
				t.Error("a refused extraction was committed")
			}
			if rec := get(t, s, "/files/docs/index.html"); rec.Code == http.StatusOK {
				t.Errorf("the swapped layer is served: %q", rec.Body.String())
			}
			noTmpLeftovers(t, dir)
		})
	}
}

// TestResolveTouchesOnlyItsFileSetAndBacksOff (A6): resolving one FileSet
// leaves the others served, and a digest whose extraction failed is not
// retried before its backoff expires — while Sync, the write-side path,
// retries at once.
func TestResolveTouchesOnlyItsFileSetAndBacksOff(t *testing.T) {
	b := newFakeBlobs()
	good := singleLayerSet(t, b, "good", "r/good", file("a.txt", "a"))
	broken := FileSet{Name: "broken", Repo: "r/broken", ManifestDigest: "sha256:" + strings.Repeat("0", 64)}
	s, _ := newTestServer(t, b, Limits{})
	clock := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return clock }
	mustSync(t, s, good)

	calls := 0
	counting := &countingBlobs{Blobs: b, onManifest: func() { calls++ }}
	s.blobs = counting
	if err := s.Resolve(context.Background(), broken); err == nil {
		t.Fatal("a FileSet with no content resolved")
	}
	if err := s.Resolve(context.Background(), broken); err == nil || calls != 1 {
		t.Errorf("a second miss within the backoff extracted again (%d reads, err %v)", calls, err)
	}
	if !s.Serves("good") {
		t.Error("resolving one FileSet stopped serving another")
	}
	clock = clock.Add(resolveBackoffMin)
	_ = s.Resolve(context.Background(), broken)
	if calls != 2 {
		t.Errorf("after the backoff the digest was read %d times in all, want 2", calls)
	}
	if err := s.Sync(context.Background(), []FileSet{good, broken}); err == nil || calls != 3 {
		t.Errorf("Sync did not retry the failed digest at once (%d reads, err %v)", calls, err)
	}
	if err := s.Resolve(context.Background(), good); err != nil {
		t.Errorf("resolving a served FileSet failed: %v", err)
	}
}

type countingBlobs struct {
	Blobs
	onManifest func()
}

func (c *countingBlobs) Manifest(ctx context.Context, repo, dgst string) ([]byte, error) {
	c.onManifest()
	return c.Blobs.Manifest(ctx, repo, dgst)
}

func cfgDigest() string {
	return "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a" // digest of "{}"
}
