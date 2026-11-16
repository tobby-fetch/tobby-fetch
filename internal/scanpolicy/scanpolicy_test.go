// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package scanpolicy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

const doc = `{"@context":"https://openvex.dev/ns/v0.2.0","@id":"https://vex.example/p","author":"a","timestamp":"2026-11-16T10:00:00Z","version":1,
"statements":[{"vulnerability":{"name":"CVE-2024-44444"},"products":[{"@id":"ghcr.io/team/app"}],"status":"not_affected","justification":"component_not_present"}]}`

func writeDoc(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "site.openvex.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestViewAndCountsFollowTheClock: the states and the gauge are computed
// at the instant they are asked for — an expiry that passes on a running
// instance moves both, with no restart.
func TestViewAndCountsFollowTheClock(t *testing.T) {
	t0 := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	clock := t0
	path := writeDoc(t, doc)
	docs, err := LoadDocuments([]config.VEXDocument{{Path: path, Expires: t0.Add(48 * time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Scan.Exceptions = []config.ScanException{{ID: "CVE-2024-11111", Justification: "j", Expires: t0.Add(time.Hour)}}
	retriever := []scan.Exception{{ID: "CVE-2024-33333", Level: scan.LevelRetriever}}
	src := New(&Options{Scan: &cfg.Scan, Documents: docs,
		Retriever: func() ([]scan.Exception, bool) { return retriever, true },
		Now:       func() time.Time { return clock }})

	v := src.View()
	if len(v.Exceptions) != 3 || len(v.Documents) != 1 || v.Documents[0].Admitted != 1 || v.Documents[0].Exceptions != 1 || !v.RetrieverKnown {
		t.Fatalf("view = %+v", v)
	}
	if v.Exceptions[1].Origin != "vex" || v.Exceptions[1].Document != path || v.Exceptions[1].Scope.Repository != "ghcr.io/team/app" {
		t.Errorf("vex exception = %+v", v.Exceptions[1])
	}
	want := metrics.ExceptionCounts{"instance": {"active": 1}, "vex": {"active": 1}, "retriever": {"active": 1}}
	if got := src.Counts(); !equal(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}

	clock = t0.Add(72 * time.Hour)
	v = src.View()
	if v.Exceptions[0].State != StateExpired || v.Exceptions[1].State != StateExpired || v.Exceptions[2].State != StateUnbounded || v.Documents[0].State != StateExpired {
		t.Errorf("after the expiries: %+v", v.Exceptions)
	}
	want = metrics.ExceptionCounts{"instance": {"expired": 1}, "vex": {"expired": 1}, "retriever": {"active": 1}}
	if got := src.Counts(); !equal(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
}

func equal(a, b metrics.ExceptionCounts) bool {
	for _, s := range []string{"instance", "retriever", "vex"} {
		for _, st := range []string{"active", "expired"} {
			if a[s][st] != b[s][st] {
				return false
			}
		}
	}
	return true
}

// TestLoadDocumentsRefusesTheStart: a document that cannot be read or
// validated is TBY-SCN-027 naming the file.
func TestLoadDocumentsRefusesTheStart(t *testing.T) {
	for name, path := range map[string]string{
		"malformed": writeDoc(t, strings.Replace(doc, `"version":1`, `"version":1,"extra":true`, 1)),
		"absent":    filepath.Join(t.TempDir(), "absent.json"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadDocuments([]config.VEXDocument{{Path: path, Expires: time.Now().Add(time.Hour)}})
			var te *taxonomy.Error
			if !errors.As(err, &te) || te.Code() != taxonomy.CodeScanVEXInvalid || te.ParamsMap()["path"] != path {
				t.Errorf("err = %v, want TBY-SCN-027 naming %s", err, path)
			}
		})
	}
}

// TestViewWithoutRetriever: the command line knows no Retriever; the view
// says so rather than listing none.
func TestViewWithoutRetriever(t *testing.T) {
	cfg := config.Default()
	v := New(&Options{Scan: &cfg.Scan}).View()
	if v.RetrieverKnown || v.Exceptions == nil || v.Documents == nil || v.Blocking {
		t.Errorf("view = %+v", v)
	}
}
