---
title: Releases and compatibility
description: Version trains, the definition of done, the patch policy, and exactly what is stable now versus frozen at 1.0.
sidebar:
  order: 4
---

This page is the canonical home of the compatibility policy. Other pages
link here; none restates it.

## Version trains

One milestone, one minor train: milestone N ships as `v0.N.x`. Delivered
so far, from the
[changelog](https://github.com/tobby-fetch/tobby-fetch/blob/main/CHANGELOG.md):

| Train | Milestone | First release | Scope |
|---|---|---|---|
| v0.1.x | Foundations | v0.1.0 — 2026-08-11 | build chain, embedded registry, quality gates |
| v0.2.x | UX preview | v0.2.0 — 2026-08-12 | UI, auth by default, error taxonomy, tasks, `/api/v1` |
| v0.3.x | Recipe engine | v0.3.0 — 2026-08-16 | signed recipes, verification at entry, cascade |
| v0.4.x | Passthrough | v0.4.0 — 2026-08-18 | continuous promotion, allowlist, RBAC, enterprise network |

The current state and the path to 1.0 are on the
[project status](../../discover/status/) page — the single source of truth
for feature status.

## Definition of done

A milestone does not ship until: the SRS requirements in scope are
implemented and tested, the milestone's crucible scenarios are written and
green, user-facing strings exist in English and French, the documentation
of the milestone is current, and the **raw acceptance report is
published** — see [acceptance reports](../acceptance-reports/).

## Patch policy — two precedents

A patch release carries fixes only, never features, on the current minor
train. The policy is best read through its precedents:

- **v0.4.1** (2026-08-18, same day as v0.4.0) closed one requirement gap
  found immediately after acceptance: an instance serving the generated
  fallback certificate could not adopt a replacement from the
  administration screen (FR-082). One fix, released without waiting.
- **v0.4.2** (2026-08-22) carried the fixes of the
  [milestone-4 quality audit](../acceptance-reports/): two confirmed
  concurrency defects in the long-lived service path, an unbounded task
  history, and a batch of surface and toolchain hardening. No features —
  and the release was re-accepted by replaying the **entire** crucible
  suite, m1 through m4, on a fresh disposable host.

Security fixes follow
[SECURITY.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/SECURITY.md):
patch of the current minor line, coordinated disclosure, only the latest
minor supported before 1.0.

## Stable already, in 0.x

Some surfaces were declared stable long before 1.0, because operators
script against them from day one:

- **CLI exit codes** — 0 success, 1 failure, 2 usage, 3 policy refusal,
  4 verification failure, following the error-taxonomy classes since
  v0.2.0. See the [CLI reference](../../reference/cli/).
- **`TBY-*` error codes** — short stable identifiers; a code never changes
  meaning. See [errors and troubleshooting](../../reference/errors/).
- **`/api/v1`** — the versioned API root (FR-060), documented by the
  OpenAPI document the instance serves. See the
  [API reference](../../reference/api/).
- **The audit-event schema** — six fields (actor, action, target, outcome,
  timestamp, origin), versioned with the same compatibility discipline as
  the REST API (FR-094). See the [audit log](../../security/audit-log/).

## Frozen at 1.0

At 1.0 the additive-evolution rule becomes a SemVer guarantee across
**four surfaces**: the CLI, the REST API, the configuration schema, and
the on-disk storage layout.

For the store, the guarantee is concrete (R-26): the store already stamps
its format version with an explicit compatibility policy, and across the
1.x series a store written by any 1.x version is readable by any other, in
both directions. If an unsupported version is ever encountered, the error
names both versions in play — and the escape hatch is standard OCI
tooling: the store exports and imports as an OCI image layout that skopeo,
oras and crane read (FR-051). At worst, your content is never captive.

The CLI carries its own contract under the same promise (R-08): `--output
json` on every command that reports anything, with a published JSON
Schema; the [exit-code table](../../reference/cli/#exit-codes), generated
from the code and covered by SemVer — removing a code or renumbering one
is a breaking change; and guaranteed non-interactive operation. Each of
the four is held by a test that walks the real command tree.

## How defects are tracked today

Tobby is in its development phase, milestones landing weekly. Current
practice, visible in the repository: defects get a stable `B-nnn`
identifier on the maintainers' tracking list, and every fixed defect is
disclosed in the
[changelog](https://github.com/tobby-fetch/tobby-fetch/blob/main/CHANGELOG.md)
under that identifier **with its root cause** — read the v0.4.0 entries
for B-014 or B-015 to see the level of detail. Known issues that are
found but deferred are published too: the milestone-4 quality audit's
"deferred, with owners" list is public in
[`docs/acceptance/`](https://github.com/tobby-fetch/tobby-fetch/tree/main/docs/acceptance).

Bug reports from outside are welcome now, through the repository's
[issue templates](https://github.com/tobby-fetch/tobby-fetch/issues) —
except suspected vulnerabilities, which go through the private channel in
SECURITY.md. As the project approaches 1.0, public issues become the
single tracker for defect follow-up as well.
