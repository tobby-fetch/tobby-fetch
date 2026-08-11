// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package audit emits the security audit log (FR-094).
//
// Audit events are ordinary structured log records on the FR-090 channels,
// distinguishable from operational logs by the stable marker field
// "log_type": "audit". Every event carries the same six-field schema —
// actor, action, target, outcome, timestamp (slog's "ts"), origin — so a
// single filter separates the security trail from the rest, and the trail
// stays parseable without regexes. The schema is versioned through
// "audit_schema" and evolves additively only.
//
// The audit log is operational evidence, not a trust anchor: Tobby signs
// nothing (ADR-0006, ADR-0007).
package audit

import (
	"context"
	"log/slog"
)

// SchemaVersion is stamped on every record as "audit_schema". Bump only on
// additive change; the schema is stable across a major series (FR-094).
const SchemaVersion = 1

// Marker field: every audit record carries "log_type": "audit".
const (
	KeyLogType = "log_type"
	LogType    = "audit"
)

// Outcome values. The set is closed: success, failure (attempted and did not
// work), denied (blocked by policy or authorization).
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
	OutcomeDenied  = "denied"
)

// ActorLocal identifies actions performed by the process itself or through
// local, non-network invocation (CLI on the host, startup, shutdown).
const ActorLocal = "local"

// OriginLocal marks events that did not come from a network client.
const OriginLocal = "local"

// Actions recorded at this milestone. The catalog grows with the features
// that ship (authentication, token lifecycle, sensitive configuration
// changes, audited overrides — FR-046, FR-054, FR-075).
const (
	ActionInstanceStart = "instance.start"
	ActionInstanceStop  = "instance.stop"
)

// Event is one security audit event (FR-094 schema).
type Event struct {
	// Actor is the authenticated identity, ActorLocal for local invocation,
	// or the explicit unauthenticated context under the FR-075 override.
	Actor string
	// Action names what was attempted, from the package's action catalog.
	Action string
	// Target is what the action applied to (instance, account, token,
	// configuration entry, media…). May be empty when the action is global.
	Target string
	// Outcome is one of OutcomeSuccess, OutcomeFailure, OutcomeDenied.
	Outcome string
	// Origin is the client network address, or OriginLocal.
	Origin string
}

// Log emits e on logger at INFO level. The timestamp is supplied by the
// logging layer ("ts"); all six schema fields plus the marker and schema
// version are present on every record.
func Log(ctx context.Context, logger *slog.Logger, e *Event) {
	logger.LogAttrs(ctx, slog.LevelInfo, e.Action,
		slog.String(KeyLogType, LogType),
		slog.Int("audit_schema", SchemaVersion),
		slog.String("actor", e.Actor),
		slog.String("action", e.Action),
		slog.String("target", e.Target),
		slog.String("outcome", e.Outcome),
		slog.String("origin", e.Origin),
	)
}
