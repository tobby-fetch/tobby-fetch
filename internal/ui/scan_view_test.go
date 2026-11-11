// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// scanStateBox lets a test change what the UI reads between two pages.
type scanStateBox struct {
	mu sync.Mutex
	s  ScanState
}

func (b *scanStateBox) get() ScanState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.s
}

func (b *scanStateBox) set(s *ScanState) {
	b.mu.Lock()
	b.s = *s
	b.mu.Unlock()
}

// TestScanBanners is the UI half of FR-031/FR-032/R-13: the degraded mode
// is a danger banner naming its code and its cause, the advisory policy a
// warning, a stale database a warning stating its age — on every page,
// and gone by themselves when the state is.
func TestScanBanners(t *testing.T) {
	box := &scanStateBox{}
	u := newTestUIWithOptions(t, &Options{Scan: box.get}, nil)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")

	box.set(&ScanState{
		Cause:  taxonomy.New(taxonomy.CodeScannerUnavailable, taxonomy.Params{"detail": "no trivy executable on the PATH"}),
		MaxAge: 168 * time.Hour,
	})
	for _, page := range []string{"/", "/content", "/tasks"} {
		body := get(t, mux, c, page, nil).Body.String()
		for _, want := range []string{
			"t-banner--danger", "Vulnerability scanning is degraded", "no trivy executable on the PATH", "TBY-SCN-001",
			"The vulnerability policy is advisory", "TBY-SCN-006",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %q", page, want)
			}
		}
	}

	// Usable, blocking, fresh: no scanning banner at all.
	box.set(&ScanState{Blocking: true, Scanner: scan.Info{Name: "trivy", Version: "0.76.2"}, Origin: "image",
		Database: &scan.Database{Version: "2", BuiltAt: time.Now().Add(-time.Hour)}, MaxAge: 168 * time.Hour})
	body := get(t, mux, c, "/", nil).Body.String()
	for _, absent := range []string{"Vulnerability scanning is degraded", "policy is advisory", "TBY-SCN-004"} {
		if strings.Contains(body, absent) {
			t.Errorf("a healthy blocking instance shows %q", absent)
		}
	}

	// Stale.
	box.set(&ScanState{Blocking: true, Scanner: scan.Info{Name: "trivy"},
		Database: &scan.Database{Version: "2", BuiltAt: time.Now().Add(-200 * time.Hour)}, MaxAge: 168 * time.Hour})
	body = get(t, mux, c, "/", nil).Body.String()
	if !strings.Contains(body, "TBY-SCN-004") || !strings.Contains(body, "beyond the 7d0h") {
		t.Error("a stale database shows no warning with its bound")
	}

	// The French shell says the same.
	body = get(t, mux, c, "/?lang=fr", map[string]string{"Accept-Language": "fr"}).Body.String()
	if !strings.Contains(body, "La base de vulnérabilités a") {
		t.Error("the stale banner is not localized")
	}
}

// TestScanIndicatorOnTheRetrieverScreen: R-13's "UI indicator shows both"
// — the age and the staleness — whether or not anything is wrong.
func TestScanIndicatorOnTheRetrieverScreen(t *testing.T) {
	box := &scanStateBox{}
	box.set(&ScanState{Blocking: false, Scanner: scan.Info{Name: "trivy", Version: "0.76.2"}, Origin: "channel",
		Database: &scan.Database{Version: "2", BuiltAt: time.Now().Add(-49 * time.Hour)}, MaxAge: 168 * time.Hour})
	u := newTestUIWithOptions(t, &Options{Scan: box.get}, nil)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")
	body := get(t, mux, c, "/admin/retriever", nil).Body.String()
	for _, want := range []string{"data-scan-indicator", "Advisory policy", "tobby-system channel", "Database age: 2d1h", "accepted up to 7d0h"} {
		if !strings.Contains(body, want) {
			t.Errorf("the indicator lacks %q", want)
		}
	}
	box.set(&ScanState{Cause: taxonomy.New(taxonomy.CodeScanDBMissing, taxonomy.Params{"repository": "ghcr.io/aquasecurity/trivy-db"}), MaxAge: time.Hour})
	body = get(t, mux, c, "/admin/retriever", nil).Body.String()
	for _, want := range []string{"No vulnerability database", "TBY-SCN-003", "unavailable"} {
		if !strings.Contains(body, want) {
			t.Errorf("the degraded indicator lacks %q", want)
		}
	}
}

// TestScanReportOnTheTaskDetail: the item's report — verdict, policy and
// the level that set it, database age, findings, reasons — is on the task
// screen, in the reader's language.
func TestScanReportOnTheTaskDetail(t *testing.T) {
	u, q, _ := newTestUIWithQueue(t)
	mux := mount(u)
	c := login(t, mux, "lecteur", "pw-view")
	score := 9.8
	p := scan.Combine(&scan.Levels{InstanceMode: scan.ModeWarn, InstanceThreshold: 7, RetrieverThreshold: &score})
	res := &scan.Result{
		Scanner: scan.Info{Name: "trivy", Version: "0.76.2"}, ScannedAt: time.Now(),
		Database: scan.Database{Version: "2", BuiltAt: time.Now().Add(-26 * time.Hour)},
		Findings: []scan.Finding{
			{ID: "CVE-2019-14697", Severity: scan.SeverityCritical, CVSS: &score, Package: "musl", InstalledVersion: "1.1.22-r2", FixedVersion: "1.1.22-r3"},
			{ID: "CVE-2099-0001", Severity: scan.SeverityUnknown, Package: "libx"},
		},
	}
	rep := scan.Evaluate(&p, scan.Subject{Reference: "docker.io/example/vulnerable@sha256:aa", Recipe: "vulnerable@1.0.0"}, res,
		scan.Freshness{MaxAge: 168 * time.Hour}, time.Now())
	created, err := q.Create(tasks.TypeSync, "retriever.yaml", "alexis", []tasks.Item{
		{Name: "vulnerable@1.0.0/app", Digest: "sha256:aa", Status: tasks.StatusDone, Scan: rep},
	})
	if err != nil {
		t.Fatal(err)
	}
	for lang, wants := range map[string][]string{
		"en": {"Vulnerability scan", "Blocked by the policy", "Blocking policy, made so at the Retriever level", "Database age: 1d2h", "CVE-2019-14697", "data-scan-violation", "TBY-SCN-005", "TBY-SCN-022"},
		"fr": {"Analyse de vulnérabilités", "Bloqué par la politique", "rendue telle au niveau Retriever", "Âge de la base : 1d2h"},
	} {
		body := get(t, mux, c, fmt.Sprintf("/tasks/%s?lang=%s", created.ID, lang), map[string]string{"Accept-Language": lang}).Body.String()
		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("[%s] the task detail lacks %q", lang, w)
			}
		}
	}
}

func TestScanItemViewBoundsTheTable(t *testing.T) {
	var fs []scan.Finding
	for i := range maxFindingRows + 7 {
		v := float64(i%10) + 0.5
		fs = append(fs, scan.Finding{ID: fmt.Sprintf("CVE-2024-%04d", i), Severity: scan.SeverityLow, CVSS: &v})
	}
	p := scan.Combine(&scan.Levels{InstanceMode: scan.ModeWarn, InstanceThreshold: 7})
	v := scanItemViewOf("en", scan.Evaluate(&p, scan.Subject{}, &scan.Result{Findings: fs}, scan.Freshness{}, time.Now()))
	if len(v.Findings) != maxFindingRows || v.More != 7 {
		t.Fatalf("rows = %d more = %d", len(v.Findings), v.More)
	}
	if v.Findings[0].Score != "9.5" {
		t.Errorf("the worst finding is not first: %+v", v.Findings[0])
	}
	if scanItemViewOf("en", nil) != nil {
		t.Error("no report rendered a section")
	}
}
