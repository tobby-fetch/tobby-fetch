// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package clock

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/buildinfo"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Record is the latest verdict for one anchor, with what names it.
type Record struct {
	Verdict
	// Medium is the identifier of the medium the verdict concerns; empty
	// for the build anchor.
	Medium string `json:"medium,omitempty"`
	// Tolerance is the bound the verdict was judged against.
	Tolerance time.Duration `json:"tolerance"`
}

// Error renders an implausible record as its taxonomy code (TBY-CLK-001
// for the build, TBY-CLK-002 for a medium); nil for a plausible one.
func (r *Record) Error() *taxonomy.Error {
	if !r.Implausible {
		return nil
	}
	params := taxonomy.Params{
		"now":    r.Now.UTC().Format(time.RFC3339),
		"anchor": r.At.UTC().Format(time.RFC3339),
		"gap":    r.Gap.Round(time.Second).String(),
	}
	if r.Anchor == AnchorMedium {
		params["medium"] = r.Medium
		return taxonomy.New(taxonomy.CodeClockBeforeMedium, params)
	}
	return taxonomy.New(taxonomy.CodeClockBeforeBuild, params)
}

// Target is what the audit event names: "build", or the medium.
func (r *Record) Target() string {
	if r.Anchor == AnchorMedium {
		return r.Medium
	}
	return string(AnchorBuild)
}

// Monitor keeps the latest verdict per anchor and tells its observers
// about every implausible one. It is the single object behind the startup
// log record, the UI banner, the metric, the audit event and the CLI
// warning (FR-096): they cannot disagree because they read one verdict.
//
// It warns and records. It never touches the clock and it changes no
// verdict of the instance by itself.
type Monitor struct {
	// Now injects the clock under test.
	Now func() time.Time

	tolerance time.Duration

	mu        sync.RWMutex
	build     *Record
	medium    *Record
	observers []func(*Record)
}

// NewMonitor returns a monitor judging against tolerance (clock.skewTolerance).
func NewMonitor(tolerance time.Duration) *Monitor {
	return &Monitor{tolerance: tolerance, Now: time.Now}
}

// Observe registers fn to be called, synchronously, with every implausible
// verdict as it is reached.
func (m *Monitor) Observe(fn func(*Record)) {
	m.mu.Lock()
	m.observers = append(m.observers, fn)
	m.mu.Unlock()
}

// CheckBuild judges the local clock against the build time of this binary.
// A build that carries none (a development build) proves nothing and
// records nothing: the metric stays absent rather than claiming a
// plausibility nobody measured.
func (m *Monitor) CheckBuild() (Record, bool) {
	at, ok := buildinfo.BuildTime()
	if !ok {
		return Record{}, false
	}
	return m.record(&Record{Verdict: Check(AnchorBuild, at, m.Now(), m.tolerance), Tolerance: m.tolerance}), true
}

// CheckMedium judges the local clock against the resolution time of a
// medium that is being opened (FR-054's resolvedAt).
func (m *Monitor) CheckMedium(id string, resolvedAt time.Time) Record {
	return m.record(&Record{
		Verdict: Check(AnchorMedium, resolvedAt, m.Now(), m.tolerance), Medium: id, Tolerance: m.tolerance,
	})
}

func (m *Monitor) record(r *Record) Record {
	m.mu.Lock()
	stored := *r
	if r.Anchor == AnchorMedium {
		m.medium = &stored
	} else {
		m.build = &stored
	}
	observers := append([]func(*Record){}, m.observers...)
	m.mu.Unlock()
	if r.Implausible {
		for _, fn := range observers {
			fn(r)
		}
	}
	return *r
}

// Records returns the latest verdict of each anchor that has been checked.
func (m *Monitor) Records() []Record {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Record
	if m.build != nil {
		out = append(out, *m.build)
	}
	if m.medium != nil {
		out = append(out, *m.medium)
	}
	return out
}

// Implausible returns the records currently found implausible: what the
// banner shows.
func (m *Monitor) Implausible() []Record {
	var out []Record
	for _, r := range m.Records() {
		if r.Implausible {
			out = append(out, r)
		}
	}
	return out
}

// Report writes the warning log record and the audit event of one
// implausible verdict (FR-096): the code, the gap, the anchor, and for a
// medium its identifier. Never a secret — only dates.
func Report(ctx context.Context, logger *slog.Logger, r *Record) {
	te := r.Error()
	if te == nil {
		return
	}
	logger.LogAttrs(ctx, slog.LevelWarn, "local clock is earlier than a date this instance proves has passed",
		slog.String("code", string(te.Code())),
		slog.String("anchor", string(r.Anchor)),
		slog.String("medium", r.Medium),
		slog.Time("now", r.Now.UTC()),
		slog.Time("anchor_time", r.At.UTC()),
		slog.Duration("gap", r.Gap.Round(time.Second)),
		slog.Duration("tolerance", r.Tolerance),
		slog.String("action", "set the host's clock from a trusted reference; Tobby warns and records, it does not correct the clock"),
		slog.String("requirement", "FR-096"))
	audit.Log(ctx, logger, &audit.Event{
		Actor:   audit.ActorLocal,
		Action:  audit.ActionClockImplausible,
		Target:  r.Target(),
		Outcome: audit.OutcomeSuccess,
		Origin:  audit.OriginLocal,
		Detail:  "gap=" + r.Gap.Round(time.Second).String() + " tolerance=" + r.Tolerance.String() + " code=" + string(te.Code()),
	})
}
