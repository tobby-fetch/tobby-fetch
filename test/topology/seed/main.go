// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Command seed pushes and verifies OCI test content against a registry, as
// a real third-party client (go-containerregistry) — the topology scenarios
// never mock the OCI protocol.
//
//	go run ./test/topology/seed push         <host:port>/<repo>:<tag>              → prints the index digest
//	go run ./test/topology/seed pull         <host:port>/<repo>:<tag> <digest>
//	go run ./test/topology/seed push-recipe  <host:port>/<repo>:<tag> <yaml-file>  → prints the manifest digest
//	go run ./test/topology/seed push-fileset <host:port>/<repo>:<tag> <dir>        → prints the manifest digest
//
// push-recipe publishes the §11.2 recipe artifact layout (artifactType,
// empty config, single YAML layer); push-fileset packages a directory as
// a single-layer OCI image (the FileSet form of RECIPE-SPEC §7.4).
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/google/go-containerregistry/pkg/v1/validate"
	"github.com/opencontainers/go-digest"
)

// platformedIndex builds a 3-platform index the way production images are
// built: every child descriptor carries its platform.
func platformedIndex() (v1.ImageIndex, error) {
	var idx v1.ImageIndex = empty.Index
	for _, pf := range []v1.Platform{
		{OS: "linux", Architecture: "amd64"},
		{OS: "linux", Architecture: "arm64"},
		{OS: "linux", Architecture: "arm", Variant: "v7"},
	} {
		img, err := random.Image(1024, 2)
		if err != nil {
			return nil, err
		}
		cfg, err := img.ConfigFile()
		if err != nil {
			return nil, err
		}
		cfg = cfg.DeepCopy()
		cfg.OS, cfg.Architecture, cfg.Variant = pf.OS, pf.Architecture, pf.Variant
		if img, err = mutate.ConfigFile(img, cfg); err != nil {
			return nil, err
		}
		p := pf
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{
			Add:        img,
			Descriptor: v1.Descriptor{Platform: &p},
		})
	}
	return idx, nil
}

// options returns the remote options: anonymous by default, Basic when
// SEED_AUTH=user:password is set — the client stays a plain third-party
// OCI client either way (milestone-2 topology authenticates the embedded
// registry, ADR-0009).
func options() []remote.Option {
	if v := os.Getenv("SEED_AUTH"); v != "" {
		if user, pass, ok := strings.Cut(v, ":"); ok {
			return []remote.Option{remote.WithAuth(&authn.Basic{Username: user, Password: pass})}
		}
	}
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: seed push <ref> | seed pull <ref> <expected-digest>")
	}
	cmd, rawRef := args[0], args[1]

	ref, err := name.ParseReference(rawRef, name.Insecure)
	if err != nil {
		return err
	}

	switch cmd {
	case "push":
		// A realistic multi-arch index: 3 explicit platforms of 2 layers
		// each — enough to exercise blobs, manifests, the index path, and
		// the platform-aware inspection of the unit import (FR-022).
		idx, err := platformedIndex()
		if err != nil {
			return err
		}
		if err := remote.WriteIndex(ref, idx, options()...); err != nil {
			return fmt.Errorf("pushing %s: %w", ref, err)
		}
		d, err := idx.Digest()
		if err != nil {
			return err
		}
		fmt.Println(d)
		return nil

	case "push-recipe":
		if len(args) != 3 {
			return fmt.Errorf("usage: seed push-recipe <ref> <yaml-file>")
		}
		return pushRecipe(ref, args[2])

	case "push-fileset":
		if len(args) != 3 {
			return fmt.Errorf("usage: seed push-fileset <ref> <dir>")
		}
		return pushFileSet(ref, args[2])

	case "pull":
		if len(args) != 3 {
			return fmt.Errorf("usage: seed pull <ref> <expected-digest>")
		}
		idx, err := remote.Index(ref, options()...)
		if err != nil {
			return fmt.Errorf("pulling %s: %w", ref, err)
		}
		d, err := idx.Digest()
		if err != nil {
			return err
		}
		if d.String() != args[2] {
			return fmt.Errorf("digest mismatch: pulled %s, expected %s", d, args[2])
		}
		if err := validate.Index(idx); err != nil {
			return fmt.Errorf("validating %s: %w", ref, err)
		}
		fmt.Println("ok", d)
		return nil
	}
	return fmt.Errorf("unknown command %q", cmd)
}

// mediaTypeRecipe is the recipe artifact envelope (RECIPE-SPEC §11.2).
const mediaTypeRecipe = "application/vnd.tobby.recipe.v1+yaml"

// pushRecipe publishes a recipe document under the §11.2 artifact layout:
// artifactType, OCI empty config, exactly one YAML layer.
func pushRecipe(ref name.Reference, yamlPath string) error {
	doc, err := os.ReadFile(yamlPath) //nolint:gosec // G304: test-tool input path
	if err != nil {
		return err
	}
	layer := static.NewLayer(doc, types.MediaType(mediaTypeRecipe))
	config := static.NewLayer([]byte("{}"), "application/vnd.oci.empty.v1+json")
	for _, l := range []v1.Layer{layer, config} {
		if err := remote.WriteLayer(ref.Context(), l, options()...); err != nil {
			return fmt.Errorf("uploading blob: %w", err)
		}
	}
	man := map[string]any{
		"schemaVersion": 2,
		"mediaType":     string(types.OCIManifestSchema1),
		"artifactType":  mediaTypeRecipe,
		"config": map[string]any{
			"mediaType": "application/vnd.oci.empty.v1+json",
			"digest":    digest.FromBytes([]byte("{}")).String(),
			"size":      2,
		},
		"layers": []map[string]any{{
			"mediaType":   mediaTypeRecipe,
			"digest":      digest.FromBytes(doc).String(),
			"size":        len(doc),
			"annotations": map[string]string{"org.opencontainers.image.title": "recipe.yaml"},
		}},
	}
	raw, err := json.Marshal(man)
	if err != nil {
		return err
	}
	if err := remote.Put(ref, rawManifest{raw: raw}, options()...); err != nil {
		return fmt.Errorf("pushing recipe manifest: %w", err)
	}
	fmt.Println(digest.FromBytes(raw))
	return nil
}

// rawManifest satisfies remote.Taggable with pre-built manifest bytes.
type rawManifest struct{ raw []byte }

func (m rawManifest) RawManifest() ([]byte, error) { return m.raw, nil }
func (m rawManifest) MediaType() (types.MediaType, error) {
	return types.OCIManifestSchema1, nil
}

// pushFileSet packages a directory as a single-layer OCI image — the
// FileSet ingredient form (RECIPE-SPEC §7.4) — and prints its digest.
func pushFileSet(ref name.Reference, dir string) error {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(paths)
	for _, p := range paths {
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p) //nolint:gosec // G304: test-tool input tree
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: filepath.ToSlash(rel), Mode: 0o644, Size: int64(len(data)),
		}); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}

	layer := static.NewLayer(buf.Bytes(), types.OCILayer)
	img, err := mutate.AppendLayers(mutate.MediaType(mutate.ConfigMediaType(empty.Image, types.OCIConfigJSON), types.OCIManifestSchema1), layer)
	if err != nil {
		return err
	}
	if err := remote.Write(ref, img, options()...); err != nil {
		return fmt.Errorf("pushing fileset: %w", err)
	}
	d, err := img.Digest()
	if err != nil {
		return err
	}
	fmt.Println(d)
	return nil
}
