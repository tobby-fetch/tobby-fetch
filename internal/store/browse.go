// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Browsing accessors over the embedded store (FR-062, R-06): repository
// listing, tag and manifest details, and store-wide counters, consumed by
// the /content screens and their /api/v1/content mirror (FR-061).
//
// The accessors read the storage backend through the distribution library
// (a second registry over the same root directory, built in Open) — never
// through the HTTP loopback (package doc). This also lifts the HTTP
// catalog's 1000-entry ceiling: Repositories loops over the storage-level
// pagination until exhaustion.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	distribution "github.com/distribution/distribution/v3"
	storagedriver "github.com/distribution/distribution/v3/registry/storage/driver"
	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ErrNotFound reports a repository, tag, or manifest absent from the local
// store. Handlers map it onto the taxonomized 404 (TBY-SRV-002); every
// other accessor error is a store read failure (TBY-STO-001).
var ErrNotFound = errors.New("store: not found")

// Kind classifies an artifact by its manifest media types — the five
// ingredient kinds of the recipe format (RECIPE-SPEC §7). Kind names are a
// frozen glossary: they are never translated (ADR-0015 §7).
type Kind string

// The five ingredient kinds (RECIPE-SPEC §7), detected from the manifest's
// artifactType and config media type.
const (
	KindContainerImage Kind = "ContainerImage"
	KindHelmChart      Kind = "HelmChart"
	KindOCIArtifact    Kind = "OCIArtifact"
	KindFileSet        Kind = "FileSet"
	KindRecipe         Kind = "Recipe"
)

// Kinds lists the five ingredient kinds in display order — the /content
// kind filter iterates on it (R-06).
func Kinds() []Kind {
	return []Kind{KindContainerImage, KindHelmChart, KindOCIArtifact, KindFileSet, KindRecipe}
}

// Media types driving kind detection. The helm config media type is the
// CNCF-registered one; the recipe artifact type is fixed by RECIPE-SPEC
// §11.2 (artifactType, with the config slot empty per the OCI 1.1 artifact
// guidance — older publishers may carry it as the config media type).
const (
	mediaTypeHelmConfig   = "application/vnd.cncf.helm.config.v1+json"
	mediaTypeRecipe       = "application/vnd.tobby.recipe.v1+yaml"
	mediaTypeOCIConfig    = "application/vnd.oci.image.config.v1+json"
	mediaTypeDockerConfig = "application/vnd.docker.container.image.v1+json"
)

// TagInfo is one tag of a repository as the tag table shows it (UI-SPEC
// §5.4): pinned digest, logical size, platform counts, manifest media type.
type TagInfo struct {
	Tag    string
	Digest string
	Size   int64
	// Platforms counts the index entries; Present counts those whose
	// manifest is locally held — a sparse index (FR-022) keeps them apart,
	// and the UI shows "present/total", never the total alone (B-007).
	Platforms int
	Present   int
	MediaType string
}

// RepoInfo is the detail of one repository: its tags sorted by tag,
// descending, the detected kind, and the logical size of the distinct
// manifests its tags reference (two tags on one digest count once; blobs
// shared across digests still count per digest — physical deduplication is
// Counts.PhysicalBytes).
type RepoInfo struct {
	Name string
	Kind Kind
	Tags []TagInfo
	Size int64
}

// PlatformInfo is one platform entry of a manifest detail. For an index it
// mirrors one index entry; Present is false when the referenced manifest
// is not in the store — a sparse index (FR-022) keeps the original index
// digest pinned without carrying every platform. A plain manifest yields a
// single entry, Present, with the platform read from its image config when
// there is one.
type PlatformInfo struct {
	OS      string
	Arch    string
	Variant string
	Digest  string
	Size    int64
	Present bool
}

// ManifestInfo is the detail of one manifest or index (UI-SPEC §5.5).
type ManifestInfo struct {
	Repo string
	// Tag is the addressed tag; empty when addressed by digest.
	Tag string
	// Digest is the pinned digest of the index or manifest (FR-022: the
	// original index digest never changes with local platform coverage).
	Digest      string
	MediaType   string
	Kind        Kind
	IsIndex     bool
	Size        int64
	Annotations map[string]string
	Platforms   []PlatformInfo
}

// Counts is the store-wide counter set of the dashboard tile (FR-062).
// PhysicalBytes is the on-disk size of the blob directory: real,
// deduplicated bytes, unlike the logical sizes of RepoInfo.
type Counts struct {
	Repositories  int
	Tags          int
	PhysicalBytes int64
}

// catalogPageSize is the internal page size of Repositories. A variable so
// the pagination loop is testable; the value never limits results.
var catalogPageSize = 256

// Repositories returns every repository of the store, sorted, by looping
// over the storage catalog's pagination — the 1000-entry ceiling belongs
// to the HTTP catalog endpoint, not to the backend.
func (s *Store) Repositories(ctx context.Context) ([]string, error) {
	var all []string
	last := ""
	buf := make([]string, catalogPageSize)
	for {
		n, err := s.browse.Repositories(ctx, buf, last)
		all = append(all, buf[:n]...)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return all, nil
			}
			// A store that never received a push has no repositories root
			// yet: an empty catalog, not a failure.
			var pathErr storagedriver.PathNotFoundError
			if errors.As(err, &pathErr) {
				return all, nil
			}
			return nil, fmt.Errorf("store: listing repositories: %w", err)
		}
		if n == 0 {
			return all, nil
		}
		last = all[len(all)-1]
	}
}

// RepoInfo returns the tag table of one repository. Unknown or invalid
// names yield ErrNotFound.
func (s *Store) RepoInfo(ctx context.Context, name string) (*RepoInfo, error) {
	repo, err := s.repository(ctx, name)
	if err != nil {
		return nil, err
	}
	tags, err := repo.Tags(ctx).All(ctx)
	if err != nil {
		return nil, mapBrowseErr("listing tags of "+name, err)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(tags)))

	ms, err := repo.Manifests(ctx)
	if err != nil {
		return nil, mapBrowseErr("opening manifests of "+name, err)
	}

	info := &RepoInfo{Name: name, Kind: KindOCIArtifact}
	cache := map[digest.Digest]*manifestSummary{}
	for _, tag := range tags {
		desc, err := repo.Tags(ctx).Get(ctx, tag)
		if err != nil {
			return nil, mapBrowseErr("resolving tag "+tag+" of "+name, err)
		}
		sum, ok := cache[desc.Digest]
		if !ok {
			sum, err = s.summarize(ctx, ms, desc.Digest)
			if err != nil {
				return nil, mapBrowseErr("reading manifest "+desc.Digest.String()+" of "+name, err)
			}
			cache[desc.Digest] = sum
			info.Size += sum.size
		}
		info.Tags = append(info.Tags, TagInfo{
			Tag:       tag,
			Digest:    desc.Digest.String(),
			Size:      sum.size,
			Platforms: sum.platforms,
			Present:   sum.present,
			MediaType: sum.mediaType,
		})
	}
	if len(info.Tags) > 0 {
		info.Kind = cache[digest.Digest(info.Tags[0].Digest)].kind
	}
	return info, nil
}

// ManifestInfo returns the detail of one manifest, addressed by tag or by
// digest. Unknown repository, tag, or digest yield ErrNotFound.
func (s *Store) ManifestInfo(ctx context.Context, name, tagOrDigest string) (*ManifestInfo, error) {
	repo, err := s.repository(ctx, name)
	if err != nil {
		return nil, err
	}
	ms, err := repo.Manifests(ctx)
	if err != nil {
		return nil, mapBrowseErr("opening manifests of "+name, err)
	}

	info := &ManifestInfo{Repo: name}
	dgst, err := digest.Parse(tagOrDigest)
	if err != nil {
		info.Tag = tagOrDigest
		desc, err := repo.Tags(ctx).Get(ctx, tagOrDigest)
		if err != nil {
			return nil, mapBrowseErr("resolving tag "+tagOrDigest+" of "+name, err)
		}
		dgst = desc.Digest
	}
	info.Digest = dgst.String()

	m, err := ms.Get(ctx, dgst)
	if err != nil {
		return nil, mapBrowseErr("reading manifest "+dgst.String()+" of "+name, err)
	}
	mediaType, payload, err := m.Payload()
	if err != nil {
		return nil, fmt.Errorf("store: manifest payload of %s: %w", name, err)
	}
	probe, err := probeManifest(payload)
	if err != nil {
		return nil, fmt.Errorf("store: parsing manifest of %s: %w", name, err)
	}
	info.MediaType = mediaType
	info.Annotations = probe.Annotations
	info.Size = int64(len(payload))

	if !isIndex(mediaType) {
		info.Kind = probe.kind()
		p := PlatformInfo{Digest: dgst.String(), Present: true, Size: manifestTotal(payload, m)}
		p.OS, p.Arch, p.Variant = s.configPlatform(ctx, repo, probe)
		info.Size = p.Size
		info.Platforms = []PlatformInfo{p}
		return info, nil
	}

	info.IsIndex = true
	for _, c := range m.References() {
		p := PlatformInfo{Digest: c.Digest.String(), Size: c.Size}
		if c.Platform != nil {
			p.OS, p.Arch, p.Variant = c.Platform.OS, c.Platform.Architecture, c.Platform.Variant
		}
		present, err := ms.Exists(ctx, c.Digest)
		if err != nil {
			return nil, mapBrowseErr("checking manifest "+c.Digest.String()+" of "+name, err)
		}
		if present {
			p.Present = true
			cm, err := ms.Get(ctx, c.Digest)
			if err != nil {
				return nil, mapBrowseErr("reading manifest "+c.Digest.String()+" of "+name, err)
			}
			_, cp, err := cm.Payload()
			if err != nil {
				return nil, fmt.Errorf("store: manifest payload of %s: %w", name, err)
			}
			p.Size = manifestTotal(cp, cm)
			info.Size += p.Size
			if info.Kind == "" {
				if childProbe, err := probeManifest(cp); err == nil {
					info.Kind = childProbe.kind()
				}
			}
		}
		info.Platforms = append(info.Platforms, p)
	}
	if info.Kind == "" {
		// A fully sparse index carries no local child to probe; an OCI
		// image index is overwhelmingly a container image.
		info.Kind = KindContainerImage
	}
	return info, nil
}

// Counts returns the store-wide counters of the dashboard tile.
func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	repos, err := s.Repositories(ctx)
	if err != nil {
		return c, err
	}
	c.Repositories = len(repos)
	for _, name := range repos {
		repo, err := s.repository(ctx, name)
		if err != nil {
			return c, err
		}
		tags, err := repo.Tags(ctx).All(ctx)
		if err != nil {
			if errors.Is(mapBrowseErr("", err), ErrNotFound) {
				continue // repository without any tag
			}
			return c, mapBrowseErr("listing tags of "+name, err)
		}
		c.Tags += len(tags)
	}
	c.PhysicalBytes, err = dirSize(filepath.Join(s.root, "docker", "registry", "v2", "blobs"))
	if err != nil {
		return c, fmt.Errorf("store: sizing blob directory: %w", err)
	}
	return c, nil
}

// BrowsePageSize is the server-side page size of the repository listing
// (UI-SPEC §5.3), shared by the /content screen and its API mirror.
const BrowsePageSize = 25

// BrowseQuery is the filter set of the repository listing. Its parameter
// names (q, kind, prefix, page) are the shared UI/API contract (FR-061,
// R-06): both surfaces parse them with ParseBrowseQuery, so parity holds
// by construction.
type BrowseQuery struct {
	// Q filters by substring on the full relocated path.
	Q string
	// Kinds keeps only the listed kinds (multi-valued "kind" parameter).
	Kinds []Kind
	// Prefix keeps repositories under an exact path prefix — the target of
	// the breadcrumb segments (UI-SPEC §5.4).
	Prefix string
	// Page is the 1-based page of BrowsePageSize entries.
	Page int
}

// ParseBrowseQuery reads a BrowseQuery from URL parameters — the single
// parser behind the /content screen and /api/v1/content (FR-061).
func ParseBrowseQuery(v url.Values) BrowseQuery {
	q := BrowseQuery{Q: v.Get("q"), Prefix: v.Get("prefix"), Page: 1}
	if p, err := strconv.Atoi(v.Get("page")); err == nil && p > 1 {
		q.Page = p
	}
	for _, k := range v["kind"] {
		if k != "" {
			q.Kinds = append(q.Kinds, Kind(k))
		}
	}
	return q
}

// Values renders the query back to URL parameters (pagination links).
func (q BrowseQuery) Values() url.Values {
	v := url.Values{}
	if q.Q != "" {
		v.Set("q", q.Q)
	}
	for _, k := range q.Kinds {
		v.Add("kind", string(k))
	}
	if q.Prefix != "" {
		v.Set("prefix", q.Prefix)
	}
	if q.Page > 1 {
		v.Set("page", strconv.Itoa(q.Page))
	}
	return v
}

// HasFilter reports whether any filter narrows the listing — it separates
// the "store empty" state from "no result for these filters" (UI-SPEC).
func (q BrowseQuery) HasFilter() bool {
	return q.Q != "" || q.Prefix != "" || len(q.Kinds) > 0
}

// RepoSummary is one row of the repository listing.
type RepoSummary struct {
	Name string
	Kind Kind
	Tags int
	Size int64
}

// BrowsePage is one page of the filtered repository listing.
type BrowsePage struct {
	Repos      []RepoSummary
	Total      int
	Page       int
	TotalPages int
}

// Browse lists repositories filtered by q — the single implementation
// behind the /content screen and its API mirror (FR-061). Kind filtering
// summarizes every matching repository before paginating; fine at this
// milestone's store sizes, an index arrives with the media manifest work.
func (s *Store) Browse(ctx context.Context, q BrowseQuery) (*BrowsePage, error) {
	names, err := s.Repositories(ctx)
	if err != nil {
		return nil, err
	}

	var matched []RepoSummary
	for _, name := range names {
		if q.Q != "" && !strings.Contains(name, q.Q) {
			continue
		}
		if q.Prefix != "" && name != q.Prefix && !strings.HasPrefix(name, q.Prefix+"/") {
			continue
		}
		info, err := s.RepoInfo(ctx, name)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue // repository vanished between listing and detail
			}
			return nil, err
		}
		if len(q.Kinds) > 0 && !kindIn(info.Kind, q.Kinds) {
			continue
		}
		matched = append(matched, RepoSummary{
			Name: name, Kind: info.Kind, Tags: len(info.Tags), Size: info.Size,
		})
	}

	page := &BrowsePage{Total: len(matched), Page: q.Page}
	start, end, totalPages := paginate(len(matched), q.Page, BrowsePageSize)
	page.TotalPages = totalPages
	page.Repos = matched[start:end]
	return page, nil
}

// paginate computes the half-open [start, end) window of the 1-based page
// over total entries, and the page count (at least 1). An out-of-range
// page yields an empty window, never an error: the screen shows its
// "no result" state and the API mirrors it (FR-061).
func paginate(total, page, size int) (start, end, totalPages int) {
	totalPages = (total + size - 1) / size
	if totalPages == 0 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	start = (page - 1) * size
	if start > total {
		return total, total, totalPages
	}
	end = min(start+size, total)
	return start, end, totalPages
}

func kindIn(k Kind, kinds []Kind) bool {
	for _, c := range kinds {
		if c == k {
			return true
		}
	}
	return false
}

// repository resolves a repository handle; invalid names are ErrNotFound
// (an invalid name cannot exist in the store).
func (s *Store) repository(ctx context.Context, name string) (distribution.Repository, error) {
	named, err := reference.WithName(name)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid repository name %q", ErrNotFound, name)
	}
	repo, err := s.browse.Repository(ctx, named)
	if err != nil {
		return nil, mapBrowseErr("opening repository "+name, err)
	}
	return repo, nil
}

// manifestSummary is the per-digest aggregate behind RepoInfo.
type manifestSummary struct {
	mediaType string
	size      int64
	platforms int
	present   int
	kind      Kind
}

// summarize aggregates one manifest or index: logical size (manifest bytes
// plus config and layer bytes; for an index, only locally present children
// count — FR-022 sparse entries have no local bytes), platform count, and
// detected kind.
func (s *Store) summarize(ctx context.Context, ms distribution.ManifestService, dgst digest.Digest) (*manifestSummary, error) {
	m, err := ms.Get(ctx, dgst)
	if err != nil {
		return nil, err
	}
	mediaType, payload, err := m.Payload()
	if err != nil {
		return nil, err
	}
	sum := &manifestSummary{mediaType: mediaType}

	if !isIndex(mediaType) {
		probe, err := probeManifest(payload)
		if err != nil {
			return nil, err
		}
		sum.kind = probe.kind()
		sum.size = manifestTotal(payload, m)
		sum.platforms = 1
		sum.present = 1
		return sum, nil
	}

	sum.size = int64(len(payload))
	for _, c := range m.References() {
		sum.platforms++
		present, err := ms.Exists(ctx, c.Digest)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		sum.present++
		cm, err := ms.Get(ctx, c.Digest)
		if err != nil {
			return nil, err
		}
		_, cp, err := cm.Payload()
		if err != nil {
			return nil, err
		}
		sum.size += manifestTotal(cp, cm)
		if sum.kind == "" {
			if probe, err := probeManifest(cp); err == nil {
				sum.kind = probe.kind()
			}
		}
	}
	if sum.kind == "" {
		sum.kind = KindContainerImage // fully sparse image index
	}
	return sum, nil
}

// manifestTotal is the logical size of one manifest: its own bytes plus
// every referenced descriptor (config and layers).
func manifestTotal(payload []byte, m distribution.Manifest) int64 {
	total := int64(len(payload))
	for _, ref := range m.References() {
		total += ref.Size
	}
	return total
}

// isIndex reports whether the media type is a manifest list / image index
// (its references are manifests, not blobs).
func isIndex(mediaType string) bool {
	return mediaType == ocispec.MediaTypeImageIndex ||
		mediaType == "application/vnd.docker.distribution.manifest.list.v2+json"
}

// manifestProbe is the raw-JSON view of a manifest for kind detection and
// annotation display — the library structs predate the OCI 1.1
// artifactType field, so the payload is the authority.
type manifestProbe struct {
	MediaType    string `json:"mediaType"`
	ArtifactType string `json:"artifactType"`
	Config       struct {
		MediaType string        `json:"mediaType"`
		Digest    digest.Digest `json:"digest"`
	} `json:"config"`
	Annotations map[string]string `json:"annotations"`
}

func probeManifest(payload []byte) (*manifestProbe, error) {
	var p manifestProbe
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// kind detects the ingredient kind from the probed media types (UI-SPEC
// §7, badge component): HelmChart by the CNCF helm config type, Recipe by
// the RECIPE-SPEC §11.2 artifact type, ContainerImage by an OCI or Docker
// image config, anything else OCIArtifact.
//
// TODO(FileSet): RECIPE-SPEC does not define a FileSet artifact media type
// yet; FileSet artifacts read as OCIArtifact until it does.
func (p *manifestProbe) kind() Kind {
	switch {
	case p.ArtifactType == mediaTypeRecipe || p.Config.MediaType == mediaTypeRecipe:
		return KindRecipe
	case p.Config.MediaType == mediaTypeHelmConfig:
		return KindHelmChart
	case p.ArtifactType != "":
		return KindOCIArtifact
	case p.Config.MediaType == mediaTypeOCIConfig || p.Config.MediaType == mediaTypeDockerConfig:
		return KindContainerImage
	default:
		return KindOCIArtifact
	}
}

// configPlatform reads os/arch/variant from an image config blob — the
// single "platform" of a plain manifest. Best-effort: a non-image config
// (helm chart, artifact) simply has no platform.
func (s *Store) configPlatform(ctx context.Context, repo distribution.Repository, probe *manifestProbe) (os, arch, variant string) {
	if probe.Config.MediaType != mediaTypeOCIConfig && probe.Config.MediaType != mediaTypeDockerConfig {
		return "", "", ""
	}
	raw, err := repo.Blobs(ctx).Get(ctx, probe.Config.Digest)
	if err != nil {
		return "", "", ""
	}
	var cfg struct {
		OS      string `json:"os"`
		Arch    string `json:"architecture"`
		Variant string `json:"variant"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", "", ""
	}
	return cfg.OS, cfg.Arch, cfg.Variant
}

// mapBrowseErr folds the library's "does not exist" error types into
// ErrNotFound and wraps everything else with context.
func mapBrowseErr(op string, err error) error {
	var (
		repoUnknown     distribution.ErrRepositoryUnknown
		repoInvalid     distribution.ErrRepositoryNameInvalid
		tagUnknown      distribution.ErrTagUnknown
		manifestUnknown distribution.ErrManifestUnknown
		revisionUnknown distribution.ErrManifestUnknownRevision
	)
	switch {
	case errors.As(err, &repoUnknown), errors.As(err, &repoInvalid),
		errors.As(err, &tagUnknown), errors.As(err, &manifestUnknown),
		errors.As(err, &revisionUnknown):
		return fmt.Errorf("%w: %s: %w", ErrNotFound, op, err)
	default:
		return fmt.Errorf("store: %s: %w", op, err)
	}
}

// dirSize sums the regular files under root; a missing directory (empty
// store) is zero.
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	return total, err
}
