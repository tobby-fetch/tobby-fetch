// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ocilayout_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"testing"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/tobby-fetch/tobby-fetch/internal/ocilayout"
)

// fakeStore is an in-memory registry standing in for the embedded store:
// enough of it to exercise the format, and none of it to hide a format
// bug behind storage behaviour. The round trip against the REAL store is
// in internal/interop; what is tested here is the layout itself.
type fakeStore struct {
	manifests map[string]map[digest.Digest]storedManifest
	tags      map[string]map[string]digest.Digest
	blobs     map[digest.Digest][]byte
}

type storedManifest struct {
	payload   []byte
	mediaType string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		manifests: map[string]map[digest.Digest]storedManifest{},
		tags:      map[string]map[string]digest.Digest{},
		blobs:     map[digest.Digest][]byte{},
	}
}

func (f *fakeStore) Repositories(context.Context) ([]string, error) {
	names := make([]string, 0, len(f.manifests))
	for name := range f.manifests {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (f *fakeStore) Tags(_ context.Context, repo string) ([]string, error) {
	tags, ok := f.tags[repo]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ocilayout.ErrNotFound, repo)
	}
	names := make([]string, 0, len(tags))
	for tag := range tags {
		names = append(names, tag)
	}
	sort.Strings(names)
	return names, nil
}

func (f *fakeStore) RawManifest(_ context.Context, repo, reference string) (payload []byte, mediaType, dgst string, err error) {
	d, err := digest.Parse(reference)
	if err != nil {
		var ok bool
		d, ok = f.tags[repo][reference]
		if !ok {
			return nil, "", "", fmt.Errorf("%w: %s:%s", ocilayout.ErrNotFound, repo, reference)
		}
	}
	m, ok := f.manifests[repo][d]
	if !ok {
		return nil, "", "", fmt.Errorf("%w: %s@%s", ocilayout.ErrNotFound, repo, d)
	}
	return m.payload, m.mediaType, d.String(), nil
}

func (f *fakeStore) BlobReader(_ context.Context, _, dgst string) (io.ReadCloser, error) {
	d, err := digest.Parse(dgst)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ocilayout.ErrNotFound, dgst)
	}
	raw, ok := f.blobs[d]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ocilayout.ErrNotFound, dgst)
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (f *fakeStore) WriteBlob(_ context.Context, _ string, dgst digest.Digest, r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if got := digest.FromBytes(raw); got != dgst {
		return fmt.Errorf("blob %s hashes to %s", dgst, got)
	}
	f.blobs[dgst] = raw
	return nil
}

func (f *fakeStore) PutManifest(_ context.Context, repo, mediaType string, payload []byte, tag string) (digest.Digest, error) {
	d := digest.FromBytes(payload)
	if f.manifests[repo] == nil {
		f.manifests[repo] = map[digest.Digest]storedManifest{}
	}
	f.manifests[repo][d] = storedManifest{payload: payload, mediaType: mediaType}
	if tag != "" {
		if f.tags[repo] == nil {
			f.tags[repo] = map[string]digest.Digest{}
		}
		f.tags[repo][tag] = d
	}
	return d, nil
}

// putBlob stores content and returns the descriptor pinning it.
func (f *fakeStore) putBlob(mediaType string, content []byte) ocispec.Descriptor {
	d := digest.FromBytes(content)
	f.blobs[d] = content
	return ocispec.Descriptor{MediaType: mediaType, Digest: d, Size: int64(len(content))}
}

// put stores a manifest document under repo, optionally tagged.
func (f *fakeStore) put(t *testing.T, repo, mediaType string, doc any, tag string) ocispec.Descriptor {
	t.Helper()
	payload, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encoding manifest: %v", err)
	}
	d, err := f.PutManifest(context.Background(), repo, mediaType, payload, tag)
	if err != nil {
		t.Fatalf("storing manifest: %v", err)
	}
	return ocispec.Descriptor{MediaType: mediaType, Digest: d, Size: int64(len(payload))}
}

// image stores a one-layer image manifest and returns its descriptor.
func (f *fakeStore) image(t *testing.T, repo, tag, flavour string) ocispec.Descriptor {
	t.Helper()
	config := f.putBlob(ocispec.MediaTypeImageConfig, []byte(`{"architecture":"amd64","os":"linux","flavour":"`+flavour+`"}`))
	layer := f.putBlob(ocispec.MediaTypeImageLayerGzip, bytes.Repeat([]byte(flavour+"-layer\n"), 64))
	return f.put(t, repo, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    config,
		Layers:    []ocispec.Descriptor{layer},
	}, tag)
}

// sparseIndex stores an index listing two platforms and holding only the
// first: the FR-042 / RECIPE-SPEC §7.1 shape, where the pinned digest is
// the index's and the store carries a subset of what it lists.
func (f *fakeStore) sparseIndex(t *testing.T, repo, tag string) (indexDesc, absentDesc ocispec.Descriptor) {
	t.Helper()
	present := f.image(t, repo, "", "amd64")
	present.Platform = &ocispec.Platform{OS: "linux", Architecture: "amd64"}

	// The absent platform: its manifest is built, described, and NEVER
	// stored — exactly what a platform-filtered synchronization leaves.
	absentPayload, err := json.Marshal(ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromString("absent-config"), Size: 7},
	})
	if err != nil {
		t.Fatalf("encoding the absent manifest: %v", err)
	}
	absent := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromBytes(absentPayload),
		Size:      int64(len(absentPayload)),
		Platform:  &ocispec.Platform{OS: "linux", Architecture: "s390x"},
	}
	index := f.put(t, repo, ocispec.MediaTypeImageIndex, ocispec.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{present, absent},
	}, tag)
	return index, absent
}

// signAttached stores the classic cosign layout: an artifact tagged
// "sha256-<hex>.sig" whose layer is the SimpleSigning payload
// (RECIPE-SPEC §12.2, first bullet).
func (f *fakeStore) signAttached(t *testing.T, repo string, subject digest.Digest) ocispec.Descriptor {
	t.Helper()
	config := f.putBlob(ocispec.MediaTypeImageConfig, []byte(`{}`))
	payload := f.putBlob("application/vnd.dev.cosign.simplesigning.v1+json",
		[]byte(`{"critical":{"image":{"docker-manifest-digest":"`+subject.String()+`"}}}`))
	return f.put(t, repo, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    config,
		Layers:    []ocispec.Descriptor{payload},
	}, "sha256-"+subject.Encoded()+".sig")
}

// signBundle stores the cosign 3.x default layout: an artifact that
// REFERS to its subject, findable only through the referrers fallback
// tag "sha256-<hex>" — the half B-015 lost on the way out.
func (f *fakeStore) signBundle(t *testing.T, repo string, subject *ocispec.Descriptor) (bundle, fallback ocispec.Descriptor) {
	t.Helper()
	bundleLayer := f.putBlob("application/vnd.dev.sigstore.bundle.v0.3+json", []byte(`{"dsseEnvelope":{}}`))
	bundle = f.put(t, repo, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: "application/vnd.dev.sigstore.bundle.v0.3+json",
		Config:       ocispec.DescriptorEmptyJSON,
		Layers:       []ocispec.Descriptor{bundleLayer},
		Subject:      subject,
	}, "")
	f.blobs[ocispec.DescriptorEmptyJSON.Digest] = []byte("{}")
	bundle.ArtifactType = "application/vnd.dev.sigstore.bundle.v0.3+json"
	fallback = f.put(t, repo, ocispec.MediaTypeImageIndex, ocispec.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{bundle},
	}, "sha256-"+subject.Digest.Encoded())
	return bundle, fallback
}
