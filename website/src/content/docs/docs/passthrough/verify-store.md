---
title: Verify the store's integrity
description: Verify a whole store on demand, in either mode — blobs against their digests, manifests for completeness, recipe signatures against the trust roots configured now, and orphaned content — from the command line, the interface and the API.
sidebar:
  order: 8
---

A store is only as good as its bytes. Tobby already checks them in pieces:
at import, before a push, and on a medium that arrives. This page is the
check you run **whenever you want to know**: a whole-store verification,
on demand, with a timestamped report you can keep. It works the same way
on an instance's store and on a transportable one — the one a mirror
synchronization produces, or a medium that arrived.

The verification **only reads**. It repairs nothing, deletes nothing and
writes nothing into the store, which is byte-identical afterwards.

## What it checks

Four checks, each independent of the others' verdict:

| Check | What it establishes | Defect |
|---|---|---|
| **Blobs** | Every blob in the content-addressed tree is re-hashed and compared with the digest it is stored under. A truncated or altered file is named by its path, with the digest it should have and the one it has. | [`TBY-INT-001`](../../reference/errors/#tby-int-001) |
| **Manifests** | Every manifest reachable from a recorded recipe — the recipe artifact, its signature artifacts, every ingredient — is complete: the manifest itself, its config and layers, the children of an index. A platform-filtered index keeps its pinned digest while carrying only some of its children (FR-022), so an absent child of an index is *not* a defect. A manifest that cannot be parsed is reported too, because what it references could not be checked. | [`TBY-INT-002`](../../reference/errors/#tby-int-002), [`TBY-INT-005`](../../reference/errors/#tby-int-005) |
| **Signatures** | Every recorded recipe's signature is verified against the trust roots configured on **this instance now**, with scopes applied. A key that has since left the trust roots, or a scope that no longer admits unsigned recipes, shows up here. A recipe admitted unsigned by a declared scope is reported with its [reduced-assurance marker](../../security/reduced-assurance/). | [`TBY-SIG-001`](../../reference/errors/#tby-sig-001), naming the scope |
| **Orphans** | Content that no recorded recipe and no protected root reaches is listed. Protected roots are the ones the prune keeps: unit imports, content seeded through `/v2/`, the offline vulnerability database. | [`TBY-INT-003`](../../reference/errors/#tby-int-003) |

The verdict is **clean** or **defects**. Orphaned content is a note, never a
reason for `defects`: it asks to be listed, and listing it is the whole of
what it asks. Two kinds are reported under the same code — a blob no
manifest reachable from any tag references (what a garbage collection would
reclaim once its grace period passes), and a tag of a recipe-managed
repository that no recorded recipe reaches (what a removed or superseded
recipe left behind). Content a synchronization is still writing can appear
as orphaned until the run finishes.

## From the command line

```sh
tobby store verify --storage-root /srv/tobby/store
tobby store verify --config /etc/tobby/config.yaml --output json
```

The command runs in its own process, against the directory, and works on a
host where no instance is running — a workstation that just received a
medium, for example. The trust roots come from the configuration
(`trust.roots`, `trust.scopes`), **never** from the store: a medium carries
none that count. It refuses a directory that is not already a Tobby store,
instead of stamping it.

Exit codes follow the [published table](../../reference/cli/#exit-codes):
`0` for an intact store, `4` — the verification class — when a defect was
found. A script can branch on it, and read the report with
`--output json`; the document is described by the schema published beside
the OpenAPI document.

:::caution
When an instance **is** serving the store, prefer the screen or the API
below. They hold the instance's content lock, so a removal in progress
cannot read as a defect, and they leave a tracked task. The command line
cannot see that lock.
:::

## From the interface

The **Store integrity** screen (`/integrity`, linked from the Content
screen and from the store administration screen) starts a verification and
shows its report. While a verification runs the screen shows the stage it is
in and its progress, and it updates by itself; once the verdict lands it
stops asking. Reading the last report is open to every role; **starting** a
verification needs the operator role, because it re-reads the whole disk.

The screen never offers to repair or delete anything. The report names what
is wrong; the tools to fix it are the ones the product already has —
re-synchronize the recipe that reaches a damaged blob, prune to the
Retriever to reclaim orphaned content (see
[Operate over time](../operate/#store-growth-stated-plainly)).

## From the API

| Request | Answer |
|---|---|
| `POST /api/v1/store/verify` | `201` with the tracked task. `409` ([`TBY-INT-004`](../../reference/errors/#tby-int-004)) naming the verification in flight when one is already pending or running — one at a time. |
| `GET /api/v1/store/verification` | The last report, whole, with the id and status of the task that produced it; while it runs, a snapshot carrying its progress. `verification` is `null` before any has been requested. |

The same report is on the task itself (`GET /api/v1/tasks/{id}`, member
`verification`). A store with defects is a **failed** task carrying its
principal defect, so `tobby sync`-style `--wait` logic and monitoring that
reads task statuses see it the way they see any other failure.

One consequence worth knowing: the verification runs on the instance's
serial task worker, so it holds the queue for as long as it takes — minutes
on a large store. Other tasks wait; nothing is lost.

## Reading the report

```json
{
  "run_id": "20261111T200000Z-3f9a1c7e",
  "started_at": "2026-11-11T20:00:00Z",
  "finished_at": "2026-11-11T20:03:41Z",
  "verdict": "defects",
  "store": { "root": "/srv/tobby/store", "media_id": "…", "transportable": true },
  "checked": { "blobs": 412, "bytes": 8388608000, "manifests": 96, "recipes": 12 },
  "recipes": [ { "name": "wordpress", "version": "6.8.2", "complete": true,
                 "signature": "verified", "key_fingerprint": "SHA256:…" } ],
  "defects": [
    { "code": "TBY-INT-001",
      "path": "docker/registry/v2/blobs/sha256/ab/abcd…/data",
      "params": { "expected": "sha256:abcd…", "actual": "sha256:9f2e…" } }
  ],
  "findings": [ { "code": "TBY-INT-003", "path": "docker.io/library/old:1.0" } ],
  "defect_count": 1, "finding_count": 1
}
```

- **Timestamped and correlated.** `started_at`, `finished_at` and the
  `run_id` of the task, so the report can be matched to its log records.
- **Codes and parameters, never sentences.** Each defect is a taxonomy code
  and the file or object it concerns; the screen and the command render it
  in your language, and a script keys on the code.
- **Bounded, with exact counts.** A store with a million damaged blobs has
  one problem, not a million lines: the first 500 of each list are named,
  `defects_omitted` and `findings_omitted` say how many more there were, and
  `defect_count` and `finding_count` are always exact.
- **A row per recorded recipe**, with whether its content is complete, how its
  signature fares under the trust configuration of this instance now, and
  its assurance marker when a scope admitted it unsigned.

## What is left to you

A verification is the answer, not the fix. A damaged or missing blob is
repaired by re-synchronizing the recipes that reach it (or restoring the
store from a sound copy); a signature defect is a trust-roots question —
the error message names the scope when one decided it; orphans are reclaimed
by pruning or removing the recipe that held them. There is no recurring
schedule in v1: run it before you carry a medium, after you restore a
backup, or when a disk starts to worry you.

## Observability and audit

- `tobby_integrity_verifications_total{verdict}` counts completed
  verifications; `tobby_integrity_last_verification_timestamp_seconds` dates
  the last one and is **absent** until one has completed — alert on its age,
  or on its absence. See [metrics and logs](../../reference/metrics-logs/).
- A finished verification writes a `store.verify` audit event with the
  requester, the origin and the verdict as its outcome. See the
  [audit log](../../security/audit-log/).
