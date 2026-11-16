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

// Monitor keeps one verdict per anchor and tells its observers about every
// implausible one. It is the single object behind the startup log record,
// the UI banner, the metric, the audit event and the CLI warning (FR-096):
// they cannot disagree because they read one verdict.
//
// For the medium anchor it keeps the LATEST resolution time seen since the
// start, not the last medium opened: a date proven passed stays proven
// when an older medium — or one forged to carry no date — is opened next,
// and the clock is judged against the most demanding anchor it holds.
// Both anchors are judged again by Recheck (and Run, periodically): an
// instance started with a good clock that is later set back is caught,
// and a clock put right clears the banner by itself.
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
	return m.record(&Record{Verdict: Check(AnchorBuild, at, m.Now(), m.tolerance), Tolerance: m.tolerance}, true), true
}

// CheckMedium judges the local clock against the resolution time of a
// medium that is being opened (FR-054's resolvedAt), and returns the
// verdict for that medium. A zero resolvedAt proves nothing and changes
// nothing. The record kept — what the banner and the metric show — is
// the medium with the latest resolution time seen since the start, judged
// against the clock now: an earlier anchor never replaces a later one.
func (m *Monitor) CheckMedium(id string, resolvedAt time.Time) Record {
	now := m.Now()
	r := Record{Verdict: Check(AnchorMedium, resolvedAt, now, m.tolerance), Medium: id, Tolerance: m.tolerance}
	if resolvedAt.IsZero() {
		return r
	}
	m.mu.RLock()
	kept := m.medium
	m.mu.RUnlock()
	if kept != nil && kept.At.After(resolvedAt) {
		// The older medium is reported to its caller (the CLI warning of the
		// command that opened it) but does not displace the later anchor.
		m.record(&Record{Verdict: Check(AnchorMedium, kept.At, now, m.tolerance), Medium: kept.Medium, Tolerance: m.tolerance}, false)
		if r.Implausible {
			m.notify(&r)
		}
		return r
	}
	return m.record(&r, true)
}

// Recheck judges the clock again against the anchors already held — the
// build time and the latest medium — and notifies the observers of a
// verdict that has become implausible since the last check. It costs two
// comparisons.
func (m *Monitor) Recheck() {
	now := m.Now()
	m.mu.RLock()
	var again []Record
	for _, r := range []*Record{m.build, m.medium} {
		if r != nil {
			again = append(again, Record{Verdict: Check(r.Anchor, r.At, now, m.tolerance), Medium: r.Medium, Tolerance: m.tolerance})
		}
	}
	m.mu.RUnlock()
	for i := range again {
		m.record(&again[i], false)
	}
}

// Run calls Recheck every interval until ctx is done.
func (m *Monitor) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Recheck()
		}
	}
}

// record stores r as its anchor's verdict. With always, an implausible
// verdict is notified whatever came before (a check someone asked for);
// otherwise only one that was not implausible already (a re-judgment, so a
// standing condition is not re-announced every interval).
func (m *Monitor) record(r *Record, always bool) Record {
	m.mu.Lock()
	stored := *r
	prev := m.build
	if r.Anchor == AnchorMedium {
		prev = m.medium
		m.medium = &stored
	} else {
		m.build = &stored
	}
	m.mu.Unlock()
	if r.Implausible && (always || prev == nil || !prev.Implausible) {
		m.notify(r)
	}
	return *r
}

func (m *Monitor) notify(r *Record) {
	m.mu.RLock()
	observers := append([]func(*Record){}, m.observers...)
	m.mu.RUnlock()
	for _, fn := range observers {
		fn(r)
	}
}

// Records returns the verdict of each anchor that has been checked.
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
