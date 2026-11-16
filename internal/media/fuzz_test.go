// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media

import (
	"os"
	"path/filepath"
	"testing"
)

// A medium is untrusted until proven otherwise (FR-054): its manifest is
// the first thing read from it, by a destination that has never met the
// producer. Whatever the file holds, reading it must end in a manifest or
// a refusal, never a panic and never a half-built manifest alongside a
// refusal.
//
//	go test -run '^$' -fuzz FuzzReadManifest ./internal/media/
func FuzzReadManifest(f *testing.F) {
	for _, seed := range []string{
		`{"mediaFormat":1,"storeFormat":1,"mediaId":"20261116T000000Z-0123456789abcdef","zone":"zone-b","recipes":[]}`,
		`{"mediaFormat":99}`,
		`{"recipes":[{"name":"a","version":"1","files":[{"path":"../../etc/passwd","digest":"sha256:00","size":-1}]}]}`,
		`{"mediaFormat":1,"recipes":null,"inventory":{"files":{}}}`,
		`[]`, `null`, `"x"`, `{`, ``,
	} {
		f.Add([]byte(seed))
	}
	root := f.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "meta"), 0o750); err != nil {
		f.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(ManifestPath))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		m, block, err := ReadManifest(root)
		if err != nil {
			t.Fatalf("a readable directory must not produce a host error: %v", err)
		}
		if m != nil && block != nil {
			t.Fatalf("a manifest and a refusal at once: %+v / %+v", m, block)
		}
		if m == nil && block == nil {
			t.Fatal("neither a manifest nor a refusal")
		}
	})
}
