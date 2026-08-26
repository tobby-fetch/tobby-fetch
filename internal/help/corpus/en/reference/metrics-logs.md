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
structured JSON logs on stdout. This page lists what the current v0.4.x
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

The standard Go runtime and process collectors are registered alongside.

Label discipline is deliberate: refusal metrics are labeled by **taxonomy
code**, never by host — a metric labeled with an attacker-supplied host is
an unbounded-cardinality hole. The host is in the log record, where it
belongs.

## JSON logs

The instance logs structured JSON to **stdout**, one object per line
(`log/slog`, no third-party framework). Level is set by `logging.level`
(default `info`).

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
| `run_id` | Identifies one synchronization run end to end. This is the identifier that will cross the air gap on the media manifest (see [Trace and prove a transfer](../../air-gap/traceability/)). |
| `task_id` | One tracked task. Shown as the correlation identifier alongside taxonomy errors, so an error on screen finds its log records. |
| `recipe` | The recipe being processed. |
| `ingredient` | The ingredient reference. |
| `digest` | The content digest concerned. |

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
- **`rate(tobby_promotion_refusals_total[…]) > 0`** — a promotion refusal
  is never routine: it is an allowlist violation, a signature that stopped
  verifying, or a destination limit. The `code` label says which; the logs
  carry the host and reference.
- **`rate(tobby_policy_rejections_total[…]) > 0`** — same reasoning on the
  fetch side.
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
