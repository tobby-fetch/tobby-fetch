// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package diag

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
}

// Generate builds one bundle on behalf of actor, seen from origin. The
// request is audited whatever its outcome, and a refusal by the redaction
// check is audited as a failure and counted as "refused".
func (s *Service) Generate(ctx context.Context, actor, origin string) (*Bundle, error) {
	secrets, err := s.Secrets()
	var b *Bundle
	if err == nil {
		b, err = Build(Options{Secrets: secrets, Tasks: s.Tasks, Now: s.Now}, s.Sources)
	} else {
		err = taxonomy.New(taxonomy.CodeDiagnosticsFailed, taxonomy.Params{"detail": err.Error()}).WithCause(err)
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
