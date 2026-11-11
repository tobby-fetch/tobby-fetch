// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/integrity/check"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestWithheldContentIsReported: a recipe the vulnerability policy
// withholds was fetched before it was scanned, so its bytes are in the
// store — on a medium, they travel. They are delivered by nothing: the
// media manifest does not list the recipe, so a destination never
// verifies nor pushes them; and they are reported, as content no recipe
// reaches, by the store verification (TBY-INT-003) until a prune reclaims
// them. The behaviour is documented (vulnerability scanning, limits).
func TestWithheldContentIsReported(t *testing.T) {
	env := newScanEnv(t, &scanEnvOpts{settings: ScanSettings{Mode: scan.ModeBlock}})
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m, err := media.Write(ctx, env.dst, media.WriteOptions{Zone: testZone, RunID: "r", ResolvedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Recipes {
		if r.Name == "vuln" {
			t.Fatalf("the medium delivers the withheld recipe: %+v", r)
		}
	}
	vulnRepo := env.hostRepo + "/images/vuln"
	if !env.dst.HasManifest(ctx, vulnRepo, env.vulnDig) {
		t.Fatal("precondition: the withheld image is no longer in the store")
	}
	rep, err := check.Verify(ctx, env.dst, check.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var reported bool
	for _, f := range rep.Findings {
		if f.Code == taxonomy.CodeIntegrityOrphan && strings.Contains(f.Path, "images/vuln") {
			reported = true
		}
	}
	if !reported {
		t.Errorf("the withheld image's content is not reported as reachable from no recipe: %+v", rep.Findings)
	}
}
