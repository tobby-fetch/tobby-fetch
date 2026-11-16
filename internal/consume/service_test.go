// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package consume

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

type fakeSource []store.RecipeRecord

func (f fakeSource) RecipeRecords() ([]store.RecipeRecord, error) { return f, nil }

const (
	d1 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	d2 = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	d3 = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
)

func graph() fakeSource {
	return fakeSource{
		{Name: "wordpress", Version: "6.5.2", Ingredients: []store.IngredientRecord{
			{Name: "wp", Kind: "ContainerImage", Repo: "docker.io/bitnami/wordpress", Tag: "6.5.2", Digest: d1},
			{Name: "chart", Kind: "HelmChart", Repo: "ghcr.io/acme/charts/wordpress", Tag: "1.0.0", Digest: d2},
			{Name: "db", Kind: "ContainerImage", Repo: "forge.example.com_5050/team/mariadb", Tag: "11", Digest: d3},
		}},
		{Name: "debs", Version: "1.0.0", Ingredients: []store.IngredientRecord{
			{Name: "debs", Kind: "FileSet", Repo: "docker.io/acme/debs", Tag: "1.0.0", Digest: d2},
		}},
	}
}

func service(base string, mod func(*Settings)) *Service {
	set := Settings{Registry: "reg.zone.example:5000", BasePath: base, Version: "v9.9.9"}
	if mod != nil {
		mod(&set)
	}
	return New(graph(), &set)
}

func TestTableMapsEveryIngredient(t *testing.T) {
	tab, err := service("", nil).Table(nil)
	if err != nil {
		t.Fatal(err)
	}
	if tab.Destination != "reg.zone.example:5000" || len(tab.Entries) != 4 {
		t.Fatalf("table = %+v", tab)
	}
	// Ordered by recipe then version; ingredients in recipe order.
	want := []struct{ ing, src, dst string }{
		{"debs", "docker.io/acme/debs:1.0.0", "reg.zone.example:5000/docker.io/acme/debs:1.0.0"},
		{"wp", "docker.io/bitnami/wordpress:6.5.2", "reg.zone.example:5000/docker.io/bitnami/wordpress:6.5.2"},
		{"chart", "ghcr.io/acme/charts/wordpress:1.0.0", "reg.zone.example:5000/ghcr.io/acme/charts/wordpress:1.0.0"},
		// A port is written host:port at the source and host_port at the destination.
		{"db", "forge.example.com:5050/team/mariadb:11", "reg.zone.example:5000/forge.example.com_5050/team/mariadb:11"},
	}
	for i, w := range want {
		e := tab.Entries[i]
		if e.Ingredient != w.ing || e.Source != w.src || e.Destination != w.dst {
			t.Errorf("entry %d = %s %s -> %s, want %s %s -> %s", i, e.Ingredient, e.Source, e.Destination, w.ing, w.src, w.dst)
		}
	}
}

func TestTableBasePrefixes(t *testing.T) {
	// The store was written under "zone", the destination publishes under "mirror".
	src := fakeSource{{Name: "a", Version: "1", Ingredients: []store.IngredientRecord{
		{Name: "x", Kind: "ContainerImage", Repo: "zone/docker.io/library/busybox", Tag: "1.36", Digest: d1},
	}}}
	set := Settings{Registry: "r.example", BasePath: "mirror", StoragePrefix: "zone"}
	tab, err := New(src, &set).Table(nil)
	if err != nil {
		t.Fatal(err)
	}
	e := tab.Entries[0]
	if e.Source != "docker.io/library/busybox:1.36" || e.Destination != "r.example/mirror/docker.io/library/busybox:1.36" {
		t.Errorf("entry = %+v", e)
	}
	// A record the configured prefix does not explain is refused, never guessed.
	set.StoragePrefix = "other"
	if _, err := New(src, &set).Table(nil); err == nil || !strings.Contains(err.Error(), "storage.basePrefix") {
		t.Errorf("foreign prefix: err = %v", err)
	}
	set.StoragePrefix = ""
	if _, err := New(src, &set).Table(nil); err == nil {
		t.Error("an unprefixed configuration read a prefixed path without complaint")
	}
}

func TestDigestOnlyIngredient(t *testing.T) {
	src := fakeSource{{Name: "a", Version: "1", Ingredients: []store.IngredientRecord{
		{Name: "x", Kind: "OCIArtifact", Repo: "ghcr.io/a/b", Digest: d1},
	}}}
	tab, err := New(src, &Settings{Registry: "r.example"}).Table(nil)
	if err != nil {
		t.Fatal(err)
	}
	if tab.Entries[0].Source != "ghcr.io/a/b@"+d1 || tab.Entries[0].Destination != "r.example/ghcr.io/a/b@"+d1 {
		t.Errorf("entry = %+v", tab.Entries[0])
	}
}

func TestSelection(t *testing.T) {
	svc := service("", nil)
	tab, err := svc.Table([]Selector{{Name: "debs", Version: "1.0.0"}})
	if err != nil || len(tab.Entries) != 1 {
		t.Fatalf("select name@version: %v %+v", err, tab)
	}
	// A name and its own name@version select the recipe once.
	tab, _ = svc.Table([]Selector{{Name: "debs"}, {Name: "debs", Version: "1.0.0"}})
	if len(tab.Entries) != 1 {
		t.Errorf("a recipe named twice was listed %d times", len(tab.Entries))
	}
	var te *taxonomy.Error
	if _, err := svc.Table([]Selector{{Name: "nope"}}); !errors.As(err, &te) || te.Code() != taxonomy.CodeNotFound {
		t.Errorf("unknown recipe: err = %v", err)
	}
	if _, err := svc.Table([]Selector{{Name: "debs", Version: "9"}}); !errors.As(err, &te) || te.Code() != taxonomy.CodeNotFound {
		t.Errorf("unknown version: err = %v", err)
	}
}

func TestNoRegistryIsRefusedWithItsCode(t *testing.T) {
	svc := service("", func(s *Settings) { s.Registry = "" })
	var te *taxonomy.Error
	if _, err := svc.Table(nil); !errors.As(err, &te) || te.Code() != taxonomy.CodeAidsNoRegistry {
		t.Errorf("table: err = %v", err)
	}
	if _, err := svc.RegistriesYAML(nil); !errors.As(err, &te) || te.Code() != taxonomy.CodeAidsNoRegistry {
		t.Errorf("registries: err = %v", err)
	}
}

func TestExportFormatsCarryTheSameRows(t *testing.T) {
	tab, err := service("", nil).Table(nil)
	if err != nil {
		t.Fatal(err)
	}
	asJSON, _ := tab.Render(FormatJSON)
	var doc Table
	if err := json.Unmarshal(asJSON, &doc); err != nil || len(doc.Entries) != len(tab.Entries) {
		t.Fatalf("json: %v %s", err, asJSON)
	}
	asCSV, _ := tab.Render(FormatCSV)
	recs, err := csv.NewReader(bytes.NewReader(asCSV)).ReadAll()
	if err != nil || len(recs) != len(tab.Entries)+1 {
		t.Fatalf("csv: %v %q", err, asCSV)
	}
	asMD, _ := tab.Render(FormatMarkdown)
	lines := strings.Split(strings.TrimSpace(string(asMD)), "\n")
	if len(lines) != len(tab.Entries)+2 {
		t.Fatalf("markdown has %d lines for %d entries", len(lines), len(tab.Entries))
	}
	for i, e := range tab.Entries {
		if recs[i+1][4] != e.Source || recs[i+1][5] != e.Destination || recs[i+1][6] != e.Digest {
			t.Errorf("csv row %d = %v, want %+v", i, recs[i+1], e)
		}
		if !strings.Contains(lines[i+2], e.Destination) || !strings.Contains(lines[i+2], e.Digest) {
			t.Errorf("markdown row %d = %q", i, lines[i+2])
		}
		if doc.Entries[i] != (Row{Recipe: e.Recipe, RecipeVersion: e.RecipeVersion, Ingredient: e.Ingredient, Kind: e.Kind, Source: e.Source, Destination: e.Destination, Digest: e.Digest}) {
			t.Errorf("json row %d = %+v", i, doc.Entries[i])
		}
	}
	if !strings.Contains(string(asJSON), `"recipeVersion"`) || strings.Contains(string(asJSON), `"nominal"`) {
		t.Errorf("json carries the wrong columns: %s", asJSON)
	}
}

type snippet struct {
	Mirrors map[string]struct {
		Endpoint []string          `yaml:"endpoint"`
		Rewrite  map[string]string `yaml:"rewrite"`
	} `yaml:"mirrors"`
	Configs map[string]map[string]any `yaml:"configs"`
}

func parse(t *testing.T, b []byte) snippet {
	t.Helper()
	var s snippet
	if err := yaml.Unmarshal(b, &s); err != nil {
		t.Fatalf("the snippet is not YAML: %v\n%s", err, b)
	}
	return s
}

func TestRegistriesYAML(t *testing.T) {
	out, err := service("", func(s *Settings) { s.Insecure = true }).RegistriesYAML(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := parse(t, out)
	// Images only: the chart host and the FileSet are not mirrored (the
	// FileSet shares docker.io, which an image already mirrors).
	if len(s.Mirrors) != 2 {
		t.Fatalf("mirrors = %v\n%s", s.Mirrors, out)
	}
	hub := s.Mirrors["docker.io"]
	if len(hub.Endpoint) != 1 || hub.Endpoint[0] != "http://reg.zone.example:5000" || hub.Rewrite["^(.*)$"] != "docker.io/$1" {
		t.Errorf("docker.io mirror = %+v", hub)
	}
	forge := s.Mirrors["forge.example.com:5050"]
	if forge.Rewrite["^(.*)$"] != "forge.example.com_5050/$1" {
		t.Errorf("forge mirror = %+v", forge)
	}
	if _, ok := s.Mirrors["ghcr.io"]; ok {
		t.Error("a host holding only a chart was mirrored")
	}
	if s.Configs != nil {
		t.Errorf("no CA declared, yet configs = %v", s.Configs)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "password") || strings.Contains(line, "username") {
			if !strings.HasPrefix(strings.TrimSpace(line), "#") {
				t.Errorf("a credential key outside a comment: %q", line)
			}
		}
	}
	for _, must := range []string{"v9.9.9", "reg.zone.example:5000", "wordpress@6.5.2", "admission policies", "never rewrites"} {
		if !strings.Contains(string(out), must) {
			t.Errorf("header lacks %q:\n%s", must, out)
		}
	}
}

func TestRegistriesYAMLSchemeBaseAndCA(t *testing.T) {
	out, err := service("mirror/zone", func(s *Settings) { s.CAFile = "/etc/ssl/zone-ca.crt" }).RegistriesYAML([]Selector{{Name: "wordpress"}})
	if err != nil {
		t.Fatal(err)
	}
	s := parse(t, out)
	hub := s.Mirrors["docker.io"]
	if hub.Endpoint[0] != "https://reg.zone.example:5000" || hub.Rewrite["^(.*)$"] != "mirror/zone/docker.io/$1" {
		t.Errorf("mirror = %+v", hub)
	}
	tls, _ := s.Configs["reg.zone.example:5000"]["tls"].(map[string]any)
	if tls["ca_file"] != "/etc/ssl/zone-ca.crt" {
		t.Errorf("configs = %v", s.Configs)
	}
	if _, hasAuth := s.Configs["reg.zone.example:5000"]["auth"]; hasAuth {
		t.Error("an auth block was generated")
	}
}

func TestRegistriesYAMLIsDeterministic(t *testing.T) {
	a, _ := service("", nil).RegistriesYAML(nil)
	b, _ := service("", nil).RegistriesYAML(nil)
	if !bytes.Equal(a, b) {
		t.Error("two generations differ")
	}
	ta, _ := service("", nil).Table(nil)
	tb, _ := service("", nil).Table(nil)
	ja, _ := ta.Render(FormatCSV)
	jb, _ := tb.Render(FormatCSV)
	if !bytes.Equal(ja, jb) {
		t.Error("two tables differ")
	}
}

func TestFromConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Storage.BasePrefix = "zone"
	if set := FromConfig(&cfg, "v1"); set.Registry != "" {
		t.Errorf("registry without any configuration = %q", set.Registry)
	}
	// This instance serves the zone itself: the store's own prefix.
	cfg.Consumption.Registry = "Tobby.Zone.Example:8443"
	set := FromConfig(&cfg, "v1")
	if set.Registry != "tobby.zone.example:8443" || set.BasePath != "zone" || set.StoragePrefix != "zone" {
		t.Errorf("serving instance: %+v", set)
	}
	// It pushes to a destination: where it pushed.
	cfg.Destination.Registry = "reg.zone.example"
	cfg.Destination.BasePath = "mirror"
	cfg.Consumption.Registry = ""
	cfg.Registries.Insecure = []string{"REG.zone.example"}
	set = FromConfig(&cfg, "v1")
	if set.Registry != "reg.zone.example" || set.BasePath != "mirror" || !set.Insecure {
		t.Errorf("pushing instance: %+v", set)
	}
	// consumption.registry wins as the registry, destination.basePath still applies.
	cfg.Consumption.Registry = "pull.zone.example"
	set = FromConfig(&cfg, "v1")
	if set.Registry != "pull.zone.example" || set.BasePath != "mirror" || set.Insecure {
		t.Errorf("aliased registry: %+v", set)
	}
}

func TestParseSelector(t *testing.T) {
	for in, want := range map[string]Selector{"rke2": {Name: "rke2"}, "rke2@1.36.5": {Name: "rke2", Version: "1.36.5"}} {
		if got, err := ParseSelector(in); err != nil || got != want {
			t.Errorf("ParseSelector(%q) = %+v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "@1", "a@", "a@b@c", "a/b"} {
		if _, err := ParseSelector(bad); err == nil {
			t.Errorf("ParseSelector(%q) accepted", bad)
		}
	}
}
