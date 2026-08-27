---
title: Audit log
description: The versioned six-field audit schema, the event catalogue dated by delivered version, durability guarantees, and how to ingest the trail into a SIEM.
sidebar:
  order: 5
---

Tobby emits a dedicated category of security audit events with a stable,
versioned schema (FR-094 — delivered v0.1.x, catalogue growing with each
milestone). Audit events are ordinary structured JSON log records on the
standard logging channels, separable from operational logs by a single
marker field — no second pipeline to deploy, nothing extra to fail.

## The schema

Six fields on every record, plus the marker and the schema version:

```json
{
  "ts": "2026-08-22T09:14:03Z",
  "level": "INFO",
  "msg": "token.create",
  "log_type": "audit",
  "audit_schema": 1,
  "actor": "alice",
  "action": "token.create",
  "target": "ci-deploy",
  "outcome": "success",
  "origin": "10.20.30.40:52114"
}
```

- **actor** — the authenticated identity, `local` for host-side CLI and
  process lifecycle, or the explicit anonymous context under the FR-075
  override.
- **action** — from a closed, documented catalogue (below).
- **target** — what the action applied to: an account, a token name, a
  repository, a configuration entry.
- **outcome** — `success`, `failure`, or `denied` (blocked by policy or
  authorization).
- **timestamp** (`ts`) and **origin** — the client network address, or
  `local`. Forwarding headers are deliberately not trusted for the
  origin: they are client-supplied.

The schema is versioned through `audit_schema` and evolves additively
only, with the same compatibility discipline as the REST API
([release and compatibility](../../project/release-compatibility/)).

## Event catalogue, dated by delivered version

| Action | Meaning | Since |
| --- | --- | --- |
| `instance.start`, `instance.stop` | process lifecycle | v0.1.x |
| `auth.override_active` | startup while `auth.disabled` is set (FR-075) | v0.4.x |
| `account.create`, `account.password_change`, `account.delete`, `account.role_change` | account lifecycle — host CLI, self-service, admin surfaces | v0.2.x–v0.4.x |
| `session.login`, `session.logout` | interactive UI sessions | v0.4.x |
| `auth.authenticate` | one credential verification on a machine surface (`/v2/`, `/api/v1`) | v0.4.x |
| `ui.access`, `api.access`, `registry.access` | authorization refusals, per surface — refusals only, by design: permitted traffic lives in the operational log | v0.4.x |
| `token.create`, `token.revoke` | static token lifecycle (FR-072) | v0.4.x |
| `import.create`, `sync.create`, `recipe.publish`, `content.delete` | content-affecting actions with their actor | v0.4.x |
| `config.promotion_interval`, `config.server_certificate` | sensitive configuration changes on a running instance (FR-013, FR-082) | v0.4.x |
| `media.import` | a transported medium pushed into the zone (FR-052) | v0.5.x |
| `media.override` | one of the two waivable media guards, attempted or applied (FR-054) | v0.5.x |
| `store.reset` | the confirmed store reset, including a refused confirmation (FR-046) | v0.5.x |
| `layout.export`, `layout.import` | OCI image layout leaving or entering the store (FR-051) | v0.5.x |
| `fileset.pack` | a host directory packed into the store as a FileSet (FR-048) | v0.5.x |
| `prune.active` | startup while retriever-aligned pruning is on (FR-045) | v0.5.x |

The media events joined the catalogue with v0.5.0: the two audited waivers
of a media import — a medium addressed to another zone and a medium older
than the last one imported here — and the confirmed store reset (FR-046).
Both the attempt and the applied waiver are recorded, with the actor and
the network origin they came from.

:::note[Upcoming — milestone 6]
Scanning policy events (FR-031) and the authentication-hardening lifecycle
events of R-14 join the catalogue with milestone 6.
:::

## Durability — and the trail that crosses the air gap

In passthrough mode audit records go to stdout with everything else
(FR-090): durability is your log collector's, which is where it belongs
for a long-lived service.

In mirror mode the log file lives **on the transport media**, under
`_tobby/logs/` and therefore outside the media manifest's coverage — a log
written inside coverage would invalidate, line by line, the inventory the
destination verifies. It has size-based rotation and an explicit fsync at
task boundaries: yanked media lose at most the task in progress (FR-053,
FR-056), which a test proves by killing the process outright. That file is
not a convenience: it is the audit trail that
physically crosses the air gap with the content, carrying the run ID that
the destination instance reuses, so one synchronization is traceable from
the connected side to the isolated side through the media (FR-090). See
[tracing a transfer](../../air-gap/traceability/).

## The unsigned-log position

The audit log is **not signed**. This is a stated position, not an
omission (FR-094): Tobby signs nothing, and a signature produced by the
same process that writes the records would attest nothing an attacker
with that process could not also forge. The trail is operational
evidence; the trust anchor for *content* is and remains cosign
verification against the destination's trust roots
([content trust](../../security/content-trust/)).

Compensating measures, stated with their limits: ship the stdout stream
to your SIEM in near-real time, so tampering after the fact requires
compromising the collector too; on media, the fsync-at-task-boundary
discipline bounds what a crash can lose; every record is correlated by
run ID, so a gap is visible as a gap. If your accreditation requires
tamper-evident logs, that property must come from your log platform —
Tobby's job is to hand it a stable, complete, parseable stream.

## SIEM ingestion

The whole trail is one filter away — no regexes, no format guessing:

```sh
# The security trail out of a mixed log stream
kubectl logs deploy/tobby | jq -c 'select(.log_type == "audit")'

# Failed and denied events only
jq -c 'select(.log_type == "audit" and .outcome != "success")'
```

Point your collector at the container stdout (passthrough), map the six
fields directly — they are stable across the 1.x series — and alert at
minimum on: `outcome == "denied"` bursts per `origin`,
`auth.override_active`, any `config.*` action, and `account.*` or
`token.*` actions outside change windows. The key set is small and fixed
on purpose: an audit schema you have to parse creatively is one you will
parse wrong.
