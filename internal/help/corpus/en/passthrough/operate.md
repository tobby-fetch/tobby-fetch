---
title: Operate over time
description: Probes, task tracking and resume, what to alert on, backup, store growth, upgrades, and graceful shutdown.
sidebar:
  order: 7
---

A passthrough instance is designed to run unattended for months. This
page is what that costs you: what to watch, what to back up, what grows,
and how to upgrade.

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
  a proxy or credential silently broken shows up here first
  (`tobby_sync_last_success_timestamp_seconds`).
- A listener certificate close to its end (`tobby_server_certificate_expiry_timestamp_seconds`).
- State-volume usage, since resumable spools live there.

The reference chart ships alert rules for these conditions and an example
dashboard, and every release publishes the metrics reference they are held
to: see [Supervise an instance](../monitoring/). When an alert needs someone
else's eyes, collect a [diagnostic bundle](../diagnostics/) — one redacted
local archive. The families are listed in
[metrics and logs](../../reference/metrics-logs/).

## Backup: the state directory

The state root is **the** backup target. It holds what nothing
recreates: accounts, tokens, the served TLS pair, the import record, the
interval override. It is small, so back it up like any precious
directory — snapshots or file copies of `state.root`, taken while the
instance is stopped or from a filesystem snapshot — together with the
configuration file and the secret files it references. The store needs no
backup: everything in it can be fetched again, and losing it costs
bandwidth, not identity. Never put the state on the store volume; Tobby
refuses the nesting outright.

The layout of the state directory is documented and checked at every start,
`tobby state inspect` lists it, and the rebuild procedure is tested end to end:
[Back up and rebuild an instance](../backup-rebuild/).

## Store growth, stated plainly

**By default, nothing cleans the store automatically in passthrough
mode.** Every synchronized version and every one-off import stays until
an administrator removes unit-imported repositories by hand (FR-044) —
recipe-managed content is not individually removable at all. A zone whose
recipes track moving constraints accumulates every version they ever
resolved.

That default is deliberate. A passthrough transit store is not a delivery
unit, and an operator asking for fresher content has not asked for older
content to be deleted: a reconciliation loop that quietly shrinks the
store a zone pulls from is exactly the failure this default prevents.

### Pruning to the Retriever

Set `sync.prune: true` (or `TOBBY_SYNC_PRUNE=true`) and every
reconciliation cycle removes the recipe-managed content the resolved
Retriever no longer references. Three kinds of content are **never**
eligible, because none of them is recipe-managed:

- unit imports (FR-023),
- the offline vulnerability database (FR-032),
- anything pushed through `/v2/` outside managed namespaces (UC3 seeding).

Two safeguards are worth knowing. A cycle in which **any** recipe failed
to resolve prunes nothing and says so in the run log: content of a recipe
that did not resolve is indistinguishable from content the Retriever
dropped, and deleting on the strength of a network failure is not a
trade this product makes. And every removed item is named — repository,
tag, digest, and the recipe that brought it — in the run log of the
cycle, not merely counted.

### Watching the volume

Set `storage.occupancyThreshold` (for example `500GiB`) and the instance
says when it is over: a persistent warning on every UI page, the same
fact on `GET /api/v1/content` and `GET /api/v1/retriever`, and the
`tobby_store_occupancy_exceeded` metric. Crossing back under retracts all
three — a warning that appears and never clears is a warning operators
learn to ignore. Unset means unmonitored, which is reported as such and
never as "within limits".

### Seeing it coming

`tobby sync --dry-run` reports what the next synchronization would do
without doing any of it: resolved versions, per-digest statuses, the
deduplicated volume to transfer, the projected store size against the
volume's free space, **and the content a prune would remove**. It writes
nothing and does not touch the reconciliation schedule. The same report is
on `POST /api/v1/plan` and on the `/recipes/plan` screen, where a candidate
Retriever can be planned instead of the configured one — which is how you
review a Retriever change before adopting it.

Exit code `5` means "changes planned", distinct from `0` ("nothing to do"),
so a pipeline can gate on it without treating a plan with work in it as a
broken build. See the [CLI reference](../../reference/cli/#tobby-sync).

:::tip[Verify the store on demand]
A plan reads metadata; to check the bytes, verify the store: every blob
re-hashed against its digest, manifests checked for completeness, recipe
signatures against the trust roots configured now, with a timestamped
report — from the interface, the API or `tobby store verify`
(R-31, since v0.6.0). See [verify the store's integrity](../verify-store/).
:::

## What the store holds, and from where

Every repository of the content browser (`/content`) carries its
**provenance class** (FR-098): `recipe` — managed by recipes verified
against this instance's trust roots, named on the repository page, with
the reduced-assurance marker when a relaxed trust scope admitted them —
`unit-import` — a [one-off import](../one-off-import/), or a FileSet
packed on this instance (shown as such) — or `seed` — pushed through
`/v2/` by a standard client. The repository page also shows, per tag, the
result and the date of its last vulnerability scan with the age of the
database it was scanned with, when this instance scanned it. The same facts
are on `GET /api/v1/content` (the `provenance` of each row) and
`GET /api/v1/content/{repo}` (the `provenance` object, and `lastScan` on
each tag); the log records that concern a repository — an import, a push,
a removal — carry a `provenance` field.

`tobby_store_unmanaged_repositories{class}` counts what no recipe manages,
by class: a zone that should hold recipe content only alerts on it being
non-zero. To make that a guarantee rather than a watch, set the
[hardening switches](../../security/model/#hardening-switches):
`hardening.disableUnitImport` closes unit imports and packing on every
surface, `hardening.disableRegistryPush` makes `/v2/` read-only for every
client and role. Both are stated at startup, in `tobby config dump`, on
the screens they disable and in `tobby_hardening_enabled`; recipe
synchronizations are unaffected. The last scan of a tag is read from the
task reports the instance still holds, so `tasks.keepFinished` bounds how
far back it reaches.

## Upgrading

Read the [release notes](../../discover/status/) first; the compatibility
policy — what is stable already, what freezes at 1.0, and how store
formats are carried across versions — lives in
[release process and compatibility](../../project/release-compatibility/).

- **Packages:** verify the new package, then install it over the old one
  (`dpkg -i` / `rpm -U` / `apk add --allow-untrusted`) and restart the
  service. Packages are scriptless; nothing runs on install.
- **Kubernetes:** `helm upgrade tobby oci://ghcr.io/tobby-fetch/charts/tobby --version
  <new version> --namespace tobby --reuse-values` — the chart version is
  the Tobby version, so it moves the image with it. If an earlier upgrade
  set `image.tag`, `--reuse-values` keeps it: clear it with
  `--set image.tag=` or the image will not move. Pin `image.digest` in
  production. The strategy is `Recreate` with a single replica: the old
  pod releases the volumes before the new one starts, so there are never
  two writers on one store. Expect a short outage per upgrade; both
  PVCs carry `helm.sh/resource-policy: keep`, so even `helm uninstall`
  leaves the data behind.

:::tip[Through its own channel]
Since v0.6.0, Tobby can also be updated through its own OCI channel
(R-25): each release is published recipe-ready, and travels as verified
content, across zones and through the air gap, like everything else it
carries. See [updating Tobby in an isolated zone](../../air-gap/update-tobby/).
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
