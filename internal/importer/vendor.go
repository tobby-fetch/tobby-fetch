// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Chart dependency vendoring (FR-025): the explicit, per-operation opt-in
// that turns a chart refused for missing declared dependencies
// (TBY-CHT-001) into an offline-deployable package. Missing dependencies
// are resolved from Chart.lock pins (or Chart.yaml constraints), fetched
// from their declared repositories — https indexes or oci:// — recursively
// within a bounded depth, and embedded under charts/. The archive is then
// repackaged deterministically, so vendoring the same upstream twice
// yields the same digest.
//
// Vendoring breaks the upstream digest and any upstream signature. It is
// therefore never silent (FR-075): the rebuilt manifest carries the
// upstream digest and the embedded list as annotations, the task report
// shows every dependency embedded, and the structured logs record the
// substitution.

package importer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/opencontainers/go-digest"
	recipev1 "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

const (
	// maxVendorDepth bounds the recursive dependency resolution.
	maxVendorDepth = 5

	// The mandatory FR-025 trace, on the manifest itself so it travels
	// with the content: the upstream digest the vendored artifact replaces
	// and the list of what was embedded.
	annotationVendoredUpstream = "dev.tobby.vendored/upstream-digest"
	annotationVendoredDeps     = "dev.tobby.vendored/dependencies"
)

// vendorChartArtifact vendors the archive's missing dependencies and
// rebuilds the artifact over the given config with the FR-025 trace
// annotations. It returns the artifact, the post-vendoring report rows
// (every dependency embedded), and the "name@version" list.
func vendorChartArtifact(ctx context.Context, config, tgz []byte, upstream digest.Digest, o *options) (*chartArtifact, []tasks.ChartDependency, []string, error) {
	vendored, list, err := vendorArchive(ctx, tgz, o, maxVendorDepth)
	if err != nil {
		return nil, nil, nil, err
	}
	arch, err := scanChartArchive(bytes.NewReader(vendored))
	if err != nil {
		return nil, nil, nil, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(fmt.Errorf("re-reading vendored archive: %w", err))
	}
	rows, terr := arch.dependencyRows()
	if terr != nil {
		// Still missing after vendoring: the refusal stands (FR-024).
		return nil, rows, nil, terr
	}
	art, err := buildChartArtifact(config, vendored, map[string]string{
		annotationVendoredUpstream: upstream.String(),
		annotationVendoredDeps:     strings.Join(list, ","),
	})
	if err != nil {
		return nil, nil, nil, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
	return art, rows, list, nil
}

// vendorOCIChart replaces the bit-exact copy of an OCI chart whose
// declared dependencies are missing, when the operation opted in (FR-025):
// the archive is vendored, the artifact rebuilt over the upstream config
// document, annotated with the mandatory trace, and tagged in place of the
// upstream manifest.
func vendorOCIChart(ctx context.Context, dst Destination, repo, tag string, img v1.Image, item *tasks.Item, t *tasks.Task, logger *slog.Logger, o *options) error {
	hash, err := img.Digest()
	if err != nil {
		return err
	}
	upstream := digest.Digest(hash.String())
	config, err := img.RawConfigFile()
	if err != nil {
		return err
	}
	tgz, err := chartLayerBytes(img)
	if err != nil {
		return err
	}
	art, rows, list, err := vendorChartArtifact(ctx, config, tgz, upstream, o)
	if rows != nil {
		t.ChartDependencies = rows
	}
	if err != nil {
		return err
	}
	logVendored(ctx, logger, upstream, art.manifestDigest, list)
	return writeChartArtifact(ctx, dst, repo, tag, art, item, logger)
}

// logVendored emits the mandatory FR-025 log trace: the digest
// substitution and the embedded list — never silent (FR-075).
func logVendored(ctx context.Context, logger *slog.Logger, upstream, vendored digest.Digest, list []string) {
	logger.LogAttrs(ctx, slog.LevelInfo, "chart dependencies vendored",
		slog.String("upstream_digest", upstream.String()),
		slog.String("vendored_digest", vendored.String()),
		slog.String("dependencies", strings.Join(list, ",")))
}

// vendorArchive returns the chart archive with every missing declared
// dependency fetched from its declared repository, itself vendored
// recursively, and embedded under charts/. An archive with nothing
// missing returns unchanged.
func vendorArchive(ctx context.Context, tgz []byte, o *options, depth int) (archive []byte, vendored []string, err error) {
	arch, err := scanChartArchive(bytes.NewReader(tgz))
	if err != nil {
		return nil, nil, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(fmt.Errorf("reading chart archive: %w", err))
	}
	var added []archiveEntry
	var list []string
	for _, dep := range arch.meta.Dependencies {
		if arch.embedded[dep.Name] {
			continue
		}
		depErr := func(cause error) error {
			return taxonomy.New(taxonomy.CodeChartDependency,
				taxonomy.Params{"chart": arch.meta.Name, "dependency": dep.Name}).
				WithCause(cause)
		}
		if depth <= 0 {
			return nil, nil, depErr(fmt.Errorf("dependency resolution exceeds the depth bound (%d)", maxVendorDepth))
		}
		version, repository := dep.Version, dep.Repository
		if pin := arch.lockFor(dep.Name); pin != nil {
			// Chart.lock is the resolved truth: exact version, and the
			// repository actually used.
			version, repository = pin.Version, pin.Repository
		}
		data, resolved, err := fetchDependency(ctx, dep.Name, version, repository, o)
		if err != nil {
			return nil, nil, depErr(err)
		}
		sub, sublist, err := vendorArchive(ctx, data, o, depth-1)
		if err != nil {
			return nil, nil, err
		}
		added = append(added, archiveEntry{
			name: arch.root + "/charts/" + dep.Name + "-" + resolved + ".tgz",
			data: sub,
		})
		list = append(list, dep.Name+"@"+resolved)
		list = append(list, sublist...)
	}
	if len(added) == 0 {
		return tgz, nil, nil
	}
	out, err := repackArchive(tgz, added)
	if err != nil {
		return nil, nil, taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
	sort.Strings(list)
	return out, list, nil
}

// fetchDependency downloads one dependency archive from its declared
// repository and returns the bytes and the concrete resolved version.
func fetchDependency(ctx context.Context, depName, version, repository string, o *options) (archive []byte, resolved string, err error) {
	switch {
	case strings.HasPrefix(repository, "oci://"):
		return fetchOCIDependency(ctx, strings.TrimPrefix(repository, "oci://")+"/"+depName, version, o)
	case isChartRepoURL(repository):
		return fetchRepoDependency(ctx, repository, depName, version, o)
	case repository == "":
		return nil, "", errors.New("the dependency declares no repository")
	default:
		return nil, "", fmt.Errorf("unsupported dependency repository %q (https:// or oci:// only)", repository)
	}
}

// fetchOCIDependency pulls a dependency chart from an OCI repository: an
// exact version is the tag; a constraint resolves against the tag list
// (RECIPE-SPEC §9.2).
func fetchOCIDependency(ctx context.Context, chartRef, version string, o *options) (archive []byte, resolved string, err error) {
	c, err := recipev1.ParseConstraint(version)
	if err != nil {
		return nil, "", fmt.Errorf("version %q: %w", version, err)
	}
	tag := version
	if !c.IsExact() {
		base, err := o.parseRef(chartRef)
		if err != nil {
			return nil, "", err
		}
		tags, err := remote.List(base.Context(), remote.WithContext(ctx))
		if err != nil {
			return nil, "", err
		}
		if tag, err = c.Resolve(tags); err != nil {
			return nil, "", err
		}
	}
	ref, err := o.parseRef(chartRef + ":" + tag)
	if err != nil {
		return nil, "", err
	}
	img, err := remote.Image(ref, remote.WithContext(ctx))
	if err != nil {
		return nil, "", err
	}
	data, err := chartLayerBytes(img)
	if err != nil {
		return nil, "", err
	}
	return data, tag, nil
}

// fetchRepoDependency resolves a dependency through a Helm repository
// index, exactly like a direct FR-024 reference.
func fetchRepoDependency(ctx context.Context, repository, depName, version string, o *options) (archive []byte, resolved string, err error) {
	base, err := url.Parse(strings.TrimRight(repository, "/"))
	if err != nil {
		return nil, "", fmt.Errorf("repository %q: %w", repository, err)
	}
	reference := repository
	if err := checkRepoScheme(base, o, reference); err != nil {
		return nil, "", err
	}
	idx, err := fetchChartIndex(ctx, base, reference)
	if err != nil {
		return nil, "", err
	}
	entry, err := resolveChartVersion(reference, depName, version, idx)
	if err != nil {
		return nil, "", err
	}
	data, err := downloadChart(ctx, base, entry, reference, o)
	if err != nil {
		return nil, "", err
	}
	return data, entry.Version, nil
}

// archiveEntry is one file to add while repackaging.
type archiveEntry struct {
	name string
	data []byte
}

// repackArchive rewrites the chart tgz with the added entries,
// deterministically (FR-025): entries sorted by name, epoch mtimes, zeroed
// ownership, and an unadorned gzip stream — vendoring the same upstream
// twice yields the same bytes, hence the same digest.
func repackArchive(tgz []byte, added []archiveEntry) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, fmt.Errorf("chart layer is not a gzip archive: %w", err)
	}
	defer gz.Close() //nolint:errcheck // read-only stream

	type entry struct {
		hdr  *tar.Header
		data []byte
	}
	tr := tar.NewReader(gz)
	var entries []entry
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxChartBytes+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > maxChartBytes {
			return nil, fmt.Errorf("archive entry %s exceeds the %d-byte bound", hdr.Name, int64(maxChartBytes))
		}
		entries = append(entries, entry{hdr: hdr, data: data})
	}
	for _, a := range added {
		entries = append(entries, entry{
			hdr:  &tar.Header{Name: a.name, Mode: 0o644, Size: int64(len(a.data)), Typeflag: tar.TypeReg},
			data: a.data,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].hdr.Name < entries[j].hdr.Name })

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	epoch := time.Unix(0, 0).UTC()
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name:     e.hdr.Name,
			Mode:     e.hdr.Mode,
			Size:     e.hdr.Size,
			Typeflag: e.hdr.Typeflag,
			Linkname: e.hdr.Linkname,
			ModTime:  epoch,
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
