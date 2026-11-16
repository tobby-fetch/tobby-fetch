// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package mediagate

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// B-063 and R9 of the milestone-7 review: an open gate serves the verified
// set and nothing else, a verdict that vouches for nothing opens nothing,
// and a destination store that lost its manifest is guarded all the same.

// smuggle stores an image in the FileSet's repository AFTER the media
// manifest was written: on the medium, uninventoried, reached by no recipe.
func (m *medium) smuggle(t *testing.T, tag string) string {
	t.Helper()
	ctx := context.Background()
	layer := []byte("SMUGGLED")
	ld := digest.FromBytes(layer)
	cfg := []byte("{}")
	cd := digest.FromBytes(cfg)
	for d, b := range map[digest.Digest][]byte{ld: layer, cd: cfg} {
		if err := m.store.WriteBlob(ctx, m.pack.Repository, d, bytes.NewReader(b)); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": cd, "size": len(cfg)},
		"layers": []any{map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": ld, "size": len(layer)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := m.store.PutManifest(ctx, m.pack.Repository, "application/vnd.oci.image.manifest.v1+json", raw, tag)
	if err != nil {
		t.Fatal(err)
	}
	return d.String()
}

func do(t *testing.T, srv interface{ Client() *http.Client }, method, url string) (status int, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", ociAccept)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // read side
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

// TestAnOpenGateServesTheVerifiedSetOnly. Proved fallible: before the fix
// an open gate handed the whole store to /v2/, so the smuggled manifest,
// its tag and its blob all answered 200 and the catalog listed everything.
func TestAnOpenGateServesTheVerifiedSetOnly(t *testing.T) {
	m := newMedium(t)
	g := Open(context.Background(), m.root, testZone, m.logger)
	srv := m.mount(g)
	defer srv.Close()
	rep := m.verified(t)
	smuggled := m.smuggle(t, "9.9.9")
	g.Observe(rep)
	if !g.Serving() {
		t.Fatal("the gate did not open on a pushable verdict")
	}
	base := srv.URL + "/v2/" + m.pack.Repository

	for _, path := range []string{"/manifests/" + m.pack.Digest, "/manifests/" + fileSetTag} {
		if status, _ := do(t, srv, http.MethodGet, base+path); status != http.StatusOK {
			t.Errorf("GET %s = %d, want 200: it is verified content", path, status)
		}
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/manifests/" + smuggled},
		{http.MethodGet, "/manifests/9.9.9"},
		{http.MethodGet, "/blobs/" + digest.FromBytes([]byte("SMUGGLED")).String()},
		{http.MethodPut, "/manifests/evil"},
		{http.MethodPost, "/blobs/uploads/"},
		{http.MethodDelete, "/manifests/" + m.pack.Digest},
	} {
		status, body := do(t, srv, c.method, base+c.path)
		if status != http.StatusForbidden || !strings.Contains(body, string(taxonomy.CodeMediaOutsideVerified)) {
			t.Errorf("%s %s = %d (%s), want 403 %s", c.method, c.path, status, body, taxonomy.CodeMediaOutsideVerified)
		}
	}

	_, body := do(t, srv, http.MethodGet, base+"/tags/list")
	var tags struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(body), &tags); err != nil {
		t.Fatalf("tags/list: %v (%s)", err, body)
	}
	if strings.Join(tags.Tags, ",") != fileSetTag {
		t.Errorf("tags/list = %v, want only the verified %s", tags.Tags, fileSetTag)
	}
	_, body = do(t, srv, http.MethodGet, srv.URL+"/v2/_catalog")
	if !strings.Contains(body, m.pack.Repository) {
		t.Errorf("the catalog does not list the verified repository: %s", body)
	}
}

// TestAVerifiedTagKeepsItsVerifiedDigest: a tag moved on the medium after
// the verification still resolves to the digest that was verified.
func TestAVerifiedTagKeepsItsVerifiedDigest(t *testing.T) {
	m := newMedium(t)
	g := Open(context.Background(), m.root, testZone, m.logger)
	srv := m.mount(g)
	defer srv.Close()
	g.Observe(m.verified(t))
	m.smuggle(t, fileSetTag) // repoints the verified tag

	req, err := http.NewRequestWithContext(context.Background(), http.MethodHead,
		srv.URL+"/v2/"+m.pack.Repository+"/manifests/"+fileSetTag, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", ociAccept)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := resp.Header.Get("Docker-Content-Digest"); resp.StatusCode != http.StatusOK || got != m.pack.Digest {
		t.Errorf("HEAD by tag = %d %q, want 200 %s", resp.StatusCode, got, m.pack.Digest)
	}
}

// TestAVerdictThatVouchesForNothingOpensNothing: "pushable" with an empty
// served set — the verdict a medium without recipe used to get — keeps the
// gate closed.
func TestAVerdictThatVouchesForNothingOpensNothing(t *testing.T) {
	m := newMedium(t)
	g := Open(context.Background(), m.root, testZone, m.logger)
	g.Observe(&media.Report{Verdict: media.VerdictPushable})
	if g.Serving() {
		t.Error("a pushable verdict vouching for nothing opened the gate")
	}
	if e := g.Refusal("/v2/"); e == nil || e.Code() != taxonomy.CodeMediaNothingCleared {
		t.Errorf("refusal = %v, want %s", e, taxonomy.CodeMediaNothingCleared)
	}
}

// TestADestinationStoreWithoutManifestIsGuarded (R9). Proved fallible:
// before the fix the gate armed only on meta/media.json, so removing it
// served the whole store.
func TestADestinationStoreWithoutManifestIsGuarded(t *testing.T) {
	m := newMedium(t)
	if err := os.Remove(filepath.Join(m.root, filepath.FromSlash(media.ManifestPath))); err != nil {
		t.Fatal(err)
	}
	g := Open(context.Background(), m.root, testZone, m.logger)
	if !g.Guarded() {
		t.Fatal("a destination store holding content but no media manifest is not guarded")
	}
	srv := m.mount(g)
	defer srv.Close()
	if status, body := get(t, srv, m.registryURL()); status != http.StatusForbidden || !strings.Contains(body, string(taxonomy.CodeMediaUnverified)) {
		t.Errorf("GET %s = %d (%s), want 403 %s", m.registryURL(), status, body, taxonomy.CodeMediaUnverified)
	}
	rep, err := media.Verify(context.Background(), m.store, media.VerifyOptions{Zone: testZone, Trust: allowUnsigned{}})
	if err != nil {
		t.Fatal(err)
	}
	g.Observe(rep)
	if g.Serving() || rep.Verdict != media.VerdictBlocked || rep.Blocks[0].Code != taxonomy.CodeMediaManifestMissing {
		t.Errorf("verdict %q blocks %+v serving %v: want blocked on %s, closed", rep.Verdict, rep.Blocks, g.Serving(), taxonomy.CodeMediaManifestMissing)
	}
}
