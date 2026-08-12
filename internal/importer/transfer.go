// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/opencontainers/go-digest"

	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Destination is the direct-to-storage write surface (ADR-0005),
// implemented by the embedded store: every write verifies the pinned
// digest at commit — the copy is bit-exact or it does not land.
type Destination interface {
	Local
	HasBlob(ctx context.Context, repo string, dgst digest.Digest) bool
	WriteBlob(ctx context.Context, repo string, dgst digest.Digest, r io.Reader) error
	PutManifest(ctx context.Context, repo, mediaType string, payload []byte, tag string) (digest.Digest, error)
}

// NewRunner returns the unit-import task runner (FR-023): one task item
// per selected platform, incremental by digest (FR-026), the original
// index preserved and tagged — sparse when only some platforms were
// selected (FR-022). Safe to re-run: a resumed task re-transfers only what
// is missing (FR-029).
func NewRunner(dst Destination, opts ...Option) tasks.Runner {
	return func(ctx context.Context, t *tasks.Task, logger *slog.Logger, save func()) error {
		return runImport(ctx, t, dst, logger, save, opts)
	}
}

func runImport(ctx context.Context, t *tasks.Task, dst Destination, logger *slog.Logger, save func(), opts []Option) error {
	ref, err := buildOptions(opts).parseRef(t.Reference)
	if err != nil {
		return taxonomy.New(taxonomy.CodeBadReference,
			taxonomy.Params{"reference": t.Reference}).WithCause(err)
	}
	host := ref.Context().RegistryStr()
	tag := "latest"
	if tg, ok := ref.(name.Tag); ok {
		tag = tg.TagStr()
	}

	rep, err := Inspect(ctx, t.Reference, dst, opts...)
	if err != nil {
		return err
	}
	repo := rep.Repository

	// The transfer re-fetches the root by reference: the raw payload the
	// registry serves now is exactly what lands (bit-exact copy).
	desc, err := remote.Get(ref, remote.WithContext(ctx))
	if err != nil {
		return mapRemoteErr(ctx, host, t.Reference, "import.inspectTimeout", err)
	}
	var idx v1.ImageIndex
	if desc.MediaType.IsIndex() {
		if idx, err = desc.ImageIndex(); err != nil {
			return taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
		}
	}

	for i := range t.Items {
		item := &t.Items[i]
		if item.Status == tasks.StatusDone || item.Status == tasks.StatusSkipped {
			continue // resumed task: settled items stay settled (FR-029)
		}
		item.Status = tasks.StatusRunning
		save()

		// Index children are stored untagged — the index carries the tag;
		// a single manifest is the tagged object itself.
		itemTag := ""
		if idx == nil {
			itemTag = tag
		}
		err := func() error {
			img, err := imageForItem(idx, desc, item)
			if err != nil {
				return err
			}
			return importImage(ctx, dst, repo, itemTag, img, item, t, logger)
		}()
		if err != nil {
			var te *taxonomy.Error
			if !errors.As(err, &te) {
				te = mapRemoteErr(ctx, host, t.Reference, "import.inspectTimeout", err)
			}
			item.Status = tasks.StatusFailed
			item.Error = tasks.FromTaxonomy(te)
			logger.LogAttrs(ctx, slog.LevelWarn, "item failed",
				slog.String("item", item.Name), slog.String("error", te.Error()))
		}
		save()
	}

	// Multi-arch: store and tag the ORIGINAL index bytes — pinned digest
	// preserved even when the selection makes it sparse (FR-022). A fully
	// failed transfer tags nothing.
	agg := t.Aggregate()
	if idx != nil && (agg.Done > 0 || agg.Skipped > 0) {
		raw, err := idx.RawManifest()
		if err != nil {
			return taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
		}
		if _, err := dst.PutManifest(ctx, repo, string(desc.MediaType), raw, tag); err != nil {
			return taxonomy.New(taxonomy.CodeStoreWrite,
				taxonomy.Params{"detail": err.Error()}).WithCause(err)
		}
		logger.LogAttrs(ctx, slog.LevelInfo, "index stored",
			slog.String("repository", repo), slog.String("tag", tag),
			slog.String("digest", desc.Digest.String()))
	}
	return nil
}

// imageForItem resolves the platform image behind one task item.
func imageForItem(idx v1.ImageIndex, desc *remote.Descriptor, item *tasks.Item) (v1.Image, error) {
	if idx == nil {
		return desc.Image()
	}
	d, err := digest.Parse(item.Digest)
	if err != nil {
		return nil, fmt.Errorf("item %s: bad digest %q: %w", item.Name, item.Digest, err)
	}
	return idx.Image(v1.Hash{Algorithm: d.Algorithm().String(), Hex: d.Encoded()})
}

// importImage streams one manifest's blobs (only the missing ones —
// FR-026), then the manifest itself. Chart manifests first pass the FR-024
// dependency verification: a chart that cannot deploy offline is refused
// whole, before anything lands.
func importImage(ctx context.Context, dst Destination, repo, tag string, img v1.Image, item *tasks.Item, t *tasks.Task, logger *slog.Logger) error {
	man, err := img.Manifest()
	if err != nil {
		return err
	}

	if string(man.Config.MediaType) == helmConfigMediaType {
		if err := verifyChart(img, t, logger); err != nil {
			return err
		}
	}

	var transferred int64
	writeBlob := func(h v1.Hash, size int64, open func() (io.ReadCloser, error)) error {
		dgst := digest.NewDigestFromEncoded(digest.Algorithm(h.Algorithm), h.Hex)
		if dst.HasBlob(ctx, repo, dgst) {
			return nil
		}
		rc, err := open()
		if err != nil {
			return err
		}
		defer rc.Close() //nolint:errcheck // read side; the digest-verified commit decides
		if err := dst.WriteBlob(ctx, repo, dgst, rc); err != nil {
			return err
		}
		transferred += size
		return nil
	}

	layers, err := img.Layers()
	if err != nil {
		return err
	}
	for _, l := range layers {
		h, err := l.Digest()
		if err != nil {
			return err
		}
		size, _ := l.Size()
		if err := writeBlob(h, size, l.Compressed); err != nil {
			return err
		}
	}

	rawCfg, err := img.RawConfigFile()
	if err != nil {
		return err
	}
	if err := writeBlob(man.Config.Digest, man.Config.Size, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(rawCfg)), nil
	}); err != nil {
		return err
	}

	raw, err := img.RawManifest()
	if err != nil {
		return err
	}
	if _, err := dst.PutManifest(ctx, repo, string(man.MediaType), raw, tag); err != nil {
		return taxonomy.New(taxonomy.CodeStoreWrite,
			taxonomy.Params{"detail": err.Error()}).WithCause(err)
	}

	item.Status = tasks.StatusDone
	item.SizeBytes = transferred
	logger.LogAttrs(ctx, slog.LevelInfo, "manifest imported",
		slog.String("item", item.Name),
		slog.String("digest", item.Digest),
		slog.Int64("transferred_bytes", transferred))
	return nil
}
