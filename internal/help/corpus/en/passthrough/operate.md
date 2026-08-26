---
title: Operate over time
description: Probes, task tracking and resume, what to alert on, backup, store growth, upgrades, and graceful shutdown.
sidebar:
  order: 7
  badge:
    text: Partial
    variant: caution
---

A passthrough instance is designed to run unattended for months. This
page is what that costs you: what to watch, what to back up, what grows,
and how to upgrade. Some of the tooling that will make parts of this
easier is still ahead — those parts are badged below rather than
hidden.

## Probes and metrics

| Path | Role |
| --- | --- |
| `/healthz` | Liveness. Answers as soon as the listener is up: the process is alive, not necessarily useful. |
| `/readyz` | Readiness. 503 until the store and the configuration are usable, and 503 again during the shutdown drain. |
| `/metrics` | OpenMetrics (FR-091). Behind the same authentication as every other surface — give your scraper a viewer account or token; the chart's ServiceMonitor supports basic auth. |

Opening a large store takes time: the reference deployment uses a
startup probe (30 × 5s) rather than a lax liveness threshold, so the
instance gets time to come up without a hung process surviving minutes
afterwards.

## Tasks are the unit of observation

Every synchronization and every one-off import runs as a tracked task
(FR-062): state, per-ingredient progress, and a log downloadable raw.
The task list is `/tasks` in the UI and `GET /api/v1/tasks` in the API
(both paginated); a task's detail and log are
`GET /api/v1/tasks/{id}` and `GET /api/v1/tasks/{id}/logs`. Logs are
structured JSON with stable correlation keys — run ID, task ID, recipe,
ingredient, digest (FR-090) — so one synchronization is fully
reconstructable by filtering on its task ID. See
[metrics and logs](../../reference/metrics-logs/) for the schema.

Finished tasks are bounded: the queue keeps the most recent
`tasks.keepFinished` (default 500; `0` keeps everything), purging older
entries together with their log files. Pending and running tasks are
never purged.

### Interrupted transfers resume

A killed process or a cut connection does not restart a run from
scratch. A synchronization resumes from persisted state, and blobs
already stored are never re-downloaded (FR-029). Since v0.4.0, resume is
fine-grained inside large blobs too (R-29): above
`transfer.resumeThreshold` (default 64MiB) the bytes are spooled in the
state directory with their offset, and the next attempt asks for the
rest with an HTTP `Range` request — a download interrupted at 90 %
restarts at 90 %, surviving a killed process, not just a dropped
connection. Integrity stays blocking: the digest is computed over the
whole spool, resumed prefix included, before a byte reaches the store,
and a source that ignores `Range` is detected and restarted rather than
concatenated. The task detail shows per-blob progress, including whether
a transfer resumed.

The operational consequence: the state volume temporarily holds one copy
of each resumable blob in flight — the reference deployment sizes it at
20Gi for this reason. `transfer.resumeThreshold: 0` disables the spool
and restores pure streaming.

## What to alert on

- `/readyz` non-200 outside deploy windows.
- Failed tasks — poll `GET /api/v1/tasks` or alert on the task-failure
  metrics.
- Policy refusals: allowlist rejections (FR-030) and signature
  verification failures (FR-033) are logged, audited, and counted in
  metrics. In a healthy zone these are zero; any step is either an
  attack or a configuration drift, and both deserve a page.
- A last-successful-sync age beyond a few multiples of `sync.interval` —
  a proxy or credential silently broken shows up here first.
- State-volume usage, since resumable spools live there.

The metric families are listed in
[metrics and logs](../../reference/metrics-logs/) — with the honest caveat
that their names are not yet contractual.

## Backup: the state directory

The state root is **the** backup target. It holds what nothing
recreates: accounts, tokens, the served TLS pair, the interval override.
It is small, so back it up like any precious directory — snapshots or
file copies of `state.root`, taken while the instance is stopped or from
a filesystem snapshot. The store needs no backup: everything in it can
be fetched again, and losing it costs bandwidth, not identity. Never
put the state on the store volume; Tobby refuses the nesting outright.

:::note[Upcoming — milestone 7]
A documented restore-and-rebuild procedure (rebuilding a full instance
from a state backup plus re-synchronization, R-27) is planned for
milestone 7. Track it on the [project status](../../discover/status/) page.
:::

## Store growth, stated plainly

Today, **nothing cleans the store automatically in passthrough mode.**
Every synchronized version and every one-off import stays until an
administrator removes unit-imported repositories by hand (FR-044) —
recipe-managed content is not individually removable at all. A zone
whose recipes track moving constraints accumulates every version they
ever resolved. Size the store volume with that in mind, and monitor it.

:::note[Upcoming — milestone 5]
Store cleanup arrives with R-33 (prune-to-Retriever extended to the
passthrough transit store). Until then, growth is monotonic — that is a
current limit, not a fine print. Track it on the
[project status](../../discover/status/) page.
:::

:::note[Upcoming — milestone 5]
A plan / dry-run mode for passthrough (R-04) — showing what a
synchronization *would* transfer before it does — ships with
milestone 5.
:::

:::note[Upcoming — milestone 6]
On-demand integrity verification of the store through the UI and API
(R-31) ships with milestone 6.
:::

## Upgrading

Read the [release notes](../../discover/status/) first; the compatibility
policy — what is stable already, what freezes at 1.0, and how store
formats are carried across versions — lives in
[release process and compatibility](../../project/release-compatibility/).

- **Packages:** verify the new package, then install it over the old one
  (`dpkg -i` / `rpm -U` / `apk add --allow-untrusted`) and restart the
  service. Packages are scriptless; nothing runs on install.
- **Kubernetes:** `helm upgrade tobby ./deploy/charts/tobby --namespace
  tobby --reuse-values --set image.tag=v0.4.2` — pin `image.digest` in
  production. The strategy is `Recreate` with a single replica: the old
  pod releases the volumes before the new one starts, so there are never
  two writers on one store. Expect a short outage per upgrade; both
  PVCs carry `helm.sh/resource-policy: keep`, so even `helm uninstall`
  leaves the data behind.

:::note[Upcoming — milestone 6]
Tobby updating through its own OCI channel (R-25) — the new release
travelling as verified content, across zones and through the air gap
like everything else it carries — ships with milestone 6.
:::

## Graceful shutdown

On SIGTERM or SIGINT, the instance stops accepting new work, turns
`/readyz` to 503, and gives in-flight transfers
`shutdown.gracePeriod` (default 30s, `--shutdown-grace-period`) to
finish or checkpoint before exiting 0 (FR-093). Checkpointed transfers
resume on the next start. Whatever supervises the process must wait
longer than the grace period — `terminationGracePeriodSeconds: 60` in
the reference deployment, `TimeoutStopSec=60` in a systemd unit —
or the final kill lands mid-checkpoint.

That closes the passthrough journey. From here:
[write recipes](../../recipes/write-and-publish/) to grow what the zone
holds, or read how the same instance prepares for
[isolated zones](../../air-gap/media-workflow/).
