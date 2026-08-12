// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Command seed pushes and verifies OCI test content against a registry, as
// a real third-party client (go-containerregistry) — the topology scenarios
// never mock the OCI protocol.
//
//	go run ./test/topology/seed push  <host:port>/<repo>:<tag>   → prints the index digest
//	go run ./test/topology/seed pull  <host:port>/<repo>:<tag> <expected-digest>
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/validate"
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
		digest, err := idx.Digest()
		if err != nil {
			return err
		}
		fmt.Println(digest)
		return nil

	case "pull":
		if len(args) != 3 {
			return fmt.Errorf("usage: seed pull <ref> <expected-digest>")
		}
		idx, err := remote.Index(ref, options()...)
		if err != nil {
			return fmt.Errorf("pulling %s: %w", ref, err)
		}
		digest, err := idx.Digest()
		if err != nil {
			return err
		}
		if digest.String() != args[2] {
			return fmt.Errorf("digest mismatch: pulled %s, expected %s", digest, args[2])
		}
		if err := validate.Index(idx); err != nil {
			return fmt.Errorf("validating %s: %w", ref, err)
		}
		fmt.Println("ok", digest)
		return nil
	}
	return fmt.Errorf("unknown command %q", cmd)
}
