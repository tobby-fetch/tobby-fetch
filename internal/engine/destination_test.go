// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/policy"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// newTestDestination builds a Destination over a bare host, without a
// server behind it: the checks below all refuse before any connection.
func newTestDestination(t *testing.T, cfg config.Destination, allow *policy.Allowlist) *Destination {
	t.Helper()
	d, err := NewDestination(cfg, config.Registries{}, allow, nil)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestDestinationIsNotConfigured: no destination is a state, not an
// error — every mirror-mode instance is in it.
func TestDestinationIsNotConfigured(t *testing.T) {
	d, err := NewDestination(config.Destination{}, config.Registries{}, nil, nil)
	if err != nil || d != nil {
		t.Fatalf("NewDestination(empty) = %v, %v; want nil, nil", d, err)
	}
	if d.Host() != "" || d.Cookbook() != "" {
		t.Error("a nil destination must report nothing rather than panic")
	}
}

// TestDestinationPathApplication covers the FR-035 mapping: the relocated
// path is computed upstream, and the destination only prepends its own
// base path and cookbook.
func TestDestinationPathApplication(t *testing.T) {
	d := newTestDestination(t, config.Destination{
		Registry: "registry.example.com", BasePath: "/zone-a/", Cookbook: "cookbook",
	}, nil)

	repo, err := d.Repository("docker.io/bitnami/wordpress")
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	if got, want := repo.String(), "registry.example.com/zone-a/docker.io/bitnami/wordpress"; got != want {
		t.Errorf("relocated destination = %q, want %q", got, want)
	}
	tag, err := d.CookbookTag("wordpress", "6.8.2")
	if err != nil {
		t.Fatalf("CookbookTag: %v", err)
	}
	if got, want := tag.String(), "registry.example.com/zone-a/cookbook/wordpress:6.8.2"; got != want {
		t.Errorf("cookbook tag = %q, want %q", got, want)
	}
	// A Docker Hub alias folds on the way in (ADR-0013) and the cookbook
	// path does not go through relocation at all: the zone's cookbook is
	// the zone's own namespace, identical in every hop.
	if d.Cookbook() != "cookbook" || d.Host() != "registry.example.com" {
		t.Errorf("Host/Cookbook = %q/%q", d.Host(), d.Cookbook())
	}
}

// TestDestinationRefusesOverlongName is the static half of FR-035: a name
// the OCI grammar cannot express is refused before any connection, with
// the limit named.
func TestDestinationRefusesOverlongName(t *testing.T) {
	d := newTestDestination(t, config.Destination{Registry: "registry.example.com"}, nil)
	long := "registry.example.com/" + strings.Repeat("a", 250)

	_, err := d.Repository(long)
	var te *taxonomy.Error
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeDestinationLimit {
		t.Fatalf("Repository(overlong) = %v, want %s", err, taxonomy.CodeDestinationLimit)
	}
	limit, _ := te.ParamsMap()["limit"].(string)
	if !strings.Contains(limit, "255") {
		t.Errorf("the refusal does not name the limit: %q", limit)
	}
}

// TestDestinationConsultsTheAllowlist is FR-030 on the write side: the
// policy answers on the registry name, before a socket exists.
func TestDestinationConsultsTheAllowlist(t *testing.T) {
	allow, err := policy.NewAllowlist([]string{"registry.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	d := newTestDestination(t, config.Destination{Registry: "elsewhere.example.net"}, allow)

	_, err = d.Repository("docker.io/library/nginx")
	var te *taxonomy.Error
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeNotAllowlisted {
		t.Fatalf("Repository(non-allowlisted) = %v, want %s", err, taxonomy.CodeNotAllowlisted)
	}
}

// TestDestinationAcceptanceIsCachedPerName: the FR-035 probe is one round
// trip per repository, not one per ingredient — and a verdict is only
// cached when it IS a verdict.
func TestDestinationAcceptanceIsCachedPerName(t *testing.T) {
	dest := newDestRegistry(t)
	d := newTestDestination(t, config.Destination{Registry: dest.addr}, nil)
	repo, err := d.Repository("docker.io/library/nginx")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for range 3 {
		if err := d.Accepts(ctx, repo); err != nil {
			t.Fatalf("Accepts: %v", err)
		}
	}
	probes := 0
	for _, r := range dest.reqs {
		if strings.Contains(r, "/tags/list") {
			probes++
		}
	}
	if probes != 1 {
		t.Errorf("the destination was probed %d times for one repository, want 1", probes)
	}
}

// TestDestinationInheritsTheInsecureOptIn: registries.insecure is shared
// with the reading side — it describes a host, not a direction.
func TestDestinationInheritsTheInsecureOptIn(t *testing.T) {
	d, err := NewDestination(
		config.Destination{Registry: "registry.example.com:5000"},
		config.Registries{Insecure: []string{"registry.example.com:5000"}},
		nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := d.Repository("docker.io/library/nginx")
	if err != nil {
		t.Fatal(err)
	}
	if got := repo.Scheme(); got != "http" {
		t.Errorf("scheme = %q, want http (the per-host opt-in applies to the destination too)", got)
	}
}
