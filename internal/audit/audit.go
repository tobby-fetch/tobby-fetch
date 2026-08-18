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

// Actions recorded so far. The catalog grows with the features that ship
// (sensitive configuration changes, audited overrides — FR-046, FR-054).
const (
	ActionInstanceStart = "instance.start"
	ActionInstanceStop  = "instance.stop"

	// Account lifecycle (R-01: local CLI, actor "local"; R-34: the
	// self-service password change carries the authenticated identity;
	// milestone 4 feature 4.3: the admin surfaces carry the acting
	// administrator as actor and the managed account as target).
	ActionAccountCreate = "account.create"
	ActionAccountPasswd = "account.password_change"
	ActionAccountDelete = "account.delete"
	ActionAccountRole   = "account.role_change"

	// ActionAuthenticate is one credential verification on a machine
	// surface — the /v2/ registry or /api/v1 (FR-094, FR-076). The target
	// names the surface, not the path: authenticating is not the same
	// event as reaching a resource, and the authorization verdict has its
	// own action below. Emission is deliberately event-shaped rather than
	// request-shaped; see auth.Authenticator for the rule.
	ActionAuthenticate = "auth.authenticate"

	// Authorization verdicts, per surface. These fire on refusal only: a
	// permitted request is the traffic the operational log already carries
	// (FR-090), and duplicating it here would bury the refusals that make
	// the trail worth reading.
	ActionRegistryAccess = "registry.access"
	ActionAPIAccess      = "api.access"
	ActionUIAccess       = "ui.access"

	// Static token lifecycle (FR-072).
	ActionTokenCreate = "token.create"
	ActionTokenRevoke = "token.revoke"

	// UI sessions (interactive sign-in and sign-out).
	ActionLogin  = "session.login"
	ActionLogout = "session.logout"
)

// Surfaces named as the target of authentication events. A surface, not a
// path: "which door was tried" is the security question; "which resource"
// is answered by the authorization actions and by the operational log.
const (
	SurfaceUI       = "ui"
	SurfaceAPI      = "api"
	SurfaceRegistry = "registry"
)

const (
	// ActionAuthOverride is emitted at startup while authentication is
	// disabled by the explicit FR-075 opt-out — the trail's counterpart of
	// the permanent UI banner.
	ActionAuthOverride = "auth.override_active"

	// ActionImportCreate is a unit-import task creation (FR-023): the actor
	// is the authenticated identity, the target the requested reference.
	ActionImportCreate = "import.create"

	// ActionSyncCreate is a synchronization trigger (FR-014): the actor is
	// the authenticated identity, the target the configured Retriever source.
	ActionSyncCreate = "sync.create"

	// ActionContentDelete is the admin removal of one unit-imported
	// repository (FR-044 amendment): the target is the repository path.
	ActionContentDelete = "content.delete"

	// ActionIntervalChange is a change to the promotion cadence of
	// FR-013 — the sensitive configuration change FR-094 asks to be
	// recorded. How often an instance reaches into a neighbouring zone is
	// a security-relevant setting, and unlike everything else in the
	// configuration it can be changed on a running instance by anyone with
	// the admin role: the trail is the only place that records who did.
	// The target is the resulting effective interval.
	ActionIntervalChange = "config.promotion_interval"

	// ActionRecipePublish is a recipe published into a cookbook from the
	// interface or the API (R-40, FR-036): outbound writing, which is
	// exactly what FR-094 asks to be recorded. The target is the
	// published reference — a refused publication carries the reference
	// that was attempted, which is the half of the trail that answers
	// "who tried to write into that cookbook".
	ActionRecipePublish = "recipe.publish"

	// ActionServerCertReplace is a replacement of the listener's own
	// certificate from the administration surfaces (FR-082). The
	// sensitive configuration change FR-094 names: it decides what every
	// client of this instance authenticates against, and it can be made
	// on a running instance by anyone holding the admin role. The target
	// is the fingerprint now served — never anything derived from the
	// private key (NFR-015).
	ActionServerCertReplace = "config.server_certificate"
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
