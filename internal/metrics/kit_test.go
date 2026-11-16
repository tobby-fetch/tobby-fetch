// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The supervision kit ships with the chart (R-11): alert rules and a
// dashboard. FR-091 asks that "every expression references only families
// of the published reference" — enforced here, against the files the chart
// ships, so a rule cannot name a family that does not exist, nor a label
// value outside the closed set the reference documents.

const (
	rulesFile     = "../../deploy/charts/tobby/files/tobby-alerts.rules.yaml"
	dashboardFile = "../../deploy/charts/tobby/files/tobby-dashboard.json"
)

var (
	familyName      = regexp.MustCompile(`\btobby_[a-z0-9_]+`)
	selector        = regexp.MustCompile(`(tobby_[a-z0-9_]+)\{([^}]*)\}`)
	matcher         = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*(=~|!~|!=|=)\s*"([^"]*)"`)
	histogramSuffix = regexp.MustCompile(`_(bucket|sum|count)$`)
)

// referenceFamilies maps each family to its documentation.
func referenceFamilies(t *testing.T) map[string]FamilyDoc {
	t.Helper()
	doc, err := Describe()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]FamilyDoc{}
	for _, f := range doc.Families {
		out[f.Name] = f
	}
	return out
}

// checkExpression holds one PromQL expression to the reference.
func checkExpression(t *testing.T, where, expr string, ref map[string]FamilyDoc) {
	t.Helper()
	for _, name := range familyName.FindAllString(expr, -1) {
		base := name
		if _, ok := ref[base]; !ok && histogramSuffix.MatchString(name) {
			base = histogramSuffix.ReplaceAllString(name, "")
		}
		if _, ok := ref[base]; !ok {
			t.Errorf("%s names %q, which is not a family of the published reference:\n  %s", where, name, expr)
		}
	}
	for _, sel := range selector.FindAllStringSubmatch(expr, -1) {
		fam := sel[1]
		if _, ok := ref[fam]; !ok {
			fam = histogramSuffix.ReplaceAllString(fam, "")
		}
		doc, ok := ref[fam]
		if !ok {
			continue
		}
		labels := map[string]LabelDoc{"le": {Name: "le"}}
		for _, l := range doc.Labels {
			labels[l.Name] = l
		}
		for _, m := range matcher.FindAllStringSubmatch(sel[2], -1) {
			l, ok := labels[m[1]]
			switch {
			case !ok && m[1] != "instance" && m[1] != "job" && m[1] != "namespace" && m[1] != "pod":
				t.Errorf("%s selects %s by label %q, which the reference does not document", where, fam, m[1])
			case ok && l.Values != nil && m[2] == "=" && !member(l.Values, m[3]):
				t.Errorf("%s selects %s{%s=%q}, outside the documented set %v", where, fam, m[1], m[3], l.Values)
			}
		}
	}
}

func TestAlertRulesNameOnlyPublishedFamilies(t *testing.T) {
	ref := referenceFamilies(t)
	raw, err := os.ReadFile(filepath.FromSlash(rulesFile))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Groups []struct {
			Name  string `yaml:"name"`
			Rules []struct {
				Alert       string            `yaml:"alert"`
				Expr        string            `yaml:"expr"`
				For         string            `yaml:"for"`
				Labels      map[string]string `yaml:"labels"`
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, g := range doc.Groups {
		for _, r := range g.Rules {
			have[r.Alert] = true
			checkExpression(t, "alert "+r.Alert, r.Expr, ref)
			if r.Labels["severity"] == "" || r.Annotations["summary"] == "" || r.Annotations["description"] == "" {
				t.Errorf("alert %s lacks a severity, a summary or a description", r.Alert)
			}
		}
	}
	// FR-091 amendment: the conditions the kit must cover.
	for _, want := range []string{
		"TobbySyncFailing", "TobbySyncStale", "TobbySyncSaturated", "TobbyStoreOccupancyHigh",
		"TobbyVulnerabilityDatabaseStale", "TobbyVulnerabilityDatabaseAbsent", "TobbyScannerDegraded",
		"TobbyServerCertificateExpiring", "TobbyClockImplausible",
	} {
		if !have[want] {
			t.Errorf("the rules file has no %s", want)
		}
	}
}

func TestDashboardNamesOnlyPublishedFamilies(t *testing.T) {
	ref := referenceFamilies(t)
	raw, err := os.ReadFile(filepath.FromSlash(dashboardFile))
	if err != nil {
		t.Fatal(err)
	}
	var dash struct {
		Panels []struct {
			Title   string `json:"title"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
		Templating struct {
			List []struct {
				Query string `json:"query"`
			} `json:"list"`
		} `json:"templating"`
	}
	if err := json.Unmarshal(raw, &dash); err != nil {
		t.Fatalf("the dashboard is not JSON: %v", err)
	}
	if len(dash.Panels) < 10 {
		t.Errorf("only %d panels", len(dash.Panels))
	}
	for _, p := range dash.Panels {
		for _, tg := range p.Targets {
			checkExpression(t, "panel "+strings.TrimSpace(p.Title), tg.Expr, ref)
		}
	}
	for _, v := range dash.Templating.List {
		if v.Query != "" && strings.Contains(v.Query, "tobby_") {
			checkExpression(t, "variable query", v.Query, ref)
		}
	}
}
