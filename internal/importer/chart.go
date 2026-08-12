// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package importer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
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

// chartLock is the subset of Chart.lock the vendoring needs (FR-025):
// exact pinned versions win over Chart.yaml ranges.
type chartLock struct {
	Dependencies []lockedDependency `yaml:"dependencies"`
}

// lockedDependency is one Chart.lock pin.
type lockedDependency struct {
	Name       string `yaml:"name"`
	Version    string `yaml:"version"`
	Repository string `yaml:"repository"`
}

// chartArchive is one scanned chart package: everything the dependency
// verification (FR-024), the OCI conversion (FR-024) and the vendoring
// (FR-025) need, gathered in a single pass over the tgz.
type chartArchive struct {
	// root is the archive's top-level directory ("wordpress").
	root string
	meta *chartMeta
	// rawMeta is Chart.yaml exactly as packaged — the source of the OCI
	// config document when converting (FR-024).
	rawMeta []byte
	// lock is Chart.lock when the package carries one; nil otherwise.
	lock *chartLock
	// embedded is the set of sub-chart names present under charts/.
	embedded map[string]bool
}

// lockFor returns the Chart.lock pin of one dependency, or nil.
func (a *chartArchive) lockFor(name string) *lockedDependency {
	if a.lock == nil {
		return nil
	}
	for i := range a.lock.Dependencies {
		if a.lock.Dependencies[i].Name == name {
			return &a.lock.Dependencies[i]
		}
	}
	return nil
}

// dependencyRows builds the FR-024 report rows and returns the taxonomy
// refusal naming the first missing dependency, if any.
func (a *chartArchive) dependencyRows() ([]tasks.ChartDependency, *taxonomy.Error) {
	var rows []tasks.ChartDependency
	var terr *taxonomy.Error
	for _, dep := range a.meta.Dependencies {
		has := a.embedded[dep.Name]
		rows = append(rows, tasks.ChartDependency{
			Chart: a.meta.Name, Name: dep.Name, Version: dep.Version,
			Repository: dep.Repository, Embedded: has,
		})
		if !has && terr == nil {
			terr = taxonomy.New(taxonomy.CodeChartDependency,
				taxonomy.Params{"chart": a.meta.Name, "dependency": dep.Name})
		}
	}
	return rows, terr
}

// VerifyChart runs the FR-024 dependency verification of one chart image
// and returns the report rows — shared by the unit import and the recipe
// engine (milestone 3): every dependency declared in Chart.yaml must be
// embedded under charts/ inside the package. A missing one fails with
// TBY-CHT-001 naming the dependency — an air-gap destination cannot fetch
// it later. The full report is returned even on failure, so it shows what
// was checked.
func VerifyChart(img v1.Image) ([]tasks.ChartDependency, error) {
	data, err := chartLayerBytes(img)
	if err != nil {
		return nil, err
	}
	arch, err := scanChartArchive(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("reading chart archive: %w", err)
	}
	rows, terr := arch.dependencyRows()
	if terr != nil {
		return rows, terr
	}
	return rows, nil
}

// chartLayerBytes reads the chart's content layer — the .tgz Helm packaged,
// stored bit-exactly (the media type is not an OCI-gzip layer, so no
// transparent decompression applies) — bounded by maxChartBytes.
func chartLayerBytes(img v1.Image) ([]byte, error) {
	layers, err := img.Layers()
	if err != nil {
		return nil, err
	}
	if len(layers) == 0 {
		return nil, errors.New("chart has no content layer")
	}
	rc, err := layers[0].Compressed()
	if err != nil {
		return nil, fmt.Errorf("opening chart archive: %w", err)
	}
	defer rc.Close() //nolint:errcheck // read-only stream
	data, err := io.ReadAll(io.LimitReader(rc, maxChartBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxChartBytes {
		return nil, fmt.Errorf("chart archive exceeds the %d-byte bound", int64(maxChartBytes))
	}
	return data, nil
}

// scanChartArchive walks the chart tgz once: Chart.yaml (raw and parsed),
// Chart.lock when present, the top-level directory, and the set of
// embedded sub-chart names under charts/.
func scanChartArchive(r io.Reader) (*chartArchive, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("chart layer is not a gzip archive: %w", err)
	}
	defer gz.Close() //nolint:errcheck // read-only stream

	tr := tar.NewReader(gz)
	arch := &chartArchive{embedded: map[string]bool{}}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		// Entries look like <chart>/Chart.yaml, <chart>/charts/<dep>-1.2.3.tgz
		// or <chart>/charts/<dep>/Chart.yaml (unpacked dependency).
		parts := strings.Split(path.Clean(hdr.Name), "/")
		switch {
		case len(parts) == 2 && parts[1] == "Chart.yaml":
			raw, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return nil, err
			}
			arch.root = parts[0]
			arch.rawMeta = raw
			arch.meta = &chartMeta{}
			if err := yaml.Unmarshal(raw, arch.meta); err != nil {
				return nil, fmt.Errorf("parsing Chart.yaml: %w", err)
			}
		case len(parts) == 2 && parts[1] == "Chart.lock":
			raw, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return nil, err
			}
			arch.lock = &chartLock{}
			if err := yaml.Unmarshal(raw, arch.lock); err != nil {
				return nil, fmt.Errorf("parsing Chart.lock: %w", err)
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
				arch.embedded[base] = true
			} else {
				arch.embedded[name] = true
			}
		}
	}
	if arch.meta == nil {
		return nil, errors.New("no Chart.yaml at the archive root")
	}
	return arch, nil
}
