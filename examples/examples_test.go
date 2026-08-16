// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package examples_test keeps the published example recipes honest: they are
// parsed by the very SDK the engine uses, so an example can never advertise a
// shape Tobby would refuse to read. Digests and platform labels are checked
// against the live registries by hand at authoring time — that part needs the
// network and stays out of the gates.
package examples_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"
)

// cooked lists the examples that claim full pinning. The others are drafts on
// purpose: their Helm chart only exists in a legacy index.yaml repository, so
// it must be republished into the operator's own registry before a digest
// exists to record (RECIPE-SPEC §7.2).
var cooked = map[string]bool{
	"keycloak.yaml":                  true,
	"otel-collector.yaml":            true,
	"victoria-metrics-operator.yaml": true,
}

func TestExamplesParse(t *testing.T) {
	files, err := filepath.Glob("*.yaml")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no example found: the directory this test guards is empty")
	}

	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			data, err := os.ReadFile(file) //nolint:gosec // fixed test corpus, not user input
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			obj, err := spec.Parse(data)
			if err != nil {
				t.Fatalf("does not parse against the specification SDK: %v", err)
			}

			recipe, ok := obj.(*spec.Recipe)
			if !ok {
				// The Retriever example: parsing it is the whole assertion.
				return
			}
			if err := recipe.Validate(spec.ProfileDraft); err != nil {
				t.Fatalf("invalid as a draft: %v", err)
			}

			err = recipe.Validate(spec.ProfileCooked)
			switch {
			case cooked[file] && err != nil:
				t.Errorf("announced as cooked but not fully pinned: %v", err)
			case !cooked[file] && err == nil:
				t.Errorf("fully pinned, so it should be listed as cooked in this test " +
					"and its header comment updated")
			}
		})
	}
}

// TestExamplesDocumented guards the README index: an example nobody points at
// is an example nobody reads, and the reasoning is the reason these exist.
func TestExamplesDocumented(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	files, err := filepath.Glob("*.yaml")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, file := range files {
		if !strings.Contains(string(readme), file) {
			t.Errorf("%s is not listed in README.md", file)
		}
	}
}
