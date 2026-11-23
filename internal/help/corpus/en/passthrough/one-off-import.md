---
title: One-off imports
description: Import a single image, chart, or OCI artifact by reference — inspect first, pick platforms, transfer only what is missing.
sidebar:
  order: 7
---

Not everything deserves a recipe. A base image to try, a chart you are
evaluating, one artifact a developer asked for today — Tobby imports a
single OCI artifact of any media type by reference, outside any recipe
run and without a container runtime on the instance. It has been part of every release since milestone 2.

## From the interface

The **Import** screen (`/import`, operator role) is a two-step,
inspect-before-import flow:

1. **Inspect.** Paste a reference — `docker.io/library/nginx:1.30`,
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
digests the registry serves *at import time*.

## From the API

The UI is a strict mirror of the API:

```sh
# Inspect: platforms, digests, sizes, local status
curl -su "$USER:$PASS" \
  "https://tobby.zone.example/api/v1/import/inspect?ref=docker.io/library/nginx:1.30"

# Import (operator role): returns the tracked task. Platforms are named
# with the digest the inspection reported — the pin travels with the request.
curl -su "$USER:$PASS" -X POST \
  -H 'Content-Type: application/json' \
  -d '{"reference":"docker.io/library/nginx:1.30",
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
the store are never re-fetched — re-importing a moved tag
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
| Listed in the zone's cookbook | no | yes |
| Signature required by default | no — see below | yes |

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
still applies, and the action is attributed to the operator who
clicked it in the audit trail. Nobody signed the *choice*.

Tobby keeps the two provenances distinct in its records. Unit-imported
content does not participate in promotion, and it is the only content an
administrator can remove repository by repository — from the repository
page or with `DELETE /api/v1/content/{repo}` — with the removal audited; recipe-managed content shows the action disabled, naming its
managing recipe. In mirror mode, unit imports are excluded from the
default prune, which touches recipe-managed content only. The
full model is [content trust](../../security/content-trust/).

The content browser and the API say it too: an imported repository shows
the provenance class `unit-import` — a FileSet packed on this instance
shows as *Packed on this instance*, class `unit-import` with the origin
`local-pack`, and content restored from an OCI image layout (`tobby import`,
`/admin/oci-layout`) as *Imported from an OCI layout*, origin `oci-layout` —
and it counts in the
`tobby_store_unmanaged_repositories{class="unit-import"}` metric. Importing into a repository that already holds content pushed
through `/v2/` leaves it classed `seed`: the class never becomes easier to
remove than what the repository already held.

## When the instance refuses imports

An instance hardened with `hardening.disableUnitImport` accepts content
through verified recipes only. The import screen then shows its form
disabled, naming the setting; `POST /api/v1/import`, its inspection,
FileSet packing — the `/filesets` form, `POST /api/v1/filesets/pack` and
`tobby fileset pack`, which reads the same configuration file — and the OCI
image layout import — the `/admin/oci-layout` form,
`POST /api/v1/oci-layout/import` and `tobby import` — answer
[TBY-POL-005](../../reference/errors/#tby-pol-005), and each refusal is a
`hardening.refused` audit event. That is a decision of the instance's
administrator, not a fault: deliver the content through a signed recipe,
or ask for the setting to be removed. See the
[configuration reference](../../reference/configuration/#hardening).

Next: once content is in, keep the instance healthy —
[operating in the long run](../../passthrough/operate/).
