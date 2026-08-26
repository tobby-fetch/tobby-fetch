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

// The task types: unit import (FR-023, milestone 2), recipe
// synchronization (FR-014, milestone 3), and the OCI image layout
// export/import of the removable-media work (FR-051, milestone 5).
const (
	TypeUnitImport   = "unit-import"
	TypeSync         = "sync"
	TypeLayoutExport = "layout-export"
	TypeLayoutImport = "layout-import"
)

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

	// VendorDependencies is the FR-025 per-operation opt-in: declared
	// chart dependencies missing from the package are fetched and embedded
	// before the artifact lands, breaking the upstream digest — always
	// traced (manifest annotations, report, logs), never the default.
	VendorDependencies bool `json:"vendor_dependencies,omitempty"`

	// Layout carries the parameters of an OCI image layout operation
	// (FR-051). They are persisted rather than held by the caller because
	// FR-029 re-runs an interrupted task from the file alone: an export
	// whose format and selection lived only in the request that started
	// it would resume as a different export.
	Layout *Layout `json:"layout,omitempty"`

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

	// Prune is what THIS run was asked to do about content the resolved
	// Retriever no longer references (FR-045). It lives on the task
	// rather than on the engine because the answer is per-run: mirror
	// mode confirms it at trigger time with the list and the total size,
	// passthrough reads it from sync.prune — and a resumed task must
	// carry the decision that was confirmed, not the one the instance
	// happens to default to when it comes back up.
	Prune bool `json:"prune,omitempty"`

	// Pruned is the FR-045 removal report of a synchronization: the
	// recipe-managed content the resolved Retriever no longer references,
	// removed at the end of the cycle when sync.prune is on. Empty on the
	// default instance, which prunes nothing.
	Pruned []Pruned `json:"pruned,omitempty"`

	// Resolutions is the FR-021 resolution report of a synchronization:
	// requested → resolved → digest, per recipe entry and per ingredient,
	// with the FR-026 status and the FR-036 effective endpoint when a
	// substitution applied.
	Resolutions []Resolution `json:"resolutions,omitempty"`
}

// Layout is the parameter set of an OCI image layout export or import
// (FR-051). Written once at creation and read-only afterwards: the
// runner never mutates it, which is why the task clone shares it.
type Layout struct {
	// Format is the shape of an export: "tar" or "directory".
	Format string `json:"format,omitempty"`
	// Recipes and Repositories narrow an export; both empty exports the
	// whole store.
	Recipes      []string `json:"recipes,omitempty"`
	Repositories []string `json:"repositories,omitempty"`
	// Repository places, on import, the entries a third-party layout
	// names by tag alone.
	Repository string `json:"repository,omitempty"`
	// Overwrite allows an export to replace an existing destination.
	Overwrite bool `json:"overwrite,omitempty"`
}

// Pruned is one row of the FR-045 removal report: what went, and which
// recipe used to hold it. The digest is carried because a tag is not an
// identity — after the medium has travelled, "which bytes left" is the
// question the run log has to be able to answer (FR-053).
type Pruned struct {
	// Repo is the store repository path the tag belonged to.
	Repo string `json:"repo"`
	// Tag is the removed tag.
	Tag string `json:"tag"`
	// Digest pins what the tag resolved to.
	Digest string `json:"digest,omitempty"`
	// Recipe is the "name@version" of the recipe that brought it and that
	// the current Retriever no longer references.
	Recipe string `json:"recipe,omitempty"`
}

// Resolution is one row of the FR-021 report.
type Resolution struct {
	// Recipe is "name@version" once resolved ("wordpress@6.8.2").
	Recipe string `json:"recipe"`
	// Ingredient is empty on the recipe entry's own row.
	Ingredient string `json:"ingredient,omitempty"`
	// Requested is the expression as written (exact tag or constraint).
	Requested string `json:"requested"`
	// Resolved is the concrete tag; Digest the pinned digest.
	Resolved string `json:"resolved"`
	Digest   string `json:"digest,omitempty"`
	// Status is the FR-026 per-digest status: new, outdated, up-to-date.
	Status string `json:"status,omitempty"`
	// Effective is the substituted endpoint actually contacted, when it
	// differs from the nominal reference (FR-036 — both are logged).
	Effective string `json:"effective,omitempty"`
	// TrustScope names the declared scope that admitted the item when the
	// strict default was relaxed (FR-033: never silent).
	TrustScope string `json:"trust_scope,omitempty"`
	// Destination is the fully qualified reference the item was promoted
	// to (FR-035 source→destination mapping, FR-065). Empty on an
	// instance that promotes nothing.
	Destination string `json:"destination,omitempty"`
	// DestinationStatus is the FR-026 per-digest status computed against
	// the DESTINATION registry before any push. It is deliberately a
	// second field rather than a reuse of Status: one says what the local
	// store was missing, the other what the next zone was missing, and on
	// a promotion service they routinely disagree — content already local
	// and not yet pushed is precisely the state the operator is looking
	// for.
	DestinationStatus string `json:"destination_status,omitempty"`
	// PushedBytes is what actually crossed for this item. Zero on an
	// up-to-date item is the FR-028 acceptance criterion, stated per row.
	PushedBytes int64 `json:"pushed_bytes,omitempty"`
}

// ChartDependency is one row of the FR-024 report.
type ChartDependency struct {
	// Chart names the chart carrying the dependency — a recipe
	// synchronization can verify several charts in one task.
	Chart      string `json:"chart,omitempty"`
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

	// Blobs is the per-blob progress of the item's large transfers
	// (FR-029, R-29). Only blobs that took the resumable path appear
	// here — the ones big enough that "running" is not an answer an
	// operator can act on. A 6 GB layer that moved 5.4 GB and got cut
	// shows as exactly that, and shows that the next attempt picked up
	// where it stopped instead of at zero.
	//
	// Deliberately bounded: small blobs are not tracked, so the persisted
	// task stays a few lines rather than one entry per layer per
	// platform.
	Blobs []BlobProgress `json:"blobs,omitempty"`
}

// cloneItems deep-copies a task's items, per-blob progress rows
// included (B-016). Item carries a Blobs slice the runner keeps
// mutating through TrackBlob; a shallow item copy would share those
// rows' backing array between the runner's working clone and the
// published task, and every reader of the published side — persist's
// marshalling, Get, List — would race with the runner's next progress
// tick. Error pointers are shared deliberately: persisted errors are
// never mutated in place (see Task.clone).
func cloneItems(items []Item) []Item {
	if items == nil {
		return nil
	}
	out := append([]Item(nil), items...)
	for i := range out {
		out[i].Blobs = append([]BlobProgress(nil), out[i].Blobs...)
	}
	return out
}

// BlobProgress is one large blob's advance within an item (FR-029).
type BlobProgress struct {
	// Digest is the blob's pinned digest.
	Digest string `json:"digest"`
	// SizeBytes is what the manifest declares for it.
	SizeBytes int64 `json:"size_bytes,omitempty"`
	// ReceivedBytes is what has durably landed in the partial-download
	// area of the state directory. It survives an instance restart,
	// because the bytes it counts do.
	ReceivedBytes int64 `json:"received_bytes,omitempty"`
	// Resumed marks a blob whose transfer picked up bytes an earlier
	// attempt had already received — the point of the whole mechanism,
	// and the thing an operator watching a stalled link wants confirmed.
	Resumed bool `json:"resumed,omitempty"`
	// Done marks a blob fully received and verified against its digest.
	Done bool `json:"done,omitempty"`
}

// TrackBlob records or updates one blob's progress on an item, keyed by
// digest so a resumed transfer updates its own row instead of appending a
// second one.
func (it *Item) TrackBlob(dgst string, received, size int64, resumed, done bool) {
	for i := range it.Blobs {
		if it.Blobs[i].Digest != dgst {
			continue
		}
		b := &it.Blobs[i]
		b.ReceivedBytes, b.Done = received, done
		if size > 0 {
			b.SizeBytes = size
		}
		// Once true, it stays true: the row records that this blob was
		// resumed at some point, not what the last chunk did.
		b.Resumed = b.Resumed || resumed
		return
	}
	it.Blobs = append(it.Blobs, BlobProgress{
		Digest: dgst, SizeBytes: size, ReceivedBytes: received, Resumed: resumed, Done: done,
	})
}

// TrackBlobDone closes one tracked blob's row once it has landed in the
// store, verified. Blobs that never took the resumable path are not
// tracked and are silently ignored here: the row exists to explain a long
// transfer, not to inventory every layer.
func (it *Item) TrackBlobDone(dgst string) {
	for i := range it.Blobs {
		if it.Blobs[i].Digest == dgst {
			it.Blobs[i].Done = true
			if it.Blobs[i].SizeBytes > 0 {
				it.Blobs[i].ReceivedBytes = it.Blobs[i].SizeBytes
			}
			return
		}
	}
}

// ItemError is a persisted failure: taxonomy code + typed parameters,
// re-rendered in the viewer's language at display time (ADR-0015 §4).
type ItemError struct {
	Code   taxonomy.Code  `json:"code"`
	Params map[string]any `json:"params,omitempty"`
	// Detail is the underlying technical cause, verbatim (B-021).
	//
	// It is a field of its OWN, beside the code and never in place of
	// it: the code is the stable contract (R-03) and the taxonomy stays
	// untouched, while the detail is the one thing the taxonomy cannot
	// carry — what the source registry, the parser or the filesystem
	// actually said. Without it a TBY-SRV-001 item states that an
	// internal error occurred and stops there, which is precisely the
	// dead end B-021 reported.
	//
	// Deliberately NOT localized: it is the wrapped Go error, the same
	// string the FR-090 log record carries, so the two can be matched
	// character for character. Never a rendered sentence — the
	// localized what/cause/action still come from the catalog at
	// display time (ADR-0015 §4).
	Detail string `json:"detail,omitempty"`
}

// FromTaxonomy converts a taxonomy error for persistence, wrapped cause
// included (B-021). The taxonomy keeps that cause for logs only; dropping
// it here was what left a failed item carrying a code an operator could
// read but not act on.
func FromTaxonomy(e *taxonomy.Error) *ItemError {
	if e == nil {
		return nil
	}
	ie := &ItemError{Code: e.Code(), Params: e.ParamsMap()}
	if cause := e.Unwrap(); cause != nil {
		ie.Detail = cause.Error()
	}
	return ie
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
