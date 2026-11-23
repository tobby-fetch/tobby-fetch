// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

const consumptionDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func consumptionStore(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(context.Background(), root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close() //nolint:errcheck // test
	records := []store.RecipeRecord{
		{Name: "rke2", Version: "1.36.5", Ingredients: []store.IngredientRecord{
			{Name: "runtime", Kind: "ContainerImage", Repo: "registry.rancher.com/rancher/rke2-runtime", Tag: "v1", Digest: consumptionDigest},
			{Name: "rpms", Kind: "FileSet", Repo: "docker.io/acme/rpms", Tag: "1", Digest: consumptionDigest},
		}},
		{Name: "app", Version: "1.0.0", Ingredients: []store.IngredientRecord{
			{Name: "busybox", Kind: "ContainerImage", Repo: "docker.io/library/busybox", Tag: "1.36", Digest: consumptionDigest},
		}},
	}
	for i := range records {
		if err := st.PutRecipeRecord(&records[i]); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runConsumption(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := New()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// TestConsumptionContractOfTheCrown pins the invocation the crown scenario
// uses: one YAML document on stdout, one JSON table with one entry per
// ingredient, FileSets included.
func TestConsumptionContractOfTheCrown(t *testing.T) {
	root := consumptionStore(t)
	out, err := runConsumption(t, "consumption", "registries-yaml", "--storage-root", root,
		"--recipe", "rke2@1.36.5", "--recipe", "app@1.0.0", "--destination", "10.180.20.5:5000")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Mirrors map[string]struct {
			Endpoint []string          `yaml:"endpoint"`
			Rewrite  map[string]string `yaml:"rewrite"`
		} `yaml:"mirrors"`
	}
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not one YAML document: %v\n%s", err, out)
	}
	for _, h := range []string{"registry.rancher.com", "docker.io"} {
		m := doc.Mirrors[h]
		if len(m.Endpoint) != 1 || m.Endpoint[0] != "https://10.180.20.5:5000" || m.Rewrite["^(.*)$"] != h+"/$1" {
			t.Errorf("%s mirror = %+v", h, m)
		}
	}

	out, err = runConsumption(t, "consumption", "table", "--storage-root", root,
		"--recipe", "rke2@1.36.5", "--recipe", "app@1.0.0", "--destination", "10.180.20.5:5000", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Destination string `json:"destination"`
		Entries     []struct {
			Recipe, RecipeVersion, Ingredient, Kind, Source, Destination, Digest string
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(out), &table); err != nil {
		t.Fatal(err)
	}
	if table.Destination != "10.180.20.5:5000" || len(table.Entries) != 3 {
		t.Fatalf("table = %+v", table)
	}
	found := false
	for _, e := range table.Entries {
		if e.Ingredient == "rpms" {
			found = true
			if e.Kind != "FileSet" || e.Source != "docker.io/acme/rpms:1" || e.Destination != "10.180.20.5:5000/docker.io/acme/rpms:1" {
				t.Errorf("fileset entry = %+v", e)
			}
		}
	}
	if !found {
		t.Error("the FileSet is missing from the table")
	}
	// The alias keeps the reserved name working.
	if out2, err := runConsumption(t, "consumption", "registries", "--storage-root", root, "--destination", "10.180.20.5:5000"); err != nil || !strings.Contains(out2, "mirrors:") {
		t.Errorf("alias: %v", err)
	}
}

func TestConsumptionRefusals(t *testing.T) {
	root := consumptionStore(t)
	// Neither destination.registry, consumption.registry nor --destination.
	_, err := runConsumption(t, "consumption", "table", "--storage-root", root)
	var te *taxonomy.Error
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeAidsNoRegistry {
		t.Errorf("no registry: err = %v", err)
	}
	if _, err := runConsumption(t, "consumption", "table", "--storage-root", root, "--destination", "https://x"); err == nil {
		t.Error("a URL destination was accepted")
	}
	_, err = runConsumption(t, "consumption", "table", "--storage-root", root, "--destination", "r.example", "--recipe", "ghost")
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeNotFound {
		t.Errorf("unknown recipe: err = %v", err)
	}
}
