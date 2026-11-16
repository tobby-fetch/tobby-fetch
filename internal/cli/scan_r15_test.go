// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/hardening"
	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestServeWiresTheExceptions: the serve wiring reads the VEX documents,
// adds their exceptions, states the expired ones at startup, exposes
// tobby_scan_exceptions, and counts a re-blocked item.
func TestServeWiresTheExceptions(t *testing.T) {
	cfg := config.Default()
	cfg.Storage.Root = filepath.Join(t.TempDir(), "store")
	cfg.State.Root = t.TempDir()
	cfg.Scan.Trivy.Path = filepath.Join(t.TempDir(), "absent", "trivy")
	cfg.Scan.Exceptions = []config.ScanException{
		{ID: "CVE-2024-11111", Justification: "j", Expires: time.Now().Add(time.Hour)},
		{ID: "CVE-2024-22222", Justification: "j", Expires: time.Now().Add(-time.Hour)},
	}
	cfg.Scan.VEX = []config.VEXDocument{{Path: writeVEX(t), Expires: time.Now().Add(time.Hour)}}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	docs, err := loadVEX(context.Background(), &cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), cfg.Storage.Root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := metrics.New()
	eng := engine.New(st, nil, nil, "", "", cfg.Sync)
	s := setupScanning(context.Background(), &cfg, st, eng, reg, logger, docs, hardening.Switches{})
	t.Cleanup(s.close)

	for _, want := range []string{`"msg":"OpenVEX document read"`, `"admitted":1`, `"reason":"impact-statement-only"`,
		`"msg":"scan exception expired: the findings it covered are judged again"`, `"id":"CVE-2024-22222"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("startup log lacks %s:\n%s", want, logs.String())
		}
	}
	v := s.policy.View()
	if len(v.Exceptions) != 3 || v.Exceptions[2].Origin != scan.SourceVEX {
		t.Errorf("policy = %+v", v.Exceptions)
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, f := range families {
		if f.GetName() == metrics.MetricScanExceptions {
			for _, m := range f.GetMetric() {
				key := ""
				for _, l := range m.GetLabel() {
					key += l.GetName() + "=" + l.GetValue() + " "
				}
				got[strings.TrimSpace(key)] = m.GetGauge().GetValue()
			}
		}
	}
	if got["source=instance state=active"] != 1 || got["source=instance state=expired"] != 1 || got["source=vex state=active"] != 1 || len(got) != 6 {
		t.Errorf("%s = %v", metrics.MetricScanExceptions, got)
	}

	// A report re-blocked by an expired exception moves the counter; one
	// that is not does not.
	observe := observeScan(reg)
	p := scan.Combine(&scan.Levels{InstanceMode: scan.ModeBlock, InstanceThreshold: 7,
		InstanceExceptions: []scan.Exception{{ID: "CVE-2024-22222", Expires: time.Now().Add(-time.Hour)}}})
	score := 9.8
	res := &scan.Result{Findings: []scan.Finding{{ID: "CVE-2024-22222", Severity: scan.SeverityCritical, CVSS: &score}}}
	observe(scan.Evaluate(&p, scan.Subject{Reference: "r"}, res, scan.Freshness{}, time.Now()))
	observe(scan.Evaluate(&p, scan.Subject{Reference: "r"}, &scan.Result{}, scan.Freshness{}, time.Now()))
	if v := counterValue(t, reg, metrics.MetricScanReblocked); v != 1 {
		t.Errorf("%s = %v, want 1", metrics.MetricScanReblocked, v)
	}
}

func counterValue(t *testing.T, reg *metrics.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}
	t.Fatalf("%s is not registered", name)
	return 0
}

// TestServeRefusesAnInvalidVEX: a scan.vex document that cannot be used
// refuses the start, naming the file (TBY-SCN-027), before anything is
// opened.
func TestServeRefusesAnInvalidVEX(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = config.ModeMirror
	cfg.Storage.Root = filepath.Join(t.TempDir(), "store")
	cfg.State.Root = t.TempDir()
	bad := filepath.Join(t.TempDir(), "bad.openvex.json")
	cfg.Scan.VEX = []config.VEXDocument{{Path: bad, Expires: time.Now().Add(time.Hour)}}
	err := runServe(context.Background(), &cfg)
	var te *taxonomy.Error
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeScanVEXInvalid || !strings.Contains(te.Error(), "bad.openvex.json") {
		t.Fatalf("runServe = %v, want TBY-SCN-027 naming the file", err)
	}
}
