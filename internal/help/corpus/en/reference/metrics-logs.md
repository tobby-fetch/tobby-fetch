---
title: Metrics and logs
description: The metrics the instance exposes today, the JSON log schema and its stable correlation keys, and what to alert on.
sidebar:
  order: 5
  badge:
    text: Partial
    variant: caution
---

One listener, two observation surfaces: `/metrics` (OpenMetrics) and the
structured JSON logs on stdout. This page lists what the current v0.5.x
series actually exposes, extracted from `internal/metrics` and
`internal/logging`.

## Metrics

:::caution[Not contractual yet]
The metric list below is real and accurate for the current series, but it
is **not a stability contract** before the supervision kit ships (R-11,
milestone 7) — names and labels may still change between minor versions.
The log schema's stable keys and the audit schema are contractual today;
the metrics are not yet. Track it on the
[project status](../../discover/status/) page.
:::

`GET /metrics` serves the OpenMetrics exposition (content negotiation on
top of the classic Prometheus format). It is one of the
[reserved prefixes](../../reference/api/#reserved-path-prefixes) and requires
no authentication, like the probes.

Domain metrics registered today:

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `tobby_build_info` | gauge | `version`, `commit` | Build metadata of the running binary; the value is always 1. |
| `tobby_sync_transfers_inflight` | gauge | — | Ingredient transfers currently in flight, bounded by `sync.parallelism`. |
| `tobby_sync_transferred_bytes_total` | counter | — | Bytes transferred by recipe synchronizations (skipped up-to-date content moves nothing). |
| `tobby_policy_rejections_total` | counter | `code` | Transfers refused by policy, by taxonomy code (the allowlist today: `TBY-POL-001`). |
| `tobby_promotion_pushes_total` | counter | `result` = `pushed` \| `skipped` | Promotion outcomes. `skipped` is the healthy signal: a settled promotion between two zones is almost nothing but skips — "the destination is at the level the Retriever asks for" as a dashboard assertion. Both label values exist from the first scrape. |
| `tobby_promotion_pushed_bytes_total` | counter | — | Bytes promotion actually moved; an already-synchronized recipe adds nothing (the differential, observed rather than assumed). |
| `tobby_promotion_refusals_total` | counter | `code` | Pushes refused before transfer, by taxonomy code (`TBY-POL-001` allowlist, `TBY-SIG-001` pre-push signature re-verification, `TBY-DST-001` destination limits). |
| `tobby_store_occupancy_bytes` | gauge | — | On-disk size of the store's deduplicated blob tree at the last sample; `0` while no `storage.occupancyThreshold` is configured and nothing is sampled. |
| `tobby_store_occupancy_threshold_bytes` | gauge | — | The configured threshold; `0` means no threshold is set and the store is not monitored. |
| `tobby_store_occupancy_exceeded` | gauge | — | `1` while the store exceeds its threshold, `0` otherwise. A gauge, not a counter, on purpose: clearing the condition must be as visible as raising it. All three exist from the first scrape. |
| `tobby_integrity_verifications_total` | counter | `verdict` = `clean` \| `defects` | Completed on-demand store verifications ([`tobby store verify`](../cli/#tobby-store-verify), the Store integrity screen, `POST /api/v1/store/verify`). Orphaned content alone never makes a verdict `defects`. Both verdicts exist from the first scrape, at zero. |
| `tobby_integrity_last_verification_timestamp_seconds` | gauge | — | End time of the last completed verification, in Unix seconds. **Absent until one has completed**: a value of `0` would read as 1970, a number where the truth is "never verified" — alert on absence, or on age. |
| `tobby_assurance_reduced_recipes` | gauge | `scope` | Recipes currently held under [reduced assurance](../../security/reduced-assurance/), by the declared trust scope that admitted them. Computed at every scrape from the recipe graph, never counted by events. Every scope that allows unsigned recipes is present from the first scrape, at zero while it holds nothing, so clearing the last one is as visible as admitting the first. |
| `tobby_auth_attempts_total` | counter | `method` = `basic` \| `token` \| `oidc` \| `saml`, `outcome` = `success` \| `failure` \| `expired` | Credential checks on the machine surfaces and sign-ins on the UI (R-14). `expired` is a token refused past its expiry. Every series exists from the first scrape. |
| `tobby_auth_sessions_expired_total` | counter | `reason` = `idle` \| `lifetime` | UI sessions ended by `auth.session.idleTimeout` or `auth.sessionTTL` (R-14), each also a `session.expire` audit record. |
| `tobby_auth_idp_reachable` | gauge | `method` = `oidc` \| `saml` | `1` while the identity provider answers the background probe, `0` while it does not (R-20). Absent until first probed, and absent for SAML with pinned metadata, which needs no server-to-server call. |
| `tobby_auth_local_logins_under_federation_total` | counter | — | Local-account sign-ins while OIDC or SAML is enabled — break-glass use, each also an `auth.local_under_federation` audit record (FR-078). |

The standard Go runtime and process collectors are registered alongside.

Label discipline is deliberate: refusal metrics are labeled by **taxonomy
code**, never by host — a metric labeled with an attacker-supplied host is
an unbounded-cardinality hole. The host is in the log record, where it
belongs.

## JSON logs

The instance logs structured JSON to **stdout**, one object per line
(`log/slog`, no third-party framework). Level is set by `logging.level`
(default `info`).

**In mirror mode the same records go to a file on the transport medium
instead** — `_tobby/logs/operations.log` by default — because the medium is
the audit channel of the transfer and whoever receives it has no access to
the workstation's stdout. Same schema, same keys, plus a durability
contract: fsync at every task boundary and size-based rotation. See
[tracing a transfer](../../air-gap/traceability/).

Stable keys on every record:

| Key | Content |
|---|---|
| `ts` | RFC 3339 timestamp, UTC, nanosecond precision. |
| `level` | `DEBUG`, `INFO`, `WARN`, `ERROR`. |
| `msg` | The event, a stable English sentence. |

Stable **correlation keys**, added as work narrows down — these names are
fixed by the log schema and safe to build extraction rules on:

| Key | Content |
|---|---|
| `run_id` | Identifies one synchronization run end to end. It crosses the air gap on the media manifest and is reused by the destination instance (see [Trace and prove a transfer](../../air-gap/traceability/)). |
| `task_id` | One tracked task. Shown as the correlation identifier alongside taxonomy errors, so an error on screen finds its log records. |
| `recipe` | The recipe being processed. |
| `ingredient` | The ingredient reference. |
| `digest` | The content digest concerned. |
| `assurance` | Present **only** on records concerning content admitted under a relaxed trust scope, with the value `reduced` ([reduced assurance](../../security/reduced-assurance/)). Filter on its presence to find them. |
| `assurance_scope` | The declared trust scope behind `assurance`, on the same records and only there. |

Other fields are contextual per message (many notable lines carry a
`requirement` field naming the SRS requirement they implement) and are not
part of the stable schema.

Every task also keeps its own log file inside the store, next to its task
record — this is what the task-detail screens and
`GET /api/v1/tasks/{id}/logs` serve, and finished-task retention
(`tasks.keepFinished`) purges both together.

Audit events travel in the same stream, with their own versioned
schema — see [Audit log](../../security/audit-log/).

## What to alert on today

With the means of the current series:

- **`readyz`** — readiness flips to 503 during startup and drain; a
  sustained 503 is an instance that cannot serve.
- **`tobby_store_occupancy_exceeded == 1`** — the store is past the
  threshold its operator set. Nothing bounds what an unattended
  passthrough instance accumulates unless `sync.prune` is on, so this is
  the alert that stands between "growing" and "the volume is full and
  writes are failing". It falls back to `0` on its own once the store is
  under the threshold again.
- **`rate(tobby_promotion_refusals_total[…]) > 0`** — a promotion refusal
  is never routine: it is an allowlist violation, a signature that stopped
  verifying, or a destination limit. The `code` label says which; the logs
  carry the host and reference.
- **`rate(tobby_policy_rejections_total[…]) > 0`** — same reasoning on the
  fetch side.
- **`tobby_auth_idp_reachable == 0`** — the identity provider is
  unreachable: people sign in with the local break-glass account meanwhile
  (FR-078), which **`increase(tobby_auth_local_logins_under_federation_total[…]) > 0`**
  shows — worth an alert on its own when the provider is up.
- **`rate(tobby_auth_attempts_total{outcome="expired"}[…]) > 0`** — a
  client still presents an expired token: replace it before it is the
  one that matters.
- **Absence of `tobby_promotion_pushes_total` movement** (neither `pushed`
  nor `skipped` increasing) on a passthrough instance with a configured
  retriever — the reconciliation loop stopped cycling.
- **`ERROR`-level log records**, and `WARN` records around fileset
  resolution and scheduling skips ("promotion cycle skipped" is
  informational coalescence, not a failure).

:::note[Upcoming — milestone 7]
The supervision kit (R-11: curated dashboards, alert rules, and the metric
stability contract) and the redacted diagnostic bundle (R-30) ship with
milestone 7. Until then, alerting is assembled from the primitives above.
Track both on the [project status](../../discover/status/) page.
:::
