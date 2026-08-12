// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package importer implements the on-demand unit import (FR-023): inspect
// a remote reference — platforms, digests, sizes, per-digest status
// against the local store (FR-022, FR-026) — then transfer the selected
// manifests bit-exactly into the embedded store, streaming, never through
// the HTTP loopback (ADR-0005).
//
// Every failure is a taxonomy entry: unparseable reference, unreachable
// registry, refused authentication, timeout, unknown reference — each a
// distinct stable code (R-03).
package importer

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Kind of the inspected artifact, detected from the config media type
// (roadmap 2.2 visual distinction).
const (
	KindImage    = "ContainerImage"
	KindChart    = "HelmChart"
	KindArtifact = "OCIArtifact"
)

// helmConfigMediaType marks a Helm chart (FR-024).
const helmConfigMediaType = "application/vnd.cncf.helm.config.v1+json"

// PlatformStatus is the per-digest status against the local store (FR-026).
type PlatformStatus string

const (
	// StatusNew means the digest is absent locally: a transfer will move it.
	StatusNew PlatformStatus = "new"
	// StatusOutdated means the local tag resolves to another digest; the
	// transfer updates the tag and moves only missing blobs.
	StatusOutdated PlatformStatus = "outdated"
	// StatusUpToDate means the digest is already local: zero transfer
	// (idempotence).
	StatusUpToDate PlatformStatus = "up-to-date"
)

// Platform is one selectable entry of the inspection report.
type Platform struct {
	// OS, Arch, Variant describe the platform ("linux", "amd64", "v7").
	// Empty for single-manifest artifacts.
	OS, Arch, Variant string
	// Digest is the platform manifest's pinned digest.
	Digest string
	// SizeBytes is the compressed transfer size (manifest + config +
	// layers).
	SizeBytes int64
	// Status is the per-digest status against the local store (FR-026).
	Status PlatformStatus
}

// Name renders the platform selector label ("linux/amd64", "linux/arm/v7").
func (p *Platform) Name() string {
	if p.OS == "" && p.Arch == "" {
		return "artifact"
	}
	s := p.OS + "/" + p.Arch
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// Report is the inspection result driving the import screen's step 2.
type Report struct {
	// Reference is the canonical source reference.
	Reference string
	// Repository is the relocated local repository (ADR-0013):
	// "docker.io/library/redis".
	Repository string
	// Tag is the resolved tag ("latest" when the reference had none).
	Tag string
	// IndexDigest is the pinned digest of the index (or of the single
	// manifest). The original index travels unmodified (FR-022).
	IndexDigest string
	// MediaType of the index or manifest.
	MediaType string
	// Kind is the detected artifact kind.
	Kind string
	// MultiArch reports an index with several platform manifests.
	MultiArch bool
	Platforms []Platform
}

// Option adjusts an inspection or a transfer.
type Option func(*options)

type options struct {
	insecure map[string]bool
}

// WithInsecureHosts marks source hosts reachable over plain HTTP or
// unverifiable TLS (registries.insecure). The opt-in is per host and
// declared in the configuration — never a global switch (FR-075); the
// private-PKI story replaces it at milestone 4 (roadmap 4.4).
func WithInsecureHosts(hosts []string) Option {
	return func(o *options) {
		for _, h := range hosts {
			o.insecure[h] = true
		}
	}
}

func buildOptions(opts []Option) *options {
	o := &options{insecure: map[string]bool{}}
	for _, fn := range opts {
		fn(o)
	}
	return o
}

// parseRef parses the reference, downgrading to the insecure scheme only
// for hosts explicitly opted in.
func (o *options) parseRef(reference string) (name.Reference, error) {
	ref, err := name.ParseReference(reference, name.WithDefaultRegistry("docker.io"))
	if err != nil {
		return nil, err
	}
	if o.insecure[ref.Context().RegistryStr()] {
		return name.ParseReference(reference, name.WithDefaultRegistry("docker.io"), name.Insecure)
	}
	return ref, nil
}

// Local answers presence questions against the embedded store. Implemented
// by the store package; nil means "nothing is local" (tests).
type Local interface {
	// HasManifest reports whether repo holds the digest.
	HasManifest(ctx context.Context, repo, digest string) bool
	// ResolveTag returns the digest repo:tag currently points at.
	ResolveTag(ctx context.Context, repo, tag string) (string, bool)
}

// Inspect fetches the reference's manifest (index or single) and builds
// the report. The caller bounds ctx (import.inspectTimeout); a deadline
// hit maps to the dedicated timeout code, distinct from unreachable
// (UI-SPEC §5.6).
func Inspect(ctx context.Context, reference string, local Local, opts ...Option) (*Report, error) {
	budget := "import.inspectTimeout"
	if dl, ok := ctx.Deadline(); ok {
		budget = time.Until(dl).Round(time.Second).String()
	}
	ref, err := buildOptions(opts).parseRef(reference)
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeBadReference,
			taxonomy.Params{"reference": reference}).WithCause(err)
	}
	repoPath, err := relocate.Path(ref.Context().Name())
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeBadReference,
			taxonomy.Params{"reference": reference}).WithCause(err)
	}

	desc, err := remote.Get(ref, remote.WithContext(ctx))
	if err != nil {
		return nil, mapRemoteErr(ctx, ref.Context().RegistryStr(), reference, budget, err)
	}

	tag := "latest"
	if t, ok := ref.(name.Tag); ok {
		tag = t.TagStr()
	}
	rep := &Report{
		Reference:   ref.Name(),
		Repository:  repoPath,
		Tag:         tag,
		IndexDigest: desc.Digest.String(),
		MediaType:   string(desc.MediaType),
	}

	if desc.MediaType.IsIndex() {
		idx, err := desc.ImageIndex()
		if err != nil {
			return nil, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
		}
		man, err := idx.IndexManifest()
		if err != nil {
			return nil, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
		}
		rep.MultiArch = true
		for i := range man.Manifests {
			m := &man.Manifests[i]
			// Attestation/unknown pseudo-platforms are not selectable
			// content platforms.
			if m.Platform == nil || m.Platform.OS == "unknown" || m.Platform.OS == "" {
				continue
			}
			p := Platform{
				OS: m.Platform.OS, Arch: m.Platform.Architecture, Variant: m.Platform.Variant,
				Digest: m.Digest.String(),
			}
			if img, err := idx.Image(m.Digest); err == nil {
				p.SizeBytes = imageSize(img)
				if rep.Kind == "" {
					rep.Kind = kindOf(img)
				}
			}
			p.Status = statusOf(ctx, local, repoPath, tag, m.Digest.String())
			rep.Platforms = append(rep.Platforms, p)
		}
		if rep.Kind == "" {
			rep.Kind = KindImage
		}
		return rep, nil
	}

	img, err := desc.Image()
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
	rep.Kind = kindOf(img)
	p := Platform{
		Digest:    desc.Digest.String(),
		SizeBytes: imageSize(img),
		Status:    statusOf(ctx, local, repoPath, tag, desc.Digest.String()),
	}
	if cfg, err := img.ConfigFile(); err == nil && cfg != nil {
		p.OS, p.Arch, p.Variant = cfg.OS, cfg.Architecture, cfg.Variant
	}
	rep.Platforms = []Platform{p}
	return rep, nil
}

// statusOf computes the FR-026 per-digest status.
func statusOf(ctx context.Context, local Local, repo, tag, digest string) PlatformStatus {
	if local == nil {
		return StatusNew
	}
	if local.HasManifest(ctx, repo, digest) {
		return StatusUpToDate
	}
	if _, ok := local.ResolveTag(ctx, repo, tag); ok {
		return StatusOutdated
	}
	return StatusNew
}

// kindOf detects the artifact kind from the config media type.
func kindOf(img v1.Image) string {
	man, err := img.Manifest()
	if err != nil || man == nil {
		return KindImage
	}
	switch {
	case string(man.Config.MediaType) == helmConfigMediaType:
		return KindChart
	case man.Config.MediaType.IsConfig():
		return KindImage
	default:
		return KindArtifact
	}
}

// imageSize sums the compressed transfer size: manifest config + layers.
func imageSize(img v1.Image) int64 {
	man, err := img.Manifest()
	if err != nil || man == nil {
		return 0
	}
	total := man.Config.Size
	for i := range man.Layers {
		total += man.Layers[i].Size
	}
	return total
}

// mapRemoteErr converts go-containerregistry failures into the taxonomy:
// timeout, authentication, unknown reference, unreachable — each its own
// stable code (R-03).
func mapRemoteErr(ctx context.Context, host, reference, budget string, err error) *taxonomy.Error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return taxonomy.New(taxonomy.CodeInspectTimeout,
			taxonomy.Params{"host": host, "timeout": budget}).WithCause(err)
	}
	var terr *transport.Error
	if errors.As(err, &terr) {
		switch terr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return taxonomy.New(taxonomy.CodeRegistryAuth,
				taxonomy.Params{"host": host}).WithCause(err)
		case http.StatusNotFound:
			return taxonomy.New(taxonomy.CodeRefNotFound,
				taxonomy.Params{"reference": reference}).WithCause(err)
		}
	}
	if strings.Contains(err.Error(), "MANIFEST_UNKNOWN") || strings.Contains(err.Error(), "NAME_UNKNOWN") {
		return taxonomy.New(taxonomy.CodeRefNotFound,
			taxonomy.Params{"reference": reference}).WithCause(err)
	}
	// Only genuine network failures read as "unreachable"; anything else is
	// an internal condition — an honest code beats a misleading one (R-03).
	var nerr net.Error
	if errors.As(err, &nerr) || errors.Is(err, syscall.ECONNREFUSED) ||
		strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "no such host") {
		return taxonomy.New(taxonomy.CodeRegistryUnreachable,
			taxonomy.Params{"host": host}).WithCause(err)
	}
	return taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
}
