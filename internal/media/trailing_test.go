// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media_test

import (
	"os"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestManifestTrailingDataBlocks (NFR-023): bytes after the manifest
// document are bytes no decoder reads and no verification vouches for.
func TestManifestTrailingDataBlocks(t *testing.T) {
	f := twoRecipes(t)
	raw, err := os.ReadFile(f.path(media.ManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path(media.ManifestPath), append(raw, []byte(`{"media_format":99}`)...), 0o600); err != nil { //nolint:gosec // G703: a fixture path under the test's own temporary store
		t.Fatal(err)
	}
	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	if len(rep.Blocks) != 1 || rep.Blocks[0].Code != taxonomy.CodeMediaManifestUnreadable {
		t.Fatalf("blocks = %v, want a single %s", blockCodes(rep), taxonomy.CodeMediaManifestUnreadable)
	}
}
