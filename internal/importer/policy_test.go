// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package importer

import (
	"errors"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/policy"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// mustSourcePolicy builds the source policy option, failing the test on a
// configuration the importer would refuse to start with.
func mustSourcePolicy(t *testing.T, cfg config.Registries) Option {
	t.Helper()
	allow, err := policy.NewAllowlist(cfg.Allowlist)
	if err != nil {
		t.Fatalf("NewAllowlist(%v): %v", cfg.Allowlist, err)
	}
	return WithSourcePolicy(cfg, allow)
}

// The allowlist must refuse a reference before anything is dialed. The
// registry address here is unroutable on purpose: if the refusal came
// from the network rather than from the policy, the error would be a
// transport failure, not TBY-POL-001.
func TestUnitImportRefusesANonAllowlistedRegistryBeforeContact(t *testing.T) {
	o := buildOptions([]Option{mustSourcePolicy(t, config.Registries{
		Allowlist: []string{"allowed.example.com"},
	})})

	_, err := o.parseRef("blocked.example.com/library/nginx:1.25.0")
	var te *taxonomy.Error
	if !errors.As(err, &te) {
		t.Fatalf("parseRef error = %v, want a taxonomy error", err)
	}
	if te.Code() != taxonomy.CodeNotAllowlisted {
		t.Errorf("code = %s, want %s", te.Code(), taxonomy.CodeNotAllowlisted)
	}

	if _, err := o.parseRef("allowed.example.com/library/nginx:1.25.0"); err != nil {
		t.Errorf("an allowlisted registry was refused: %v", err)
	}
}

// A chart repository is not an OCI registry, but it is a source, and
// FR-030 bounds sources.
func TestChartRepositoryIsSubjectToTheAllowlist(t *testing.T) {
	o := buildOptions([]Option{mustSourcePolicy(t, config.Registries{
		Allowlist: []string{"charts.example.com"},
	})})

	if _, err := parseChartRepoRef("https://blocked.example.com/charts/gitea", o); err == nil {
		t.Error("a chart repository outside the allowlist was accepted")
	}
	if _, err := parseChartRepoRef("https://charts.example.com/gitea", o); err != nil {
		t.Errorf("an allowlisted chart repository was refused: %v", err)
	}
}

// An undeclared policy restricts nothing, and a caller that threads the
// option through a zero-valued struct field must not crash.
func TestNoSourcePolicyIsUnrestricted(t *testing.T) {
	for _, name := range []string{"no options at all", "a nil option"} {
		t.Run(name, func(t *testing.T) {
			opts := []Option{}
			if name == "a nil option" {
				opts = []Option{nil}
			}
			o := buildOptions(opts)
			if _, err := o.parseRef("anything.example.com/library/nginx:1.25.0"); err != nil {
				t.Errorf("parseRef = %v, want no restriction", err)
			}
		})
	}
}
