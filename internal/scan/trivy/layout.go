// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package trivy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// imageTarget is one single-platform image manifest to hand the scanner.
type imageTarget struct {
	Digest    string
	MediaType string
	Platform  string
}

// targetsOf expands a stored manifest into the image manifests to scan: the
// manifest itself, or the children of an index that this store actually
// holds (a sparse index carries only the selected platforms, FR-022) and
// that are images — attestation manifests ("unknown/unknown") carry no
// filesystem to scan. platform, when set, narrows to one child.
func targetsOf(ctx context.Context, st Store, repo, dgst, platform string) ([]imageTarget, error) {
	m, _, err := readManifest(ctx, st, repo, dgst)
	if err != nil {
		return nil, err
	}
	if !m.isIndex() {
		return []imageTarget{{Digest: dgst, MediaType: m.MediaType}}, nil
	}
	var out []imageTarget
	for _, d := range m.Manifests {
		if d.Platform == nil || d.Platform.OS == "unknown" || d.Platform.Architecture == "unknown" {
			continue
		}
		label := d.Platform.OS + "/" + d.Platform.Architecture
		if d.Platform.Variant != "" {
			label += "/" + d.Platform.Variant
		}
		if platform != "" && label != platform && !strings.HasPrefix(label, platform+"/") {
			continue
		}
		if _, _, _, err := st.RawManifest(ctx, repo, d.Digest); err != nil {
			continue // not selected, not in the store (sparse index)
		}
		out = append(out, imageTarget{Digest: d.Digest, MediaType: d.MediaType, Platform: label})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s@%s holds no scannable image manifest", repo, dgst)
	}
	return out, nil
}

// writeLayout materializes one image from the store as an OCI image layout
// under dir — the input the scanner reads (`trivy image --input <dir>`).
// It is a copy, streamed blob by blob and verified against each digest,
// not a view: the scanner never sees the store itself, so a hostile image
// cannot make it read or write anything else of the instance.
func writeLayout(ctx context.Context, st Store, repo string, t imageTarget, dir string) error {
	m, raw, err := readManifest(ctx, st, repo, t.Digest)
	if err != nil {
		return err
	}
	blobs := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobs, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(blobs, strings.TrimPrefix(t.Digest, "sha256:")), raw, 0o600); err != nil {
		return err
	}
	descs := append([]ociDescriptor{{Digest: m.Config.Digest, Size: m.Config.Size}}, m.Layers...)
	for _, d := range descs {
		if d.Digest == "" {
			return fmt.Errorf("%s@%s is not an image manifest", repo, t.Digest)
		}
		if err := copyBlob(ctx, st, repo, d.Digest, filepath.Join(blobs, strings.TrimPrefix(d.Digest, "sha256:"))); err != nil {
			return err
		}
	}
	mediaType := t.MediaType
	if mediaType == "" {
		mediaType = m.MediaType
	}
	index, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{{
			"mediaType": mediaType,
			"digest":    t.Digest,
			"size":      len(raw),
		}},
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "index.json"), index, 0o600)
}

func copyBlob(ctx context.Context, st Store, repo, dgst, dst string) error {
	if !strings.HasPrefix(dgst, "sha256:") || strings.ContainsAny(strings.TrimPrefix(dgst, "sha256:"), `/\.`) {
		return fmt.Errorf("unsupported blob digest %q", dgst)
	}
	if _, err := os.Stat(dst); err == nil {
		return nil // the config and a layer may share a digest
	}
	rc, err := st.BlobReader(ctx, repo, dgst)
	if err != nil {
		return err
	}
	defer rc.Close() //nolint:errcheck // read side
	vr, err := newVerifiedReader(rc, dgst, maxDBFileBytes)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: a path this adapter derived from a validated digest
	if err != nil {
		return err
	}
	_, err = io.Copy(f, vr)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
