// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// thirdpartyDir holds the vendored single-file UI dependencies (ADR-0010).
// Deliberately named "thirdparty", not "vendor": directories named vendor
// are ignored by the Go toolchain.
const thirdpartyDir = "static/thirdparty"

// vendoredManifest mirrors static/thirdparty/VENDORED.yaml.
type vendoredManifest struct {
	Assets []vendoredAsset `yaml:"assets"`
}

type vendoredAsset struct {
	Name      string `yaml:"name"`
	File      string `yaml:"file"`
	Version   string `yaml:"version"`
	SourceURL string `yaml:"source_url"`
	SHA256    string `yaml:"sha256"`
	License   string `yaml:"license"`
	Vendored  string `yaml:"vendored"`
}

// TestVendoredAssetIntegrity recomputes the sha256 of every vendored UI
// asset and compares it with the fingerprint recorded in VENDORED.yaml
// (ADR-0010): the test fails if a vendored file diverges from its
// recorded integrity, or if the manifest is incomplete.
func TestVendoredAssetIntegrity(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(thirdpartyDir, "VENDORED.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	var manifest vendoredManifest
	if err := yaml.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse VENDORED.yaml: %v", err)
	}
	if len(manifest.Assets) == 0 {
		t.Fatal("VENDORED.yaml declares no assets")
	}

	for _, asset := range manifest.Assets {
		t.Run(asset.Name, func(t *testing.T) {
			for field, value := range map[string]string{
				"name":       asset.Name,
				"file":       asset.File,
				"version":    asset.Version,
				"source_url": asset.SourceURL,
				"sha256":     asset.SHA256,
				"license":    asset.License,
				"vendored":   asset.Vendored,
			} {
				if value == "" {
					t.Errorf("VENDORED.yaml: missing %q for asset %q", field, asset.Name)
				}
			}

			data, err := os.ReadFile(filepath.Join(thirdpartyDir, asset.File))
			if err != nil {
				t.Fatalf("read vendored file %q: %v", asset.File, err)
			}

			sum := sha256.Sum256(data)
			if got := hex.EncodeToString(sum[:]); got != asset.SHA256 {
				t.Errorf("%s: sha256 mismatch\n  manifest: %s\n  computed: %s\nthe vendored file diverged from VENDORED.yaml (ADR-0010)",
					asset.File, asset.SHA256, got)
			}
		})
	}
}
