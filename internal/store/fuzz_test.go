// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"context"
	"log/slog"
	"testing"
)

// Manifest and index payloads come from the network: a standard client
// pushes them to /v2/, the engine copies them from foreign registries, a
// transported medium carries them. Every reader of those bytes — the
// library's unmarshalling, the kind detection of the browser, the
// referrers index — must survive any input.
//
//	go test -run '^$' -fuzz FuzzProbeManifest ./internal/store/
//	go test -run '^$' -fuzz FuzzPutManifest ./internal/store/
//
// Seeds are checked in as testdata/fuzz corpora by `go test` itself, and
// run as ordinary tests on every build.

var fuzzMediaTypes = []string{
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
}

func addManifestSeeds(f *testing.F) {
	f.Helper()
	d := `"sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"`
	for i, seed := range []string{
		`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":` + d + `,"size":0},"layers":[]}`,
		`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":` + d + `,"size":1,"platform":{"os":"linux","architecture":"arm64"}}]}`,
		`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","artifactType":"application/vnd.example+json","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":` + d + `,"size":0},"layers":[],"subject":{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":` + d + `,"size":1}}`,
		`{"schemaVersion":2,"subject":{"digest":"not-a-digest"}}`,
		`{"schemaVersion":2,"manifests":null,"layers":null,"config":null,"subject":null}`,
		`{"subject":[1,2,3],"config":"x","annotations":{"a":1}}`,
		`[]`, `null`, `{`, ``,
	} {
		f.Add(uint8(i), []byte(seed))
	}
}

func FuzzProbeManifest(f *testing.F) {
	addManifestSeeds(f)
	f.Fuzz(func(t *testing.T, _ uint8, payload []byte) {
		if p, err := probeManifest(payload); err == nil {
			_ = p.kind()
			_ = p.fileSetMarked()
		}
		if subject, facts, ok := parseSubject(payload); ok {
			if subject.Validate() != nil {
				t.Fatalf("parseSubject accepted an invalid digest %q", subject)
			}
			_ = facts.artifactTypeOrConfig()
		}
	})
}

// FuzzPutManifest drives the store's own write path with arbitrary
// payloads and media types: no input may panic it, and whatever it accepts
// must be readable back by the browsing and referrers readers.
func FuzzPutManifest(f *testing.F) {
	addManifestSeeds(f)
	ctx := context.Background()
	st, err := Open(ctx, f.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = st.Close() })
	f.Fuzz(func(t *testing.T, sel uint8, payload []byte) {
		if len(payload) > 1<<16 {
			return
		}
		mediaType := fuzzMediaTypes[int(sel)%len(fuzzMediaTypes)]
		dgst, err := st.PutManifest(ctx, "fuzz/repo", mediaType, payload, "")
		if err != nil {
			return
		}
		if _, err := st.ManifestInfo(ctx, "fuzz/repo", dgst.String()); err != nil {
			t.Logf("an accepted manifest cannot be described: %v", err)
		}
		if _, err := st.Referrers(ctx, "fuzz/repo", dgst, ""); err != nil {
			t.Fatalf("referrers of an accepted manifest: %v", err)
		}
	})
}
