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

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/validate"
)

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
		// A deterministic-size multi-arch index: 3 platform manifests of
		// 2 layers each — enough to exercise blobs, manifests, and the
		// index path.
		idx, err := random.Index(1024, 2, 3)
		if err != nil {
			return err
		}
		if err := remote.WriteIndex(ref, idx); err != nil {
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
		idx, err := remote.Index(ref)
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
