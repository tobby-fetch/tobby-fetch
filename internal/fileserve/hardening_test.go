// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package fileserve

import (
	"context"
	"errors"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestPackRefusedWhenUnitImportsAreDisabled locks hardening.disableUnitImport
// on packing (FR-098): every surface packs through a Packer, and a hardened
// one refuses before reading the tree — nothing is written, nothing is
// marked — with TBY-POL-005 naming the setting.
func TestPackRefusedWhenUnitImportsAreDisabled(t *testing.T) {
	st := newFakeStore()
	src := tree(t, map[string]string{"a.txt": "hello"})
	for _, opts := range [][]PackerOption{
		{WithUnitImportDisabled(true)},
		// Confined or not: the switch is not a pack root, it closes the door.
		{WithUnitImportDisabled(true), WithPackRoots([]string{src})},
	} {
		_, err := NewPacker(st, "", discardLogger(), opts...).
			Pack(context.Background(), PackRequest{Source: src, Name: "docs", Version: "1.0.0"})
		var refused UnitImportDisabled
		if !errors.As(err, &refused) {
			t.Fatalf("Pack = %v, want the hardening refusal", err)
		}
		te := PackProblem(err)
		if te.Code() != taxonomy.CodeUnitImportDisabled || te.ParamsMap()["setting"] != "hardening.disableUnitImport" {
			t.Errorf("PackProblem = %s %v, want TBY-POL-005 naming the setting", te.Code(), te.ParamsMap())
		}
		if te.Entry().Class != taxonomy.ClassPolicy {
			t.Errorf("class = %v, want policy (exit code 3)", te.Entry().Class)
		}
	}
	if st.writes != 0 || len(st.manual) != 0 || len(st.tags) != 0 {
		t.Errorf("a refused packing wrote: %d blobs, marked %v, tagged %v", st.writes, st.manual, st.tags)
	}
	// Off, the same packer packs.
	packInto(t, st, src, "docs", "1.0.0", WithUnitImportDisabled(false))
}
