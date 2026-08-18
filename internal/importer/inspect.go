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
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	recipev1 "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/blobfetch"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/policy"
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
	// Tag is the resolved tag: "latest" when the reference had none and
	// "latest" exists, otherwise the highest stable semver tag (B-009) or
	// the resolved chart version (FR-024).
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
	insecure  map[string]bool
	allowlist *policy.Allowlist
	egress    *netx.Egress
	// resumer opens large blobs on the resumable path (FR-029). Nil is
	// the plain streaming behavior, which is also what a zero threshold
	// or an instance without a state directory gets.
	resumer *blobfetch.Resumer
}

// WithSourcePolicy carries everything that decides which sources a unit
// import may reach: the allowlist (FR-030) and the per-host plain-HTTP
// opt-ins (registries.insecure — per host and declared, never a global
// switch, FR-075).
//
// eg is the instance's shared outbound transport — the proxy and the
// private authorities the bytes travel through (FR-080, FR-081).
//
// The three travel as one option on purpose. They are the same decision
// seen three times — "may this instance talk to that host, over what, and
// by which route" — and a call site that remembered one but not the
// others would be a call site with no allowlist and no proxy at all. A
// nil egress is the unconfigured direct one.
func WithSourcePolicy(cfg config.Registries, allow *policy.Allowlist, eg *netx.Egress) Option {
	return func(o *options) {
		o.allowlist = allow
		o.egress = netx.Or(eg)
		for _, h := range cfg.Insecure {
			o.insecure[h] = true
		}
	}
}

// WithResume gives a unit import the instance's blob resumer, so a
// multi-gigabyte layer cut at 90 % restarts at 90 % (FR-029, R-29).
//
// It is a separate option from WithSourcePolicy on purpose: the policy
// answers "may this instance reach that host, over what, and by which
// route", and it must be threaded through every call site including the
// ones in tests. Resumption answers "what happens when the link drops
// halfway", it is instance-wide state, and a call site that omits it
// degrades to today's behavior rather than losing its proxy.
func WithResume(r *blobfetch.Resumer) Option {
	return func(o *options) { o.resumer = r }
}

func buildOptions(opts []Option) *options {
	o := &options{insecure: map[string]bool{}, egress: netx.Direct()}
	for _, fn := range opts {
		// A nil Option is an unset one, not a crash: callers thread the
		// source policy through struct fields, and a zero field must
		// behave like the undeclared policy it represents.
		if fn == nil {
			continue
		}
		fn(o)
	}
	return o
}

// stripOCIScheme removes the optional "oci://" prefix — the notation Helm
// uses for OCI chart references (B-008). The canonical form is scheme-less;
// the prefix is accepted on input and never round-tripped.
func stripOCIScheme(reference string) string {
	return strings.TrimPrefix(reference, "oci://")
}

// hasExplicitVersion reports whether the reference pins a tag or a digest
// explicitly — as opposed to relying on the "latest" default (B-009).
func hasExplicitVersion(reference string) bool {
	reference = stripOCIScheme(reference)
	if strings.Contains(reference, "@") {
		return true
	}
	last := reference
	if i := strings.LastIndexByte(reference, '/'); i >= 0 {
		last = reference[i+1:]
	}
	return strings.Contains(last, ":")
}

// parseRef parses the reference, downgrading to the insecure scheme only
// for hosts explicitly opted in.
func (o *options) parseRef(reference string) (name.Reference, error) {
	reference = stripOCIScheme(reference)
	ref, err := name.ParseReference(reference, name.WithDefaultRegistry("docker.io"))
	if err != nil {
		return nil, err
	}
	// FR-030 before any connection: the reference is resolved, nothing
	// has been dialed. Checked on the registry the client will contact.
	if err := o.allowlist.Check(ref.Context().RegistryStr()); err != nil {
		return nil, err
	}
	if o.insecure[ref.Context().RegistryStr()] {
		return name.ParseReference(reference, name.WithDefaultRegistry("docker.io"), name.Insecure)
	}
	return ref, nil
}

// remoteOpts are the go-containerregistry options of every outbound read
// a unit import performs: the caller's deadline and the instance's shared
// transport (FR-080, FR-081). Every remote.* call in this package takes
// them, which is what keeps the proxy from being honored on the paths
// somebody remembered and skipped on the rest.
func (o *options) remoteOpts(ctx context.Context) []remote.Option {
	return []remote.Option{remote.WithContext(ctx), remote.WithTransport(o.egress.RoundTripper())}
}

// client returns an HTTP client on the shared transport for the plain
// HTTP(S) paths — Helm chart repositories (FR-024), which are not
// registries and would otherwise quietly acquire a transport of their
// own.
func (o *options) client() *http.Client {
	return o.egress.Client(0)
}

// Local answers presence questions against the embedded store. Implemented
// by the store package; nil means "nothing is local" (tests).
type Local interface {
	// HasManifest reports whether repo holds the digest.
	HasManifest(ctx context.Context, repo, digest string) bool
	// ResolveTag returns the digest repo:tag currently points at.
	ResolveTag(ctx context.Context, repo, tag string) (string, bool)
}

// budgetOf renders the remaining inspection budget for the timeout entry.
func budgetOf(ctx context.Context) string {
	if dl, ok := ctx.Deadline(); ok {
		return time.Until(dl).Round(time.Second).String()
	}
	return "import.inspectTimeout"
}

// Inspect fetches the reference's manifest (index or single) and builds
// the report. The caller bounds ctx (import.inspectTimeout); a deadline
// hit maps to the dedicated timeout code, distinct from unreachable
// (UI-SPEC §5.6). An https:// reference designates a Helm chart-repository
// chart (FR-024) and is inspected through its conversion.
func Inspect(ctx context.Context, reference string, local Local, opts ...Option) (*Report, error) {
	o := buildOptions(opts)
	if isChartRepoURL(reference) {
		return inspectChartRepo(ctx, reference, local, o)
	}
	budget := budgetOf(ctx)
	ref, err := o.parseRef(reference)
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeBadReference,
			taxonomy.Params{"reference": reference}).WithCause(err)
	}
	repoPath, err := relocate.Path(ref.Context().Name())
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeBadReference,
			taxonomy.Params{"reference": reference}).WithCause(err)
	}

	desc, err := remote.Get(ref, o.remoteOpts(ctx)...)
	if err != nil {
		if desc, ref, err = resolveUntagged(ctx, o, ref, reference, budget, err); err != nil {
			return nil, err
		}
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

// resolveUntagged is the B-009 fallback: when the reference carried no
// explicit tag or digest and the "latest" default does not exist — chart
// repositories tag by version, without "latest" — the tag list is resolved
// to the highest stable semver version (the FR-021 "*" rule, RECIPE-SPEC
// §9.2) and the fetch retried on it. Every other failure re-raises the
// original taxonomy error unchanged.
func resolveUntagged(ctx context.Context, o *options, ref name.Reference, reference, budget string, gerr error) (*remote.Descriptor, name.Reference, error) {
	terr := mapRemoteErr(ctx, ref.Context().RegistryStr(), reference, budget, gerr)
	if terr.Code() != taxonomy.CodeRefNotFound || hasExplicitVersion(reference) {
		return nil, nil, terr
	}
	tags, err := remote.List(ref.Context(), o.remoteOpts(ctx)...)
	if err != nil {
		return nil, nil, terr
	}
	tag, err := highestStable(tags)
	if err != nil {
		// No semver tag either: the TBY-REG-005 refusal stands, enriched
		// with what was attempted — never a silent fallback (FR-021).
		return nil, nil, taxonomy.New(taxonomy.CodeRefNotFound,
			taxonomy.Params{"reference": reference}).
			WithCause(fmt.Errorf(`tag "latest" does not exist and no stable semver tag is available: %w`, err))
	}
	resolved := ref.Context().Tag(tag)
	desc, err := remote.Get(resolved, o.remoteOpts(ctx)...)
	if err != nil {
		return nil, nil, mapRemoteErr(ctx, ref.Context().RegistryStr(), resolved.Name(), budget, err)
	}
	return desc, resolved, nil
}

// highestStable resolves the highest stable semver among tags through the
// recipe-spec SDK — the same "*" resolution rule as the recipe engine
// (RECIPE-SPEC §9.2): non-semver tags are ignored, pre-releases excluded.
func highestStable(tags []string) (string, error) {
	c, err := recipev1.ParseConstraint("*")
	if err != nil {
		return "", err
	}
	return c.Resolve(tags)
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
