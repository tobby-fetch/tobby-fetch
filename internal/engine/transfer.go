// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/opencontainers/go-digest"
)

// Store is the direct-to-storage write surface the engine needs
// (ADR-0005), implemented by the embedded store. Every write verifies the
// pinned digest at commit: the copy is bit-exact or it does not land.
type Store interface {
	HasManifest(ctx context.Context, repo, dgst string) bool
	ResolveTag(ctx context.Context, repo, tag string) (string, bool)
	HasBlob(ctx context.Context, repo string, dgst digest.Digest) bool
	WriteBlob(ctx context.Context, repo string, dgst digest.Digest, r io.Reader) error
	PutManifest(ctx context.Context, repo, mediaType string, payload []byte, tag string) (digest.Digest, error)
}

// copyImage streams one image manifest's blobs — only the missing ones
// (FR-026) — then the manifest itself, bit-exactly (NFR-007: streamed,
// never whole in memory). Returns the transferred byte count.
func copyImage(ctx context.Context, dst Store, repo, tag string, img v1.Image) (int64, error) {
	man, err := img.Manifest()
	if err != nil {
		return 0, err
	}
	var transferred int64
	write := func(h v1.Hash, size int64, open func() (io.ReadCloser, error)) error {
		d := digest.NewDigestFromEncoded(digest.Algorithm(h.Algorithm), h.Hex)
		if dst.HasBlob(ctx, repo, d) {
			return nil
		}
		rc, err := open()
		if err != nil {
			return err
		}
		defer rc.Close() //nolint:errcheck // read side; the digest-verified commit decides
		if err := dst.WriteBlob(ctx, repo, d, rc); err != nil {
			return err
		}
		transferred += size
		return nil
	}

	layers, err := img.Layers()
	if err != nil {
		return transferred, err
	}
	for _, l := range layers {
		h, err := l.Digest()
		if err != nil {
			return transferred, err
		}
		size, _ := l.Size()
		if err := write(h, size, l.Compressed); err != nil {
			return transferred, err
		}
	}
	rawCfg, err := img.RawConfigFile()
	if err != nil {
		return transferred, err
	}
	if err := write(man.Config.Digest, man.Config.Size, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(rawCfg)), nil
	}); err != nil {
		return transferred, err
	}
	raw, err := img.RawManifest()
	if err != nil {
		return transferred, err
	}
	if _, err := dst.PutManifest(ctx, repo, string(man.MediaType), raw, tag); err != nil {
		return transferred, err
	}
	return transferred, nil
}

// copyIndexChildren copies every child of an index — nested indexes
// recursively — untagged (the tagged object is the original index), then
// stores the ORIGINAL index bytes under the tag: the pinned digest is
// preserved even when a platform selection makes it sparse (FR-022).
// selected nil copies every child; otherwise only the listed platform
// digests are copied.
func copyIndexChildren(ctx context.Context, dst Store, repo, tag string, desc *remote.Descriptor, selected map[string]bool) (int64, error) {
	idx, err := desc.ImageIndex()
	if err != nil {
		return 0, err
	}
	man, err := idx.IndexManifest()
	if err != nil {
		return 0, err
	}
	var transferred int64
	for i := range man.Manifests {
		child := &man.Manifests[i]
		if selected != nil && !selected[child.Digest.String()] {
			continue
		}
		if dst.HasManifest(ctx, repo, child.Digest.String()) {
			continue
		}
		if child.MediaType.IsIndex() {
			nested, err := idx.ImageIndex(child.Digest)
			if err != nil {
				return transferred, err
			}
			n, err := copyNestedIndex(ctx, dst, repo, nested)
			transferred += n
			if err != nil {
				return transferred, err
			}
			continue
		}
		img, err := idx.Image(child.Digest)
		if err != nil {
			return transferred, err
		}
		n, err := copyImage(ctx, dst, repo, "", img)
		transferred += n
		if err != nil {
			return transferred, err
		}
	}
	raw, err := idx.RawManifest()
	if err != nil {
		return transferred, err
	}
	if _, err := dst.PutManifest(ctx, repo, string(desc.MediaType), raw, tag); err != nil {
		return transferred, err
	}
	return transferred, nil
}

// copyNestedIndex copies a nested index and all its children, untagged.
func copyNestedIndex(ctx context.Context, dst Store, repo string, idx v1.ImageIndex) (int64, error) {
	man, err := idx.IndexManifest()
	if err != nil {
		return 0, err
	}
	var transferred int64
	for i := range man.Manifests {
		child := &man.Manifests[i]
		if dst.HasManifest(ctx, repo, child.Digest.String()) {
			continue
		}
		if child.MediaType.IsIndex() {
			nested, err := idx.ImageIndex(child.Digest)
			if err != nil {
				return transferred, err
			}
			n, err := copyNestedIndex(ctx, dst, repo, nested)
			transferred += n
			if err != nil {
				return transferred, err
			}
			continue
		}
		img, err := idx.Image(child.Digest)
		if err != nil {
			return transferred, err
		}
		n, err := copyImage(ctx, dst, repo, "", img)
		transferred += n
		if err != nil {
			return transferred, err
		}
	}
	raw, err := idx.RawManifest()
	if err != nil {
		return transferred, err
	}
	mt, err := idx.MediaType()
	if err != nil {
		return transferred, err
	}
	if _, err := dst.PutManifest(ctx, repo, string(mt), raw, ""); err != nil {
		return transferred, err
	}
	return transferred, nil
}

// copyArtifact copies a small artifact manifest (a recipe, a cosign
// signature) with its config and layer blobs, fetched from the nominal
// repository, into dst under the given repository and tag. Artifact blobs
// are small by construction; the read is bounded.
func copyArtifact(ctx context.Context, dst Store, remotes *Remotes, nominalRepo, localRepo, tag string, manifestBytes []byte, mediaType string) error {
	var man artifactManifest
	if err := json.Unmarshal(manifestBytes, &man); err != nil {
		return fmt.Errorf("parsing artifact manifest: %w", err)
	}
	repo, _, err := remotes.Repository(nominalRepo)
	if err != nil {
		return err
	}
	blobs := []struct {
		dgst string
		size int64
	}{{man.Config.Digest, 0}}
	for _, l := range man.Layers {
		blobs = append(blobs, struct {
			dgst string
			size int64
		}{l.Digest, l.Size})
	}
	for _, b := range blobs {
		if b.dgst == "" {
			continue
		}
		d, err := digest.Parse(b.dgst)
		if err != nil {
			return fmt.Errorf("artifact blob digest %q: %w", b.dgst, err)
		}
		if dst.HasBlob(ctx, localRepo, d) {
			continue
		}
		layer, err := remote.Layer(repo.Digest(b.dgst), remotes.options(ctx)...)
		if err != nil {
			return err
		}
		rc, err := layer.Compressed()
		if err != nil {
			return err
		}
		payload, err := readBounded(rc, maxRecipeBytes)
		_ = rc.Close()
		if err != nil {
			return err
		}
		if err := dst.WriteBlob(ctx, localRepo, d, bytes.NewReader(payload)); err != nil {
			return err
		}
	}
	_, err = dst.PutManifest(ctx, localRepo, mediaType, manifestBytes, tag)
	return err
}
