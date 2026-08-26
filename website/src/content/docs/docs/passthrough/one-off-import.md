---
title: One-off imports
description: Import a single image, chart, or OCI artifact by reference — inspect first, pick platforms, transfer only what is missing.
sidebar:
  order: 6
---

Not everything deserves a recipe. A base image to try, a chart you are
evaluating, one artifact a developer asked for today — Tobby imports a
single OCI artifact of any media type by reference, outside any recipe
run and without a container runtime on the instance (FR-023). It landed
in milestone 2 and has been part of every release since.

## From the interface

The **Import** screen (`/import`, operator role) is a two-step,
inspect-before-import flow:

1. **Inspect.** Paste a reference — `docker.io/library/nginx:1.29`,
   `ghcr.io/org/chart:2.0.1` — and the instance contacts the registry
   through the same shared transport as everything else (proxy, private
   CAs, credentials file, allowlist). One inspection is bounded by
   `import.inspectTimeout` (default 20s); a deadline hit answers with
   the dedicated `TBY-REG-004` code, distinct from "unreachable".
2. **Select and import.** For a multi-platform image, each platform is a
   row with its digest and size, and a per-row status telling you
   whether the store already holds it. The button states what it will
   do — "Import (2 platforms, ~180 MB)" — before you press it.

![Unit import, step 2: inspection result with one row per platform, digests, sizes, status against the store, and the sized import button](../../../../assets/docs/import-step2.png)

The screen is addressable: `/import?ref=…` pre-fills the form and
triggers the inspection on load, which is also how "retry" on a failed
import task and deep links from a manifest detail work. The POST never
trusts what the browser displayed: the server re-inspects and pins the
digests the registry serves *at import time* (FR-026).

## From the API

The UI is a strict mirror of the API (FR-061):

```sh
# Inspect: platforms, digests, sizes, local status
curl -su "$USER:$PASS" \
  "https://tobby.zone.example/api/v1/import/inspect?ref=docker.io/library/nginx:1.29"

# Import (operator role): returns the tracked task. Platforms are named
# with the digest the inspection reported — the pin travels with the request.
curl -su "$USER:$PASS" -X POST \
  -H 'Content-Type: application/json' \
  -d '{"reference":"docker.io/library/nginx:1.29",
       "platforms":[{"name":"linux/amd64","digest":"sha256:…"}]}' \
  "https://tobby.zone.example/api/v1/import"
```

The import runs as a tracked task — progress, per-item outcome, and
downloadable log, like any synchronization (see
[operate](../../passthrough/operate/#tasks-are-the-unit-of-observation)).
The result is pullable from the embedded registry by tag and digest,
under the same relocated path a recipe would have used.

## Incremental by digest

Imports are differential, at two grains. Blobs and manifests already in
the store are never re-fetched (FR-028) — re-importing a moved tag
transfers only what actually changed. And the inspection reports
per-platform status against the store, so importing `linux/arm64` of an
image whose `linux/amd64` you already hold moves only the arm64 layers.
When everything is already up to date, the screen says so and gives you
the pull command instead of a button that would do nothing.

## When to prefer a recipe

A one-off import answers "get me this, now". A recipe answers "keep the
zone at this level" — and everything in the
[continuous promotion loop](../../passthrough/overview/#continuous-promotion)
keys off recipes, not imports:

| | One-off import | Recipe |
| --- | --- | --- |
| Reconciled at every cycle | no — imported once | yes |
| Version constraints (`~`, `^`, `12.x`) | no — one reference | yes |
| Pushed to the destination registry | no — store only | yes |
| Listed in the zone's cookbook | no | yes (FR-034) |
| Signature required by default | no — see below | yes (FR-033) |

If the same reference keeps coming back through the import screen, that
is a recipe asking to be written —
[write and publish one](../../recipes/write-and-publish/).

## What a one-off import means for provenance

Be clear-eyed about what you are attesting. A recipe arrives signed, and
its signature — verified against your trust roots — covers the exact
digests of every ingredient: someone accountable stated *this content,
at these digests, belongs in this zone*. A one-off import carries no
such statement. What you get is integrity, not endorsement: digests are
pinned at inspection and verified end to end, the registry allowlist
still applies (FR-030), and the action is attributed to the operator who
clicked it in the audit trail (FR-094). Nobody signed the *choice*.

Tobby keeps the two provenances distinct in its records. Unit-imported
content does not participate in promotion, and it is the only content an
administrator can remove repository by repository — from the repository
page or with `DELETE /api/v1/content/{repo}` — with the removal audited
(FR-044); recipe-managed content shows the action disabled, naming its
managing recipe. In mirror mode, unit imports are excluded from the
default prune, which touches recipe-managed content only (FR-045). The
full model is [content trust](../../security/content-trust/).

Last stop of the section: [operating in the long run](../../passthrough/operate/).
