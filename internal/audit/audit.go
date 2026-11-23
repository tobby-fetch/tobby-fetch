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
	"strings"
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
	// ActionFileSetPack is the packing of a local file tree into a
	// FileSet imported in the store (FR-048): the target is the packed
	// reference, version included. Recorded from every surface — the
	// content is unsigned and of local origin, so the trail is the only
	// record of who put it there.
	ActionFileSetPack = "fileset.pack"

	// ActionPruneActive is emitted at startup while the passthrough prune
	// is enabled by the explicit R-33 opt-in (FR-045 amendment) — the
	// trail's counterpart of the retriever screen's own statement. An
	// instance that deletes content on a timer must be answerable for
	// when it was told to.
	ActionPruneActive = "prune.active"

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

	// ActionStoreReset is the full store reset of FR-046 — the single
	// most destructive action the product offers, and the one the
	// requirement names explicitly among the audited events. The target
	// is the storage root that was emptied; a refused confirmation is
	// recorded too, as "denied": somebody typing the wrong word into that
	// field is the trail's early warning.
	ActionStoreReset = "store.reset"

	// ActionLayoutExport and ActionLayoutImport are the OCI image layout
	// operations of FR-051. Recorded because they cross the instance's
	// boundary in both directions: an export writes content of this store
	// to a path on the host — the interoperability escape hatch is also
	// the way content leaves — and an import brings outside bytes in. The
	// target is the medium's path.
	ActionLayoutExport = "layout.export"
	// ActionLayoutImport is the inbound half of ActionLayoutExport: a
	// medium's content entering this store.
	ActionLayoutImport = "layout.import"
	// ActionMediaImport is a destination-side media import (FR-052): the
	// actor is the authenticated identity or the local invocation, the
	// target the transported store. Recorded because it is the moment
	// content from outside the zone enters the zone's registry, which is
	// the event FR-094 exists for.
	ActionMediaImport = "media.import"

	// ActionMediaOverride is an administrator waiving one FR-054 guard on
	// a media verification or a media import — the zone-identity refusal
	// or the R-28 freshness refusal. Both requirements name the audit
	// record explicitly, and it carries the guard as its target so the
	// trail says WHICH barrier was lowered and not merely that one was.
	// A success record is written each time a waiver is applied to an
	// operation — a waived verification can open the serving gate as
	// surely as a waived import pushes — and its detail names that
	// operation (MediaOverrideDetail), so a Verify followed by an import
	// under the same waiver reads as the two applications it is.
	ActionMediaOverride = "media.override"

	// ActionServerCertReplace is a replacement of the listener's own
	// certificate from the administration surfaces (FR-082). The
	// sensitive configuration change FR-094 names: it decides what every
	// client of this instance authenticates against, and it can be made
	// on a running instance by anyone holding the admin role. The target
	// is the fingerprint now served — never anything derived from the
	// private key (NFR-015).
	ActionServerCertReplace = "config.server_certificate"
)

// Actions reserved at the opening of milestone 6. No surface emits them
// yet; each track wires its own and documents it on the audit-log page
// (website security/audit-log) when it does.
const (
	// ActionTokenExpire is a static token refused because it expired
	// (FR-072 amendment, R-14) — the end of its lifecycle, recorded like
	// its creation and revocation. Track B.
	ActionTokenExpire = "token.expire"
	// ActionSessionExpire is a UI session ended by its idle timeout or its
	// absolute lifetime; the reason is the target (R-14). Track B.
	ActionSessionExpire = "session.expire"
	// ActionLocalUnderFederation is a sign-in with a LOCAL account while a
	// federated method is enabled (FR-078, R-20): break-glass use, kept
	// filterable on its own. Track B.
	ActionLocalUnderFederation = "auth.local_under_federation"
	// ActionScanBlock is an item refused by the vulnerability policy
	// (FR-031) — the scan events R-12 schedules for milestone 6. Track A.
	ActionScanBlock = "scan.block"
	// ActionScanScannerRepaired is a channel-delivered scanner binary that
	// a refresh found changed on disk since it was verified, and unpacked
	// again from the verified delivery without running it (FR-031, B-045):
	// the target is the binary's path; the outcome success when the
	// delivery was unpacked again, failure when it could not be (the
	// scanner is then unavailable). Track A.
	ActionScanScannerRepaired = "scan.scanner.repaired"
	// ActionIntegrityVerify is an on-demand store verification (FR-057,
	// R-31): who asked, on which store, with which verdict. Track C.
	//
	// Emitted — unlike the rest of this block — when a verification
	// FINISHES, by the task runner (check.Service) and by `tobby store
	// verify`: the verdict is the outcome (success for a clean store,
	// failure for defects), the requester is the actor, and the origin is
	// the one recorded on the task when the request arrived.
	ActionIntegrityVerify = "store.verify"
)

// Actions reserved at the opening of milestone 7. No surface emits them
// yet; each track wires its own and documents it on the audit-log page
// (website security/audit-log) when it does.
const (
	// ActionDiagnosticsBundle is a diagnostic bundle produced or refused
	// (FR-095, R-30): the requester is the actor, the outcome failure when
	// the redaction check aborted it. Track B.
	ActionDiagnosticsBundle = "diagnostics.bundle"
	// ActionClockImplausible is a local clock found earlier than an anchor
	// beyond the tolerance (FR-096, R-32); the target names the anchor
	// ("build" or the medium identifier), the gap is in the details.
	// Track B.
	ActionClockImplausible = "clock.implausible"
	// ActionScanExceptionExpired is an item refused again because the
	// exception that admitted it expired (FR-031 amendment, R-15): the
	// target is the item ("repository@digest"), the outcome denied, and
	// the exception is named in the exception, exception_source and
	// exception_expires fields — one record per expired exception that
	// matched the refused item. Emitted by the engine beside the
	// scan.block record of the same refusal.
	ActionScanExceptionExpired = "scan.exception_expired"
	// ActionHardeningRefused is a write refused by a hardening switch
	// (FR-098, R-17): a unit import, a packing or a registry push, with
	// the setting that refused it. Track C.
	ActionHardeningRefused = "hardening.refused"
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
	// ActorName is an optional readable name for Actor, when Actor is an
	// opaque key — a federated identity's OIDC subject or SAML NameID
	// (milestone 6, track B). Display only, never an identity: filter on
	// Actor. Emitted as "actor_name" only when set (additive to the
	// schema).
	ActorName string
	// Detail is optional free context that is not the target — the gap of
	// a clock found implausible, for one (FR-096). Emitted as "detail"
	// only when set (additive to the schema). Never a secret: the same
	// rule as every field of the trail.
	Detail string
	// Setting names the configuration key that decided the outcome, on
	// the events a setting causes rather than an identity — the hardening
	// refusals of FR-098 (ActionHardeningRefused). Emitted as "setting"
	// only when set (additive to the schema, like actor_name).
	Setting string
	// Exception, ExceptionSource and ExceptionExpires name the scan
	// exception an event is about (ActionScanExceptionExpired, R-15): its
	// identifier, where it was written ("instance", "retriever", or
	// "vex:<document path>"), and its expiry, RFC 3339. Emitted as
	// "exception", "exception_source" and "exception_expires" only when
	// set (additive to the schema).
	Exception        string
	ExceptionSource  string
	ExceptionExpires string
}

// Log emits e on logger at INFO level. The timestamp is supplied by the
// logging layer ("ts"); all six schema fields plus the marker and schema
// version are present on every record.
func Log(ctx context.Context, logger *slog.Logger, e *Event) {
	attrs := []slog.Attr{
		slog.String(KeyLogType, LogType),
		slog.Int("audit_schema", SchemaVersion),
		slog.String("actor", e.Actor),
		slog.String("action", e.Action),
		slog.String("target", e.Target),
		slog.String("outcome", e.Outcome),
		slog.String("origin", e.Origin),
	}
	if e.ActorName != "" {
		attrs = append(attrs, slog.String("actor_name", e.ActorName))
	}
	if e.Detail != "" {
		attrs = append(attrs, slog.String("detail", e.Detail))
	}
	if e.Setting != "" {
		attrs = append(attrs, slog.String("setting", e.Setting))
	}
	if e.Exception != "" {
		attrs = append(attrs, slog.String("exception", e.Exception),
			slog.String("exception_source", e.ExceptionSource),
			slog.String("exception_expires", e.ExceptionExpires))
	}
	logger.LogAttrs(ctx, slog.LevelInfo, e.Action, attrs...)
}

// The operations a media waiver is applied to, as MediaOverrideDetail
// names them.
const (
	// MediaOperationVerify is a verification of the medium on its own —
	// the screen's Verify step, POST /api/v1/media/verify, `tobby media
	// verify`. Its verdict reaches the serving gate (FR-054).
	MediaOperationVerify = "verify"
	// MediaOperationImport is a media import, which re-verifies under the
	// same waiver before it pushes anything (FR-052).
	MediaOperationImport = "import"
)

// MediaOverrideDetail is the detail of an applied media.override record:
// the operation the waiver was applied to, the zone this instance serves
// and the medium's identity, space-separated key=value pairs. An empty
// value is left out rather than written as "zone=".
func MediaOverrideDetail(operation, zone, mediaID string) string {
	parts := make([]string, 0, 3)
	for _, kv := range [][2]string{{"operation", operation}, {"zone", zone}, {"media", mediaID}} {
		if kv[1] != "" {
			parts = append(parts, kv[0]+"="+kv[1])
		}
	}
	return strings.Join(parts, " ")
}
