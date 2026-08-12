// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package importer

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"gopkg.in/yaml.v3"

	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// chartMeta is the subset of Chart.yaml the dependency report needs.
type chartMeta struct {
	Name         string `yaml:"name"`
	Dependencies []struct {
		Name       string `yaml:"name"`
		Version    string `yaml:"version"`
		Repository string `yaml:"repository"`
		Condition  string `yaml:"condition"`
	} `yaml:"dependencies"`
}

// verifyChart produces the FR-024 dependency report of a Helm chart and
// enforces its offline deployability: every dependency declared in
// Chart.yaml must be embedded under charts/ inside the package. A missing
// one fails the import, naming the dependency (TBY-CHT-001) — an air-gap
// destination cannot fetch it later. The report persists on the task,
// visible in the detail screen and the API.
func verifyChart(img v1.Image, t *tasks.Task, logger *slog.Logger) error {
	layers, err := img.Layers()
	if err != nil {
		return err
	}
	if len(layers) == 0 {
		return errors.New("chart has no content layer")
	}
	// The chart archive is the first (and normally only) content layer.
	// Compressed() returns the layer bytes exactly as stored — the .tgz
	// Helm packaged (the media type is not an OCI-gzip layer, so no
	// transparent decompression applies to chart content).
	rc, err := layers[0].Compressed()
	if err != nil {
		return fmt.Errorf("opening chart archive: %w", err)
	}
	defer rc.Close() //nolint:errcheck // read-only stream

	meta, embedded, err := scanChartArchive(rc)
	if err != nil {
		return fmt.Errorf("reading chart archive: %w", err)
	}

	t.ChartDependencies = nil
	for _, dep := range meta.Dependencies {
		has := embedded[dep.Name]
		t.ChartDependencies = append(t.ChartDependencies, tasks.ChartDependency{
			Name: dep.Name, Version: dep.Version, Repository: dep.Repository,
			Embedded: has,
		})
		if !has {
			return taxonomy.New(taxonomy.CodeChartDependency,
				taxonomy.Params{"chart": meta.Name, "dependency": dep.Name})
		}
	}
	logger.LogAttrs(context.Background(), slog.LevelInfo, "chart dependencies verified",
		slog.String("chart", meta.Name),
		slog.Int("dependencies", len(meta.Dependencies)))
	return nil
}

// scanChartArchive walks the chart tgz once: Chart.yaml metadata and the
// set of embedded sub-chart names under charts/.
func scanChartArchive(r io.Reader) (*chartMeta, map[string]bool, error) {
	// Helm packages are gzip'd tars; Uncompressed already removed the OCI
	// layer gzip, but the chart layer content itself is a .tgz.
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, nil, fmt.Errorf("chart layer is not a gzip archive: %w", err)
	}
	defer gz.Close() //nolint:errcheck // read-only stream

	tr := tar.NewReader(gz)
	var meta *chartMeta
	embedded := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		// Entries look like <chart>/Chart.yaml, <chart>/charts/<dep>-1.2.3.tgz
		// or <chart>/charts/<dep>/Chart.yaml (unpacked dependency).
		parts := strings.Split(path.Clean(hdr.Name), "/")
		switch {
		case len(parts) == 2 && parts[1] == "Chart.yaml":
			raw, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return nil, nil, err
			}
			meta = &chartMeta{}
			if err := yaml.Unmarshal(raw, meta); err != nil {
				return nil, nil, fmt.Errorf("parsing Chart.yaml: %w", err)
			}
		case len(parts) >= 3 && parts[1] == "charts":
			name := parts[2]
			if strings.HasSuffix(name, ".tgz") {
				// <dep>-<version>.tgz → dependency name up to the last
				// dash-version segment.
				base := strings.TrimSuffix(name, ".tgz")
				if i := strings.LastIndex(base, "-"); i > 0 {
					base = base[:i]
				}
				embedded[base] = true
			} else {
				embedded[name] = true
			}
		}
	}
	if meta == nil {
		return nil, nil, errors.New("no Chart.yaml at the archive root")
	}
	return meta, embedded, nil
}
