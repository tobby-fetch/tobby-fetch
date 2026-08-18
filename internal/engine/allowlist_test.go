// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/policy"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

func mustAllow(t *testing.T, entries []string) *policy.Allowlist {
	t.Helper()
	a, err := policy.NewAllowlist(entries)
	if err != nil {
		t.Fatalf("NewAllowlist(%v): %v", entries, err)
	}
	return a
}

// countingRegistry answers nothing useful; it exists to be counted. If the
// allowlist works, this handler is never reached.
func countingRegistry(t *testing.T, hits *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// The point of FR-030 is not that a forbidden transfer fails: it is that
// it never happens. A refusal that reached the network first would still
// have leaked the fact that this instance wanted that content, to a host
// the operator excluded on purpose.
func TestAllowlistRefusesBeforeAnyConnection(t *testing.T) {
	var hits atomic.Int32
	addr := countingRegistry(t, &hits)

	remotes, err := NewRemotes(
		config.Registries{Insecure: []string{addr}},
		mustAllow(t, []string{"allowed.example.com"}),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = remotes.Get(t.Context(), addr+"/library/nginx", "1.25.0")
	var te *taxonomy.Error
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeNotAllowlisted {
		t.Fatalf("Get error = %v, want %s", err, taxonomy.CodeNotAllowlisted)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("the refused registry was contacted %d times; the allowlist must refuse before the socket", got)
	}
}

// Same guarantee on the write side: a publication to a forbidden
// destination uploads nothing, and does not even look to see what is
// already there.
func TestAllowlistRefusesPublicationBeforeAnyConnection(t *testing.T) {
	var hits atomic.Int32
	addr := countingRegistry(t, &hits)

	p, err := NewPublisher(
		config.Registries{Insecure: []string{addr}},
		mustAllow(t, []string{"allowed.example.com"}),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	doc := cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{cookedIngredient()})
	_, err = p.PublishRecipe(t.Context(), addr+"/cookbook/wordpress:6.8.2", doc)
	var te *taxonomy.Error
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeNotAllowlisted {
		t.Fatalf("PublishRecipe error = %v, want %s", err, taxonomy.CodeNotAllowlisted)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("the refused destination was contacted %d times; nothing must be sent", got)
	}
}

// Under substitution the recipe names one host and another is contacted.
// FR-030 is about the second one — trust follows the recipe, policy
// follows the wire (ADR-0013).
func TestAllowlistFollowsTheSubstitutedEndpointNotTheRecipe(t *testing.T) {
	var hits atomic.Int32
	addr := countingRegistry(t, &hits)

	// docker.io is on the list; the endpoint actually serving it is not.
	remotes, err := NewRemotes(
		config.Registries{
			Substitutions: map[string]string{"docker.io": addr},
			Insecure:      []string{addr},
		},
		mustAllow(t, []string{"docker.io"}),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = remotes.Get(t.Context(), "docker.io/library/nginx", "1.25.0")
	var te *taxonomy.Error
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeNotAllowlisted {
		t.Fatalf("Get error = %v, want %s: the substitute endpoint is what the policy bounds", err, taxonomy.CodeNotAllowlisted)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("the substitute endpoint was contacted %d times", got)
	}

	// Allowing the substitute lets it through: the nominal host is not
	// what was checked.
	remotes, err = NewRemotes(
		config.Registries{
			Substitutions: map[string]string{"docker.io": addr},
			Insecure:      []string{addr},
		},
		mustAllow(t, []string{addr}),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remotes.Get(t.Context(), "docker.io/library/nginx", "1.25.0"); errors.As(err, &te) && te.Code() == taxonomy.CodeNotAllowlisted {
		t.Error("allowing the substituted endpoint did not admit the fetch")
	}
	if hits.Load() == 0 {
		t.Error("the allowed endpoint was never contacted")
	}
}

// A refusal is counted, which is what FR-091 exposes.
func TestRefusalsAreObserved(t *testing.T) {
	allow := mustAllow(t, []string{"allowed.example.com"})
	var refused []string
	allow.Observe(func(host string) { refused = append(refused, host) })

	remotes, err := NewRemotes(config.Registries{}, allow, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remotes.Get(t.Context(), "blocked.example.com/library/nginx", "1.25.0"); err == nil {
		t.Fatal("the fetch was not refused")
	}
	if len(refused) != 1 || refused[0] != "blocked.example.com" {
		t.Errorf("observed %v, want one refusal naming blocked.example.com", refused)
	}
}
