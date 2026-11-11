// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/scan/trivy"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/ui"
)

// scanning is the instance's vulnerability scanning (FR-031, FR-032,
// R-13): the Trivy adapter over the store, wired into the engine, the
// metrics and the interface.
type scanning struct {
	scanner *trivy.Scanner
	// workDir is the scanner's private directory; tempWorkDir says this
	// process created it under the temporary directory, to remove at
	// close.
	workDir     string
	tempWorkDir bool
	eng         *engine.Engine
	maxAge      time.Duration
	logger      *slog.Logger
}

// setupScanning builds the scanner, locates its binary and database, and
// announces the posture at startup on the log — the degraded mode and the
// advisory policy are both stated, never silent (FR-075).
func setupScanning(ctx context.Context, cfg *config.Config, st *store.Store, eng *engine.Engine, reg *metrics.Registry, logger *slog.Logger) *scanning {
	// The scanner's private directory — the unpacked database, a
	// channel-delivered binary, one scratch copy per scan — lives in the
	// state directory, never in the transportable store (R-16).
	//
	// Without one (auth.disabled), the directory is created fresh, 0700,
	// under a name nobody can predict — never a fixed path under the
	// shared temporary directory, where another local user could prepare
	// it first with a database of their own (B-046). The adapter refuses
	// a work directory it does not own anyway.
	workDir, temp := "", false
	if cfg.State.Root != "" {
		workDir = filepath.Join(cfg.State.Root, "scanner")
	} else if d, err := os.MkdirTemp("", "tobby-scanner-"); err == nil {
		workDir, temp = d, true
	} else {
		logger.LogAttrs(ctx, slog.LevelError, "the scanner work directory cannot be created",
			slog.String("error", err.Error()), slog.String("requirement", "FR-031"))
	}
	s := &scanning{
		workDir: workDir, tempWorkDir: temp,
		scanner: trivy.New(&trivy.Options{
			Store: st, WorkDir: workDir,
			Path: cfg.Scan.Trivy.Path, Digest: cfg.Scan.Trivy.Digest, Ref: cfg.Scan.Trivy.Ref,
			DBRepository: cfg.Scan.DBRepository, BasePrefix: cfg.Storage.BasePrefix,
			Timeout: time.Duration(cfg.Scan.Timeout), Logger: logger,
		}),
		eng:    eng,
		maxAge: time.Duration(cfg.Scan.MaxDBAge),
		logger: logger,
	}
	settings := engine.ScanSettingsFrom(&cfg.Scan, s.scanner)
	settings.Observe = func(r *scan.Report) {
		reg.Scan.Items.WithLabelValues(string(r.Policy.Verdict)).Inc()
		for sev, n := range map[string]int{
			"CRITICAL": r.Counts.Critical, "HIGH": r.Counts.High, "MEDIUM": r.Counts.Medium,
			"LOW": r.Counts.Low, "UNKNOWN": r.Counts.Unknown,
		} {
			if n > 0 {
				reg.Scan.Findings.WithLabelValues(sev).Add(float64(n))
			}
		}
	}
	settings.Refresh = func(ctx context.Context) { s.refresh(ctx) }
	eng.SetScan(&settings)
	reg.Scan.SetState(s.metricState)

	s.refresh(ctx)
	if eng.ScanPolicyBlocking() {
		logger.LogAttrs(ctx, slog.LevelInfo, "vulnerability policy is blocking",
			slog.String("threshold", scan.FormatScore(cfg.Scan.Threshold)),
			slog.Int("exceptions", len(cfg.Scan.Exceptions)),
			slog.String("requirement", "FR-031"))
	} else {
		logger.LogAttrs(ctx, slog.LevelWarn, "vulnerability policy is advisory",
			slog.String("setting", "scan.mode"),
			slog.String("effect", "every image is scanned and every finding reported; a violation is pushed anyway — set scan.mode: block to refuse it, or pose a threshold in the Retriever's spec.scanPolicy"),
			slog.String("threshold", scan.FormatScore(cfg.Scan.Threshold)),
			slog.String("requirement", "FR-031/FR-075"))
	}
	if cfg.Scan.MaxDBAgeAction == config.ScanModeBlock {
		logger.LogAttrs(ctx, slog.LevelInfo, "a stale vulnerability database refuses pushes",
			slog.String("max_db_age", cfg.Scan.MaxDBAge.String()), slog.String("requirement", "R-13"))
	}
	return s
}

// close removes the work directory this process created for itself.
func (s *scanning) close() {
	if s.tempWorkDir {
		os.RemoveAll(s.workDir) //nolint:errcheck,gosec // best-effort cleanup of a private directory
	}
}

// refresh re-reads the scanner's deliveries and logs a degraded mode.
func (s *scanning) refresh(ctx context.Context) {
	st := s.scanner.Refresh(ctx)
	if cause := s.scanner.Cause(); cause != nil {
		s.logger.LogAttrs(ctx, slog.LevelError, "vulnerability scanning degraded: content is reported as not scanned",
			slog.String("code", string(cause.Code())),
			slog.String("error", cause.Error()),
			slog.Bool("scanner_available", st.Available),
			slog.Bool("database_present", st.Database != nil),
			slog.String("effect", "under a blocking policy nothing that could not be scanned is pushed"),
			slog.String("requirement", "FR-031/FR-032"))
		return
	}
	s.logger.LogAttrs(ctx, slog.LevelInfo, "vulnerability scanning ready",
		slog.String("scanner", st.Scanner.Version),
		slog.String("database", st.Database.Digest),
		slog.Time("database_built_at", st.Database.BuiltAt),
		slog.String("database_age", scan.FormatAge(time.Since(st.Database.BuiltAt))),
		slog.Bool("database_stale", time.Since(st.Database.BuiltAt) > s.maxAge || scan.FromFuture(st.Database.BuiltAt, time.Now())),
		slog.String("requirement", "FR-032/R-13"))
}

func (s *scanning) metricState() metrics.ScanState {
	st := s.scanner.Status(context.Background())
	m := metrics.ScanState{
		PolicyBlocking:   s.eng.ScanPolicyBlocking(),
		ScannerAvailable: st.Available,
		DBPresent:        st.Database != nil,
	}
	if st.Database != nil {
		age := time.Since(st.Database.BuiltAt)
		m.DBAgeSeconds = age.Seconds()
		m.DBStale = age > s.maxAge || scan.FromFuture(st.Database.BuiltAt, time.Now())
	}
	return m
}

// uiState is what the interface reads on every page.
func (s *scanning) uiState() ui.ScanState {
	st := s.scanner.Status(context.Background())
	origin, _ := s.scanner.Origin()
	return ui.ScanState{
		Blocking: s.eng.ScanPolicyBlocking(),
		Cause:    s.scanner.Cause(),
		Scanner:  st.Scanner,
		Origin:   origin,
		Database: st.Database,
		MaxAge:   s.maxAge,
	}
}
