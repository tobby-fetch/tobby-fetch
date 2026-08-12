// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package tasks is Tobby's persistent task queue (roadmap 2.4): every
// operation is a tracked task with per-item status, its own log stream,
// and crash-safe resumption (FR-029). Tasks persist inside the store
// (FR-050) so a transported store carries its operation history.
//
// Failures persist as taxonomy code + parameters — never a localized
// sentence (ADR-0015 §4): history re-renders in the viewer's language long
// after the fact. The principal code of a multi-item task is computed by
// taxonomy.Principal, in one place.
package tasks

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Status of a task or of one of its items. The set is closed and versioned
// with the persistence schema: "done with errors" is a *rendering* of
// StatusFailed with partial success (items aggregates), not a stored state.
type Status string

// The task and item statuses.
const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
	// StatusSkipped marks an item already up to date: no transfer (FR-026).
	StatusSkipped Status = "skipped"
)

// TypeUnitImport is the one task type of milestone 2 (FR-023). Milestone 3
// adds recipe synchronization, milestone 5 the media operations.
const TypeUnitImport = "unit-import"

// Task is one tracked operation.
type Task struct {
	// ID identifies the task ("tsk_…"); part of URLs and logs.
	ID string `json:"id"`
	// RunID correlates the task's log records end to end (FR-090, R-09).
	RunID string `json:"run_id"`
	// Type is the operation type (TypeUnitImport…).
	Type string `json:"type"`
	// Reference is the subject of the operation (the requested reference).
	Reference string `json:"reference"`
	// Actor is the authenticated identity that triggered the task (FR-094).
	Actor string `json:"actor"`

	Status   Status    `json:"status"`
	Created  time.Time `json:"created"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	// Resumed marks a task re-queued after an instance interruption
	// (FR-029): visible in the list and on the detail header.
	Resumed bool `json:"resumed,omitempty"`

	// Error is the task-level failure (when the operation could not even
	// decompose into items — e.g. the inspection failed).
	Error *ItemError `json:"error,omitempty"`

	Items []Item `json:"items"`

	// ChartDependencies is the FR-024 report of a Helm chart import: the
	// declared dependencies and whether each is embedded in the package.
	// The container is the extensible "reports" zone of the task detail —
	// version resolution (milestone 3) and scan results (milestone 6) join
	// it without a schema break.
	ChartDependencies []ChartDependency `json:"chart_dependencies,omitempty"`
}

// ChartDependency is one row of the FR-024 report.
type ChartDependency struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Repository string `json:"repository,omitempty"`
	// Embedded reports whether the dependency archive travels inside the
	// chart package (charts/): required for offline deployability.
	Embedded bool `json:"embedded"`
}

// Item is one element of a task: a platform manifest, a chart, a config
// blob group — the unit of per-item status (roadmap 2.4).
type Item struct {
	// Name identifies the item within the task ("linux/amd64", "chart").
	Name string `json:"name"`
	// Digest is the item's pinned digest, when known.
	Digest string `json:"digest,omitempty"`
	// SizeBytes is the item's transferred size (raw bytes; the UI
	// localizes).
	SizeBytes int64 `json:"size_bytes,omitempty"`

	Status Status     `json:"status"`
	Error  *ItemError `json:"error,omitempty"`
}

// ItemError is a persisted failure: taxonomy code + typed parameters,
// re-rendered in the viewer's language at display time (ADR-0015 §4).
type ItemError struct {
	Code   taxonomy.Code  `json:"code"`
	Params map[string]any `json:"params,omitempty"`
}

// FromTaxonomy converts a taxonomy error for persistence.
func FromTaxonomy(e *taxonomy.Error) *ItemError {
	if e == nil {
		return nil
	}
	return &ItemError{Code: e.Code(), Params: e.ParamsMap()}
}

// Taxonomy rebuilds the renderable error.
func (ie *ItemError) Taxonomy(correlation string) *taxonomy.Error {
	if ie == nil {
		return nil
	}
	return taxonomy.New(ie.Code, taxonomy.Params(ie.Params)).WithCorrelation(correlation)
}

// Aggregates are the per-task item counters, computed at read time — the
// persisted schema stays untouched (UI-SPEC: "terminée avec erreurs" is
// items_failed > 0 on a finished task).
type Aggregates struct {
	Total, Done, Failed, Skipped int
}

// Aggregate counts t's items by outcome.
func (t *Task) Aggregate() Aggregates {
	var a Aggregates
	a.Total = len(t.Items)
	for _, it := range t.Items {
		switch it.Status {
		case StatusDone:
			a.Done++
		case StatusFailed:
			a.Failed++
		case StatusSkipped:
			a.Skipped++
		case StatusPending, StatusRunning:
		}
	}
	return a
}

// Principal returns the task's principal error for lists and reports —
// the single aggregation rule of the taxonomy package.
func (t *Task) Principal() *taxonomy.Error {
	var errs []*taxonomy.Error
	if t.Error != nil {
		errs = append(errs, t.Error.Taxonomy(t.RunID))
	}
	for _, it := range t.Items {
		if it.Error != nil {
			errs = append(errs, it.Error.Taxonomy(t.RunID))
		}
	}
	return taxonomy.Principal(errs)
}

// Active reports whether the task still moves (drives the UI polling:
// the server re-emits the polling attribute only while something is
// active).
func (t *Task) Active() bool {
	return t.Status == StatusPending || t.Status == StatusRunning
}

// newID mints a short random identifier with the given prefix.
func newID(prefix string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("tasks: reading random bytes: " + err.Error())
	}
	return prefix + hex.EncodeToString(b[:])
}
