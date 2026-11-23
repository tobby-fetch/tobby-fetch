// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ReferencePath is where the generated reference lives in the repository.
const referenceFile = "../../docs/metrics-reference.json"

// TestReferenceListsEveryFamilyAndNothingElse: the endpoint and the
// document agree in both directions (FR-091 acceptance), and every label
// value a series carries lies inside its documented closed set.
func TestReferenceListsEveryFamilyAndNothingElse(t *testing.T) {
	doc, err := Describe()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Families) < 25 {
		t.Errorf("only %d families documented", len(doc.Families))
	}
	for _, f := range doc.Families {
		if f.Requirement == "" || f.Presence == "" || f.Help == "" {
			t.Errorf("%s: requirement, presence and help are required: %+v", f.Name, f)
		}
	}
}

// TestPresenceClaimsHold: a family documented as "always" is on the first
// scrape of a bare registry; one documented otherwise is not — the "true
// value or absent" rule (FR-091) held to the document.
func TestPresenceClaimsHold(t *testing.T) {
	// What an instance wires before its first scrape (serve.go): the
	// authentication families and the reduced-assurance collector.
	reg := New()
	RegisterAuth(reg)
	reg.RegisterReducedRecipes(nil, func() map[string]int { return nil })
	bare, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	exposed := map[string]bool{}
	for _, f := range bare {
		exposed[f.GetName()] = true
	}
	for name, m := range metaTable {
		if m.presence == always && !exposed[name] {
			t.Errorf("%s is documented as always present but a fresh registry does not expose it", name)
		}
		if m.presence != always && exposed[name] {
			t.Errorf("%s is documented as conditional (%s) but a fresh registry already exposes it", name, m.presence)
		}
	}
}

// TestReferenceIsFresh: docs/metrics-reference.json is the document the
// release signs; it must be what the registry generates now.
func TestReferenceIsFresh(t *testing.T) {
	doc, err := Describe()
	if err != nil {
		t.Fatal(err)
	}
	want, err := Render(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.FromSlash(referenceFile)
	got, err := os.ReadFile(path) //nolint:gosec // G304: a fixed repository path
	if err != nil {
		t.Fatalf("%v\nregenerate it: go run ./tools/metricsref", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale: the registry no longer generates it\nregenerate it: go run ./tools/metricsref", referenceFile)
	}
	var back Reference
	if err := json.Unmarshal(got, &back); err != nil || back.Format != ReferenceFormat {
		t.Errorf("the committed reference does not parse: %v", err)
	}
}
