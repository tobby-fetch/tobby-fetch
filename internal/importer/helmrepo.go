// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Helm HTTP(S) chart repositories (FR-024): a reference of the form
// https://<base>/<chart>[:<version>] designates a chart of a classic Helm
// repository. The chart is located through <base>/index.yaml, downloaded —
// size-bounded and digest-verified when the index announces one — then
// converted into the standard OCI chart artifact `helm push` would have
// produced: config = the Chart.yaml document as JSON, one content layer =
// the .tgz. It lands direct-to-storage under the relocated path
// <repository-host>/<path>/<chart> (ADR-0013, reusing internal/relocate),
// tagged with the version.
//
// The conversion is deterministic: the same source bytes always produce
// the same manifest digest, so inspection and import agree without shared
// state and re-imports are idempotent (FR-026).

package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/opencontainers/go-digest"
	recipev1 "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"
	"gopkg.in/yaml.v3"

	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

const (
	// helmChartContentMediaType is the single content layer of an OCI
	// chart artifact — the .tgz exactly as packaged (what `helm push`
	// produces).
	helmChartContentMediaType = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"

	// maxIndexBytes bounds index.yaml downloads: a repository index is
	// metadata; anything larger is refused before it fills memory.
	maxIndexBytes = 32 << 20
	// maxChartBytes bounds chart archive downloads and reads.
	maxChartBytes = 256 << 20
)

// isChartRepoURL reports whether the reference designates a Helm HTTP(S)
// repository chart (FR-024) rather than an OCI reference.
func isChartRepoURL(reference string) bool {
	return strings.HasPrefix(reference, "https://") || strings.HasPrefix(reference, "http://")
}

// chartRepoRef is a parsed chart-repository reference.
type chartRepoRef struct {
	// base is the repository base URL (scheme, host, directory path).
	base *url.URL
	// chart is the chart name — the reference's last path segment.
	chart string
	// version is the requested version (exact or a §9 constraint); empty
	// resolves the highest stable version of the index.
	version string
	// repo is the relocated destination repository (ADR-0013).
	repo string
}

// checkRepoScheme admits https, and plain http only for hosts explicitly
// opted into registries.insecure — per host and declared, never a global
// switch (FR-075).
func checkRepoScheme(u *url.URL, o *options, reference string) error {
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && o.insecure[u.Host]:
		return nil
	}
	return taxonomy.New(taxonomy.CodeBadReference, taxonomy.Params{"reference": reference}).
		WithCause(fmt.Errorf("scheme %q refused: https, or http with the per-host registries.insecure opt-in (FR-075)", u.Scheme))
}

// parseChartRepoRef splits https://<base>/<chart>[:<version>] and computes
// the relocated destination repository.
func parseChartRepoRef(reference string, o *options) (*chartRepoRef, error) {
	badRef := func(cause error) error {
		return taxonomy.New(taxonomy.CodeBadReference,
			taxonomy.Params{"reference": reference}).WithCause(cause)
	}
	u, err := url.Parse(reference)
	if err != nil {
		return nil, badRef(err)
	}
	if err := checkRepoScheme(u, o, reference); err != nil {
		return nil, err
	}
	trimmed := strings.Trim(u.Path, "/")
	if trimmed == "" {
		return nil, badRef(errors.New("expected https://<repository>/<chart>[:<version>]"))
	}
	segs := strings.Split(trimmed, "/")
	last := segs[len(segs)-1]
	chart, version := last, ""
	if i := strings.IndexByte(last, ':'); i >= 0 {
		chart, version = last[:i], last[i+1:]
		if version == "" {
			return nil, badRef(errors.New("empty version after ':'"))
		}
	}
	if chart == "" {
		return nil, badRef(errors.New("empty chart name"))
	}
	dir := strings.Join(segs[:len(segs)-1], "/")
	base := *u
	base.Path = "/" + dir
	base.RawQuery, base.Fragment = "", ""
	hostPath := u.Host
	if dir != "" {
		hostPath += "/" + dir
	}
	repo, err := relocate.Path(hostPath + "/" + chart)
	if err != nil {
		return nil, badRef(err)
	}
	return &chartRepoRef{base: &base, chart: chart, version: version, repo: repo}, nil
}

// helmIndex is the subset of a Helm repository index.yaml the import needs
// (FR-024): per-chart entries with version, archive URLs, and the digest
// the repository announces.
type helmIndex struct {
	Entries map[string][]helmIndexEntry `yaml:"entries"`
}

// helmIndexEntry is one chart version of the index.
type helmIndexEntry struct {
	Version string   `yaml:"version"`
	URLs    []string `yaml:"urls"`
	Digest  string   `yaml:"digest"`
}

// httpFetch performs one bounded GET, mapping failures onto the taxonomy
// exactly like the OCI paths (R-03): timeout, authentication, unknown
// reference, unreachable — each its own stable code.
func httpFetch(ctx context.Context, target string, limit int64, reference string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeBadReference,
			taxonomy.Params{"reference": reference}).WithCause(err)
	}
	host := req.URL.Host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, mapRemoteErr(ctx, host, reference, budgetOf(ctx), err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only stream
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, taxonomy.New(taxonomy.CodeRegistryAuth, taxonomy.Params{"host": host})
	case http.StatusNotFound:
		return nil, taxonomy.New(taxonomy.CodeRefNotFound, taxonomy.Params{"reference": reference})
	default:
		return nil, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(fmt.Errorf("GET %s: unexpected status %d", target, resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, mapRemoteErr(ctx, host, reference, budgetOf(ctx), err)
	}
	if int64(len(data)) > limit {
		return nil, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(fmt.Errorf("GET %s: response exceeds the %d-byte bound", target, limit))
	}
	return data, nil
}

// fetchChartIndex downloads and parses <base>/index.yaml.
func fetchChartIndex(ctx context.Context, base *url.URL, reference string) (*helmIndex, error) {
	raw, err := httpFetch(ctx, base.JoinPath("index.yaml").String(), maxIndexBytes, reference)
	if err != nil {
		return nil, err
	}
	idx := &helmIndex{}
	if err := yaml.Unmarshal(raw, idx); err != nil {
		return nil, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(fmt.Errorf("parsing %s/index.yaml: %w", base, err))
	}
	return idx, nil
}

// resolveChartVersion selects the index entry: the requested version — an
// exact version or a §9 constraint — or, absent one, the highest stable
// version of the index. Never a silent fallback (FR-021 semantics).
func resolveChartVersion(reference, chart, version string, idx *helmIndex) (*helmIndexEntry, error) {
	entries := idx.Entries[chart]
	if len(entries) == 0 {
		return nil, taxonomy.New(taxonomy.CodeRefNotFound,
			taxonomy.Params{"reference": reference}).
			WithCause(fmt.Errorf("chart %q is not in the repository index", chart))
	}
	expr := version
	if expr == "" {
		expr = "*"
	}
	versionResolve := func(cause error) error {
		return taxonomy.New(taxonomy.CodeVersionResolve, taxonomy.Params{
			"reference": reference, "constraint": expr, "detail": cause.Error(),
		}).WithCause(cause)
	}
	c, err := recipev1.ParseConstraint(expr)
	if err != nil {
		return nil, versionResolve(err)
	}
	candidates := make([]string, 0, len(entries))
	for i := range entries {
		candidates = append(candidates, entries[i].Version)
	}
	resolved, err := c.Resolve(candidates)
	if err != nil {
		return nil, versionResolve(err)
	}
	for i := range entries {
		if entries[i].Version == resolved {
			return &entries[i], nil
		}
	}
	return nil, taxonomy.New(taxonomy.CodeInternal, nil).
		WithCause(fmt.Errorf("resolved version %q vanished from the index", resolved))
}

// downloadChart fetches the entry's archive — first URL, absolute or
// relative to the base — bounded by maxChartBytes, and verifies the digest
// the index announces when present (FR-024 integrity note).
func downloadChart(ctx context.Context, base *url.URL, entry *helmIndexEntry, reference string, o *options) ([]byte, error) {
	if len(entry.URLs) == 0 {
		return nil, taxonomy.New(taxonomy.CodeRefNotFound,
			taxonomy.Params{"reference": reference}).
			WithCause(errors.New("index entry has no archive URL"))
	}
	rel, err := url.Parse(entry.URLs[0])
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(fmt.Errorf("index entry URL %q: %w", entry.URLs[0], err))
	}
	target := rel
	if !rel.IsAbs() {
		// JoinPath cleans the path: a relative URL cannot escape the base.
		target = base.JoinPath(rel.Path)
	}
	if err := checkRepoScheme(target, o, reference); err != nil {
		return nil, err
	}
	data, err := httpFetch(ctx, target.String(), maxChartBytes, reference)
	if err != nil {
		return nil, err
	}
	if entry.Digest != "" {
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		if !strings.EqualFold(got, strings.TrimPrefix(entry.Digest, "sha256:")) {
			return nil, taxonomy.New(taxonomy.CodeDigestMismatch, taxonomy.Params{
				"reference": reference,
				"expected":  entry.Digest,
				"actual":    "sha256:" + got,
			})
		}
	}
	return data, nil
}

// chartArtifact is a chart converted to its standard OCI form — the exact
// artifact `helm push` would have produced, plus optional vendoring
// annotations (FR-025). Every digest is computable before anything lands.
type chartArtifact struct {
	config         []byte
	configDigest   digest.Digest
	tgz            []byte
	tgzDigest      digest.Digest
	manifest       []byte
	manifestDigest digest.Digest
}

// v1Hash converts an opencontainers digest into go-containerregistry's.
func v1Hash(d digest.Digest) v1.Hash {
	return v1.Hash{Algorithm: string(d.Algorithm()), Hex: d.Encoded()}
}

// buildChartArtifact assembles the OCI manifest of a chart package —
// deterministically: same config and archive bytes, same manifest digest
// (json.Marshal orders annotation keys).
func buildChartArtifact(config, tgz []byte, annotations map[string]string) (*chartArtifact, error) {
	a := &chartArtifact{
		config: config, configDigest: digest.FromBytes(config),
		tgz: tgz, tgzDigest: digest.FromBytes(tgz),
	}
	raw, err := json.Marshal(v1.Manifest{
		SchemaVersion: 2,
		MediaType:     types.OCIManifestSchema1,
		Config: v1.Descriptor{
			MediaType: helmConfigMediaType,
			Size:      int64(len(config)),
			Digest:    v1Hash(a.configDigest),
		},
		Layers: []v1.Descriptor{{
			MediaType: helmChartContentMediaType,
			Size:      int64(len(tgz)),
			Digest:    v1Hash(a.tgzDigest),
		}},
		Annotations: annotations,
	})
	if err != nil {
		return nil, err
	}
	a.manifest = raw
	a.manifestDigest = digest.FromBytes(raw)
	return a, nil
}

// chartConfigJSON renders the packaged Chart.yaml as the JSON config
// document of the OCI chart artifact — key-sorted, hence deterministic.
func chartConfigJSON(rawMeta []byte) ([]byte, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(rawMeta, &doc); err != nil {
		return nil, fmt.Errorf("parsing Chart.yaml: %w", err)
	}
	return json.Marshal(doc)
}

// convertChartArchive converts one downloaded .tgz into its OCI artifact
// and returns both the artifact and the scanned package.
func convertChartArchive(tgz []byte, annotations map[string]string) (*chartArtifact, *chartArchive, error) {
	internal := func(err error) error {
		return taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
	arch, err := scanChartArchive(bytes.NewReader(tgz))
	if err != nil {
		return nil, nil, internal(fmt.Errorf("reading chart archive: %w", err))
	}
	config, err := chartConfigJSON(arch.rawMeta)
	if err != nil {
		return nil, nil, internal(err)
	}
	art, err := buildChartArtifact(config, tgz, annotations)
	if err != nil {
		return nil, nil, internal(err)
	}
	return art, arch, nil
}

// inspectChartRepo is the FR-024 inspection: locate the chart in the
// repository index, download and convert it, and report one HelmChart
// artifact — a single selectable entry carrying the converted manifest's
// digest and the FR-026 status against the local store.
func inspectChartRepo(ctx context.Context, reference string, local Local, o *options) (*Report, error) {
	cr, err := parseChartRepoRef(reference, o)
	if err != nil {
		return nil, err
	}
	idx, err := fetchChartIndex(ctx, cr.base, reference)
	if err != nil {
		return nil, err
	}
	entry, err := resolveChartVersion(reference, cr.chart, cr.version, idx)
	if err != nil {
		return nil, err
	}
	tgz, err := downloadChart(ctx, cr.base, entry, reference, o)
	if err != nil {
		return nil, err
	}
	art, _, err := convertChartArchive(tgz, nil)
	if err != nil {
		return nil, err
	}
	return &Report{
		Reference:   reference,
		Repository:  cr.repo,
		Tag:         entry.Version,
		IndexDigest: art.manifestDigest.String(),
		MediaType:   string(types.OCIManifestSchema1),
		Kind:        KindChart,
		Platforms: []Platform{{
			Digest:    art.manifestDigest.String(),
			SizeBytes: int64(len(art.config)) + int64(len(art.tgz)),
			Status:    statusOf(ctx, local, cr.repo, entry.Version, art.manifestDigest.String()),
		}},
	}, nil
}

// runChartRepoImport transfers one chart-repository reference (FR-024):
// the re-downloaded archive is converted — deterministically, to the very
// digest the inspection pinned — and written direct-to-storage. Missing
// declared dependencies refuse the import (TBY-CHT-001) unless the
// operation opted into vendoring (FR-025).
func runChartRepoImport(ctx context.Context, t *tasks.Task, dst Destination, logger *slog.Logger, save func(), o *options) error {
	cr, err := parseChartRepoRef(t.Reference, o)
	if err != nil {
		return err
	}
	idx, err := fetchChartIndex(ctx, cr.base, t.Reference)
	if err != nil {
		return err
	}
	entry, err := resolveChartVersion(t.Reference, cr.chart, cr.version, idx)
	if err != nil {
		return err
	}
	tgz, err := downloadChart(ctx, cr.base, entry, t.Reference, o)
	if err != nil {
		return err
	}

	for i := range t.Items {
		item := &t.Items[i]
		if item.Status == tasks.StatusDone || item.Status == tasks.StatusSkipped {
			continue // resumed task: settled items stay settled (FR-029)
		}
		item.Status = tasks.StatusRunning
		save()
		if err := importChartArchive(ctx, dst, cr.repo, entry.Version, tgz, item, t, logger, o); err != nil {
			var te *taxonomy.Error
			if !errors.As(err, &te) {
				te = taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
			}
			item.Status = tasks.StatusFailed
			item.Error = tasks.FromTaxonomy(te)
			logger.LogAttrs(ctx, slog.LevelWarn, "item failed",
				slog.String("item", item.Name), slog.String("error", te.Error()))
		}
		save()
	}
	return nil
}

// importChartArchive verifies the FR-024 dependency embedding, optionally
// vendors (FR-025), then writes the converted chart.
func importChartArchive(ctx context.Context, dst Destination, repo, version string, tgz []byte, item *tasks.Item, t *tasks.Task, logger *slog.Logger, o *options) error {
	art, arch, err := convertChartArchive(tgz, nil)
	if err != nil {
		return err
	}
	rows, terr := arch.dependencyRows()
	t.ChartDependencies = rows
	if terr != nil {
		if !t.VendorDependencies {
			return terr
		}
		// The upstream anchor is the digest the unvendored conversion
		// would have produced — what the inspection reported.
		vart, vrows, list, err := vendorChartArtifact(ctx, art.config, tgz, art.manifestDigest, o)
		if vrows != nil {
			t.ChartDependencies = vrows
		}
		if err != nil {
			return err
		}
		logVendored(ctx, logger, art.manifestDigest, vart.manifestDigest, list)
		art = vart
	}
	return writeChartArtifact(ctx, dst, repo, version, art, item, logger)
}

// writeChartArtifact writes the artifact's blobs — only the missing ones,
// FR-026 — then its manifest, tagged.
func writeChartArtifact(ctx context.Context, dst Destination, repo, tag string, art *chartArtifact, item *tasks.Item, logger *slog.Logger) error {
	var transferred int64
	for _, b := range []struct {
		dgst digest.Digest
		data []byte
	}{{art.tgzDigest, art.tgz}, {art.configDigest, art.config}} {
		if dst.HasBlob(ctx, repo, b.dgst) {
			continue
		}
		if err := dst.WriteBlob(ctx, repo, b.dgst, bytes.NewReader(b.data)); err != nil {
			return err
		}
		transferred += int64(len(b.data))
	}
	if _, err := dst.PutManifest(ctx, repo, string(types.OCIManifestSchema1), art.manifest, tag); err != nil {
		return taxonomy.New(taxonomy.CodeStoreWrite,
			taxonomy.Params{"detail": err.Error()}).WithCause(err)
	}
	item.Status = tasks.StatusDone
	item.SizeBytes = transferred
	item.Digest = art.manifestDigest.String()
	logger.LogAttrs(ctx, slog.LevelInfo, "chart artifact stored",
		slog.String("repository", repo), slog.String("tag", tag),
		slog.String("digest", art.manifestDigest.String()))
	return nil
}
