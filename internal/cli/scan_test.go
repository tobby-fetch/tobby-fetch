// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
	"github.com/tobby-fetch/tobby-fetch/internal/server"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestSetupScanningAnnouncesTheDegradedMode is the FR-031 acceptance of the
// degraded mode on the startup surfaces: with the scanner binary removed,
// the log record, the metric and the interface state all say so — and the
// advisory default is announced too.
func TestSetupScanningAnnouncesTheDegradedMode(t *testing.T) {
	cfg := config.Default()
	cfg.Storage.Root = filepath.Join(t.TempDir(), "store")
	cfg.State.Root = t.TempDir()
	cfg.Scan.Trivy.Path = filepath.Join(t.TempDir(), "removed", "trivy")
	st, err := store.Open(context.Background(), cfg.Storage.Root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := engine.New(st, nil, nil, "", "", cfg.Sync)
	reg := metrics.New()
	var logs bytes.Buffer
	s := setupScanning(context.Background(), &cfg, st, eng, reg, slog.New(slog.NewJSONHandler(&logs, nil)))

	for _, want := range []string{
		"vulnerability scanning degraded", string(taxonomy.CodeScannerUnavailable), "vulnerability policy is advisory",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("startup log lacks %q:\n%s", want, logs.String())
		}
	}
	ui := s.uiState()
	if ui.Cause == nil || ui.Cause.Code() != taxonomy.CodeScannerUnavailable || ui.Blocking {
		t.Errorf("ui state = %+v", ui)
	}
	ms := s.metricState()
	if ms.ScannerAvailable || ms.DBPresent || ms.PolicyBlocking {
		t.Errorf("metric state = %+v", ms)
	}
}

// TestServeExposesTheScanState: an instance started without a scanner
// serves the degraded mode on /metrics from the first scrape.
func TestServeExposesTheScanState(t *testing.T) {
	cfg := serveConfig(t)
	cfg.Mode = config.ModePassthrough
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Shutdown.GracePeriod = config.Duration(2 * time.Second)
	cfg.Scan.Trivy.Path = filepath.Join(t.TempDir(), "removed", "trivy")

	srvCh := make(chan *server.Server, 1)
	serveHook = func(s *server.Server) { srvCh <- s }
	defer func() { serveHook = nil }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, &cfg) }()
	var srv *server.Server
	select {
	case srv = <-srvCh:
	case err := <-done:
		t.Fatalf("runServe exited before serving: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("runServe never built its server")
	}
	var body string
	deadline := time.Now().Add(10 * time.Second)
	for {
		if addr := srv.Addr(); addr != "" {
			if resp, err := http.Get("http://" + addr + "/metrics"); err == nil {
				raw, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				body = string(raw)
				if strings.Contains(body, "tobby_scan_scanner_available") {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the scan gauges never appeared:\n%s", body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, want := range []string{
		"tobby_scan_scanner_available 0", "tobby_scan_db_present 0", "tobby_scan_policy_blocking 0",
		`tobby_scan_items_total{verdict="not-scanned"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
	if strings.Contains(body, "tobby_scan_db_age_seconds") {
		t.Error("an absent database is given an age")
	}
	http.DefaultClient.CloseIdleConnections()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("runServe returned %v", err)
	}
}
