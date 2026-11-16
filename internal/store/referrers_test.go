// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/opencontainers/go-digest"
)

const ociManifest = "application/vnd.oci.image.manifest.v1+json"

// referrersFixture is a store behind a real listener with one tagged
// subject in repository "app".
type referrersFixture struct {
	st      *Store
	srv     *httptest.Server
	subject string
}

func newReferrersFixture(t *testing.T) *referrersFixture {
	t.Helper()
	st, err := Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := httptest.NewServer(st.APIHandler())
	t.Cleanup(srv.Close)
	return &referrersFixture{st: st, srv: srv, subject: putTestManifest(t, st, "app", "v1", "subject layer")}
}

// referrer builds the manifest of an artifact attached to subject, with its
// own layer blob written to the repository.
func (f *referrersFixture) referrer(t *testing.T, artifactType, layer string) (raw []byte, dgst digest.Digest) {
	t.Helper()
	cfg := []byte(`{}`)
	ctx := context.Background()
	for _, b := range [][]byte{cfg, []byte(layer)} {
		if err := f.st.WriteBlob(ctx, "app", digest.FromBytes(b), bytes.NewReader(b)); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     ociManifest,
		"artifactType":  artifactType,
		"config":        map[string]any{"mediaType": "application/vnd.oci.empty.v1+json", "digest": digest.FromBytes(cfg).String(), "size": len(cfg)},
		"layers":        []map[string]any{{"mediaType": "text/plain", "digest": digest.FromBytes([]byte(layer)).String(), "size": len(layer)}},
		"subject":       map[string]any{"mediaType": ociManifest, "digest": f.subject, "size": 1},
		"annotations":   map[string]string{"note": layer},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw, digest.FromBytes(raw)
}

func (f *referrersFixture) do(t *testing.T, method, path string, body []byte, contentType string) (resp *http.Response, out []byte) {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ = io.ReadAll(resp.Body)
	return resp, out
}

func (f *referrersFixture) push(t *testing.T, artifactType, layer string) digest.Digest {
	t.Helper()
	raw, d := f.referrer(t, artifactType, layer)
	resp, body := f.do(t, http.MethodPut, "/v2/app/manifests/"+d.String(), raw, ociManifest)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT referrer: %d %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("OCI-Subject"); got != f.subject {
		t.Fatalf("OCI-Subject = %q, want %q", got, f.subject)
	}
	return d
}

type listing struct {
	MediaType string `json:"mediaType"`
	Manifests []struct {
		Digest       string            `json:"digest"`
		ArtifactType string            `json:"artifactType"`
		MediaType    string            `json:"mediaType"`
		Annotations  map[string]string `json:"annotations"`
	} `json:"manifests"`
}

func (f *referrersFixture) list(t *testing.T, subject, query string) (*http.Response, listing) {
	t.Helper()
	resp, body := f.do(t, http.MethodGet, "/v2/app/referrers/"+subject+query, nil, "")
	var l listing
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &l); err != nil {
			t.Fatalf("listing is not JSON: %v: %s", err, body)
		}
		if !strings.Contains(string(body), `"manifests":[`) {
			t.Fatalf("manifests must be an array, never null: %s", body)
		}
	}
	return resp, l
}

func TestReferrersAPI(t *testing.T) {
	f := newReferrersFixture(t)

	// Nothing attached yet: an empty image index, not a 404.
	resp, l := f.list(t, f.subject, "")
	if resp.StatusCode != http.StatusOK || len(l.Manifests) != 0 || l.MediaType != indexMediaType {
		t.Fatalf("empty listing: %d %+v", resp.StatusCode, l)
	}
	if ct := resp.Header.Get("Content-Type"); ct != indexMediaType {
		t.Errorf("Content-Type = %q", ct)
	}

	sbom := f.push(t, "application/vnd.example.sbom", "sbom bytes")
	sig := f.push(t, "application/vnd.example.signature", "sig bytes")

	resp, l = f.list(t, f.subject, "")
	if resp.StatusCode != http.StatusOK || len(l.Manifests) != 2 {
		t.Fatalf("listing: %d %+v", resp.StatusCode, l)
	}
	if resp.Header.Get("OCI-Filters-Applied") != "" {
		t.Error("OCI-Filters-Applied set without a filter")
	}
	got := map[string]string{}
	for _, m := range l.Manifests {
		got[m.Digest] = m.ArtifactType
		if m.MediaType != ociManifest || m.Annotations["note"] == "" {
			t.Errorf("descriptor lacks mediaType/annotations: %+v", m)
		}
	}
	if got[sbom.String()] != "application/vnd.example.sbom" || got[sig.String()] != "application/vnd.example.signature" {
		t.Errorf("artifact types: %v", got)
	}

	// Filtered: only the matching artifact type, and the header says so.
	resp, l = f.list(t, f.subject, "?artifactType=application/vnd.example.sbom")
	if len(l.Manifests) != 1 || l.Manifests[0].Digest != sbom.String() || resp.Header.Get("OCI-Filters-Applied") != "artifactType" {
		t.Errorf("filtered listing: %+v, filters %q", l, resp.Header.Get("OCI-Filters-Applied"))
	}
	if _, l = f.list(t, f.subject, "?artifactType=application/nothing"); len(l.Manifests) != 0 {
		t.Errorf("a filter matching nothing listed %+v", l)
	}

	// A subject nobody refers to, and one that does not exist at all.
	if resp, l = f.list(t, digest.FromString("elsewhere").String(), ""); resp.StatusCode != http.StatusOK || len(l.Manifests) != 0 {
		t.Errorf("unreferenced subject: %d %+v", resp.StatusCode, l)
	}
	if resp, _ = f.list(t, "not-a-digest", ""); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed digest: %d, want 400", resp.StatusCode)
	}
	if resp, _ := f.do(t, http.MethodGet, "/v2/nope/referrers/"+f.subject, nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown repository: %d, want 404", resp.StatusCode)
	}

	// A third-party client sees the same through its own implementation.
	ref, err := name.NewDigest(strings.TrimPrefix(f.srv.URL, "http://")+"/app@"+f.subject, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := remote.Referrers(ref)
	if err != nil {
		t.Fatal(err)
	}
	im, err := idx.IndexManifest()
	if err != nil || len(im.Manifests) != 2 {
		t.Fatalf("remote.Referrers: %v, %+v", err, im)
	}

	// Deleting a referrer removes it from the listing.
	if resp, _ := f.do(t, http.MethodDelete, "/v2/app/manifests/"+sig.String(), nil, ""); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("DELETE referrer: %d", resp.StatusCode)
	}
	if _, l = f.list(t, f.subject, ""); len(l.Manifests) != 1 || l.Manifests[0].Digest != sbom.String() {
		t.Errorf("after delete: %+v", l)
	}
}

func TestPutWithoutSubjectSetsNoHeader(t *testing.T) {
	f := newReferrersFixture(t)
	cfg := []byte(`{}`)
	raw, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": ociManifest,
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": digest.FromBytes(cfg).String(), "size": 2},
		"layers": []any{},
	})
	if err := f.st.WriteBlob(context.Background(), "app", digest.FromBytes(cfg), bytes.NewReader(cfg)); err != nil {
		t.Fatal(err)
	}
	resp, body := f.do(t, http.MethodPut, "/v2/app/manifests/plain", raw, ociManifest)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d %s", resp.StatusCode, body)
	}
	if h := resp.Header.Get("OCI-Subject"); h != "" {
		t.Errorf("OCI-Subject = %q on a manifest without subject", h)
	}
	// A body the wrapper cannot read as a manifest is still the library's
	// to refuse.
	if resp, _ = f.do(t, http.MethodPut, "/v2/app/manifests/bad", []byte("not json"), ociManifest); resp.StatusCode < 400 {
		t.Errorf("garbage manifest answered %d", resp.StatusCode)
	}
}

// The index is a cache: gone, it is rebuilt from the repository; and a
// reopened store serves the persisted one.
func TestReferrersIndexPersistsAndRebuilds(t *testing.T) {
	f := newReferrersFixture(t)
	d := f.push(t, "application/vnd.example.sbom", "persisted")
	dir := filepath.Join(f.st.root, "meta", "referrers")
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("persisted index: %v, %v", files, err)
	}

	st2, err := Open(context.Background(), f.st.root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	got, err := st2.Referrers(context.Background(), "app", digest.Digest(f.subject), "")
	if err != nil || len(got) != 1 || got[0].Digest != d {
		t.Fatalf("after reopen: %v, %+v", err, got)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	got, err = st2.Referrers(context.Background(), "app", digest.Digest(f.subject), "")
	if err != nil || len(got) != 1 || got[0].Digest != d {
		t.Fatalf("after losing meta/referrers: %v, %+v", err, got)
	}
	// A read derives the index in memory and never persists it (B-064).
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("a read persisted the rebuilt index")
	}

	// Corruption is treated like absence.
	idxFile := referrerIndexPath(f.st.root, "app")
	if err := os.MkdirAll(filepath.Dir(idxFile), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(idxFile, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err = st2.Referrers(context.Background(), "app", digest.Digest(f.subject), ""); err != nil || len(got) != 1 {
		t.Fatalf("after corruption: %v, %+v", err, got)
	}
	if err := st2.RebuildReferrers(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A manifest written through the Go API (the engine's path) is indexed too.
func TestPutManifestIndexesReferrers(t *testing.T) {
	f := newReferrersFixture(t)
	raw, d := f.referrer(t, "application/vnd.example.attestation", "attested")
	if _, err := f.st.PutManifest(context.Background(), "app", ociManifest, raw, ""); err != nil {
		t.Fatal(err)
	}
	_, l := f.list(t, f.subject, "")
	if len(l.Manifests) != 1 || l.Manifests[0].Digest != d.String() {
		t.Errorf("listing: %+v", l)
	}
}

// FR-044: a referrer lives as long as its subject, without a tag of its
// own; once the subject is unreachable the existing rule applies and the
// referrer, and the blobs only it held, are swept.
func TestSweepKeepsReferrersWhileTheSubjectLives(t *testing.T) {
	old := sweepGrace
	sweepGrace = -1
	defer func() { sweepGrace = old }()
	f := newReferrersFixture(t)
	ctx := context.Background()

	d := f.push(t, "application/vnd.example.sbom", "sbom only the referrer holds")
	// An attestation of the attestation: referrers are followed transitively.
	cfg := []byte(`{}`)
	second, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": ociManifest, "artifactType": "application/vnd.example.sig",
		"config":  map[string]any{"mediaType": "application/vnd.oci.empty.v1+json", "digest": digest.FromBytes(cfg).String(), "size": 2},
		"layers":  []any{},
		"subject": map[string]any{"mediaType": ociManifest, "digest": d.String(), "size": 1},
	})
	dd, err := f.st.PutManifest(ctx, "app", ociManifest, second, "")
	if err != nil {
		t.Fatal(err)
	}
	layerBlob := digest.FromString("sbom only the referrer holds")
	blobPath := func(dg digest.Digest) string {
		return filepath.Join(f.st.root, "docker", "registry", "v2", "blobs", "sha256", dg.Encoded()[:2], dg.Encoded(), "data")
	}
	revPath := func(dg digest.Digest) string {
		return filepath.Join(f.st.root, "docker", "registry", "v2", "repositories", "app", "_manifests", "revisions", "sha256", dg.Encoded())
	}

	if err := f.st.Sweep(ctx, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{blobPath(layerBlob), revPath(d), revPath(dd)} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("a referrer of a tagged subject was swept: %v", err)
		}
	}
	if _, l := f.list(t, f.subject, ""); len(l.Manifests) != 1 {
		t.Errorf("listing after sweep: %+v", l)
	}

	// The subject loses its tag: nothing reaches the referrers any more.
	if err := f.st.DeleteTag(ctx, "app", "v1"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{blobPath(layerBlob), revPath(d), revPath(dd)} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived the removal of its subject", p)
		}
	}
}

// B-043: the index names digests, it cannot make the sweep keep a blob the
// repository does not hold. A referrer entry planted in the index for a
// digest this repository never stored pins nothing.
func TestReferrersIndexPinsNoForeignDigest(t *testing.T) {
	old := sweepGrace
	sweepGrace = -1
	defer func() { sweepGrace = old }()
	f := newReferrersFixture(t)
	ctx := context.Background()

	putTestManifest(t, f.st, "other", "v1", "other layer")
	foreign := digest.FromString("foreign blob")
	if err := f.st.WriteBlob(ctx, "other", foreign, strings.NewReader("foreign blob")); err != nil {
		t.Fatal(err)
	}
	// Plant: subject (tagged here) -> a digest only "other" knows.
	idx := &referrerFile{Repository: "app", Referrers: map[digest.Digest][]referrerEntry{
		digest.Digest(f.subject): {{MediaType: ociManifest, Digest: foreign, Size: 12}},
	}}
	if err := writeJSON(referrerIndexPath(f.st.root, "app"), idx); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DeleteRepository(ctx, "other", slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(f.st.root, "docker", "registry", "v2", "blobs", "sha256", foreign.Encoded()[:2], foreign.Encoded())
	if _, err := os.Stat(p); err == nil {
		t.Error("a planted index entry kept a foreign blob alive past its repository")
	}
}

func TestDeleteRepositoryDropsItsIndex(t *testing.T) {
	f := newReferrersFixture(t)
	f.push(t, "application/vnd.example.sbom", "gone soon")
	if _, ok := f.st.loadReferrerIndex("app"); !ok {
		t.Fatal("no index was persisted")
	}
	if err := f.st.DeleteRepository(context.Background(), "app", slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.st.loadReferrerIndex("app"); ok {
		t.Error("the index of a removed repository survived")
	}
}

// A store opened for inspection stays byte-identical: nothing is written
// when the index has to be derived.
func TestInspectStoreWritesNoIndex(t *testing.T) {
	f := newReferrersFixture(t)
	f.push(t, "application/vnd.example.sbom", "inspected")
	dir := filepath.Join(f.st.root, "meta", "referrers")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	st, err := OpenWith(context.Background(), f.st.root, slog.New(slog.DiscardHandler), Options{Inspect: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	got, err := st.Referrers(context.Background(), "app", digest.Digest(f.subject), "")
	if err != nil || len(got) != 1 {
		t.Fatalf("%v, %+v", err, got)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("an inspection store wrote the referrers index")
	}
}
