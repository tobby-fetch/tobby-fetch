// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/hardening"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/scanpolicy"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// TestPolicyScreen renders /admin/policy in both languages: every
// exception with its origin, scope and state, the unbounded Retriever
// exception marked TBY-SCN-028, the note that the Retriever is not known
// yet when it is not, and both hardening switches; the screen is reached
// from the administration sub-menu.
func TestPolicyScreen(t *testing.T) {
	cfg := config.Default()
	cfg.Scan.Mode = config.ScanModeBlock
	cfg.Scan.Exceptions = []config.ScanException{
		{ID: "CVE-2024-11111", Justification: "parser disabled", Expires: time.Now().Add(24 * time.Hour), Scope: config.ExceptionScope{Recipe: "wordpress"}},
		{ID: "CVE-2024-22222", Justification: "legacy", Expires: time.Now().Add(-time.Hour)},
	}
	known := true
	src := scanpolicy.New(&scanpolicy.Options{
		Scan: &cfg.Scan, Hardening: hardening.Switches{DisableUnitImport: true},
		Retriever: func() ([]scan.Exception, bool) {
			if !known {
				return nil, false
			}
			return []scan.Exception{{ID: "CVE-2024-33333", Level: scan.LevelRetriever}}, true
		},
	})
	u := newTestUIWithOptions(t, &Options{Policy: src}, nil)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")
	for lang, wants := range map[string][]string{
		"en": {"Content policy", `data-policy-exception="CVE-2024-11111"`, `data-policy-state="active"`, "recipe", "wordpress",
			`data-policy-exception="CVE-2024-22222"`, `data-policy-state="expired"`, `data-policy-state="unbounded"`, "TBY-SCN-028",
			"Every item of its level", "hardening.disableUnitImport", `href="/admin/policy"`},
		"fr": {"Politique de contenu", "Dérogations", "échue", "sans limite", "Tous les éléments de son niveau"},
	} {
		body := get(t, mux, c, fmt.Sprintf("/admin/policy?lang=%s", lang), map[string]string{"Accept-Language": lang}).Body.String()
		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("[%s] /admin/policy lacks %q", lang, w)
			}
		}
	}
	known = false
	if body := get(t, mux, c, "/admin/policy", nil).Body.String(); !strings.Contains(body, "data-policy-retriever-unknown") {
		t.Error("the screen does not say the Retriever's exceptions are not known yet")
	}
}

// TestScanReportMarkers: beside each finding, the task report says why it
// is or is not excepted — an exception out of scope for this item, an
// expired one, a VEX-born one with its document, an unbounded one.
func TestScanReportMarkers(t *testing.T) {
	u, q, _ := newTestUIWithQueue(t)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")
	digest := "sha256:" + strings.Repeat("a", 64)
	score := 9.8
	now := time.Now()
	p := scan.Combine(&scan.Levels{InstanceMode: scan.ModeBlock, InstanceThreshold: 7,
		InstanceExceptions: []scan.Exception{
			{ID: "CVE-2024-00001", Expires: now.Add(time.Hour), Scope: &scan.ExceptionScope{Digest: "sha256:" + strings.Repeat("b", 64)}},
			{ID: "CVE-2024-00002", Expires: now.Add(-time.Hour)},
			{ID: "CVE-2024-00003", Expires: now.Add(time.Hour), VEX: "/etc/tobby/site.openvex.json", Scope: &scan.ExceptionScope{Digest: digest}},
		},
		RetrieverExceptions: []scan.Exception{{ID: "CVE-2024-00004"}},
	})
	var findings []scan.Finding
	for i := 1; i <= 4; i++ {
		findings = append(findings, scan.Finding{ID: fmt.Sprintf("CVE-2024-%05d", i), Severity: scan.SeverityCritical, CVSS: &score, Package: "openssl"})
	}
	rep := scan.Evaluate(&p, scan.Subject{Reference: "docker.io/library/nginx@" + digest, Recipe: "nginx@1.0.0", Digest: digest, Repository: "docker.io/library/nginx"},
		&scan.Result{Scanner: scan.Info{Name: "trivy", Version: "0.76.2"}, ScannedAt: now, Database: scan.Database{Version: "2", BuiltAt: now}, Findings: findings},
		scan.Freshness{MaxAge: time.Hour}, now)
	created, err := q.Create(tasks.TypeSync, "retriever.yaml", "alexis", []tasks.Item{
		{Name: "nginx@1.0.0/app", Digest: digest, Status: tasks.StatusDone, Scan: rep},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, c, "/tasks/"+created.ID, nil).Body.String()
	for _, w := range []string{
		`data-scan-marker="out-of-scope"`, "Exception CVE-2024-00001 does not cover this item", "TBY-SCN-026",
		`data-scan-marker="expired"`, "TBY-SCN-021",
		`data-scan-marker="vex"`, "From the VEX document /etc/tobby/site.openvex.json",
		`data-scan-marker="unbounded"`, "Exception CVE-2024-00004 has no expiry", "TBY-SCN-028",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("the task report lacks %q", w)
		}
	}
}
