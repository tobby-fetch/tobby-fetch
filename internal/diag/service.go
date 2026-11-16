// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package diag

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Service produces bundles for the surfaces that ask for one — the
// command line on the host, the API, the screen — and records each
// request: the audit event naming who asked (FR-094) and the counter by
// outcome (FR-091). One service behind all three is what keeps their
// archives and their trail identical (FR-061).
type Service struct {
	// Secrets builds the set of secret values at the moment of the
	// request, so a rotated file or a changed environment is covered.
	Secrets func() (*Secrets, error)
	// Sources are the collectors.
	Sources *Sources
	// Tasks is diagnostics.tasks.
	Tasks int
	// Logger receives the audit event. Required.
	Logger *slog.Logger
	// Metrics counts the outcome; nil for a command that has no registry.
	Metrics *metrics.Registry
	// Now injects the clock under test.
	Now func() time.Time

	// busy admits one bundle at a time: each holds every member in memory
	// and scans it, and an administrator's repeated clicks must not
	// multiply that by the number of clicks.
	busy atomic.Bool
}

func (s *Service) build() (*Bundle, error) {
	secrets, err := s.Secrets()
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeDiagnosticsFailed, taxonomy.Params{"detail": err.Error()}).WithCause(err)
	}
	return Build(Options{Secrets: secrets, Tasks: s.Tasks, Now: s.Now}, s.Sources)
}

// Generate builds one bundle on behalf of actor, seen from origin. The
// request is audited whatever its outcome, and a refusal by the redaction
// check is audited as a failure and counted as "refused".
//
// One bundle is built at a time: a request that arrives while another is
// being built is refused with TBY-DIA-003 (HTTP 409), audited and counted
// as failed, rather than queued.
func (s *Service) Generate(ctx context.Context, actor, origin string) (*Bundle, error) {
	var b *Bundle
	var err error
	if s.busy.CompareAndSwap(false, true) {
		b, err = s.build()
		s.busy.Store(false)
	} else {
		err = taxonomy.New(taxonomy.CodeDiagnosticsBusy, nil)
	}
	outcome, count := audit.OutcomeSuccess, metrics.BundleWritten
	detail := ""
	if err != nil {
		outcome, count = audit.OutcomeFailure, metrics.BundleFailed
		var te *taxonomy.Error
		if errors.As(err, &te) {
			detail = string(te.Code())
			if te.Code() == taxonomy.CodeDiagnosticsRedaction {
				count = metrics.BundleRefused
				if p := te.ParamsMap(); p != nil {
					detail += " member=" + fmt.Sprint(p["member"])
				}
			}
		}
	}
	if s.Metrics != nil {
		s.Metrics.ObserveDiagnosticsBundle(count)
	}
	audit.Log(ctx, s.Logger, &audit.Event{
		Actor: actor, Action: audit.ActionDiagnosticsBundle, Target: "bundle",
		Outcome: outcome, Origin: origin, Detail: detail,
	})
	return b, err
}
