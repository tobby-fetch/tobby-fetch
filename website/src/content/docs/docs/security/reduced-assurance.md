---
title: The reduced-assurance marker
description: How content admitted under a relaxed trust scope stays visible — in task reports, the API, the interface, logs, metrics and the media manifest — and why the destination re-reads the marker as a claim, never as a verdict.
sidebar:
  order: 7
---

A [relaxation scope](../content-trust/#relaxation-scopes--and-their-visibility)
is visible as a *setting*: a banner, the effective configuration. That is not
enough. What it let in has to stay visible too, item by item, long after the
synchronization that admitted it — or the day someone asks "which of these
recipes was never signed?".

The **reduced-assurance marker** (SRS FR-037) is that visibility, extended
from configuration to content. The principle is the one of the secure-by-default
rule (FR-075): what was admitted with less assurance is **never presented as
if it had full assurance**.

## What carries the marker

A recipe admitted **unsigned** by a declared scope, and every item it reaches,
carries a marker with three parts: the level (`reduced`), the name of the scope
that admitted it, and the reason (`unsigned`). A recipe whose signature was
verified against a trust root carries none — absence means full assurance.

| Surface | How the marker appears |
|---|---|
| **Task report** | The `assurance` member of every item of the recipe: its artifact, its ingredients, its promotion halves, its media verdict. Served as-is by `GET /api/v1/tasks/{id}`. |
| **API** | `assurance` on each recorded recipe (`GET /api/v1/recipes`) and on every task item. |
| **Interface** | A *Reduced assurance* badge, with the scope in its hover text, on the Recipes screen, on the items of the task detail, and on the Media screen. |
| **Logs** | The `assurance` field (value `reduced`) and `assurance_scope` on the records that concern the recipe — and **only** those: filter on the field's presence to find them. See [metrics and logs](../../reference/metrics-logs/). |
| **Metrics** | `tobby_assurance_reduced_recipes{scope}`: how many recipes are currently held under reduced assurance, per declared scope — from the first scrape, at zero while a scope holds nothing. |
| **Media manifest** | The marker, per recipe, in `meta/media.json`. Omitted — not `null` — for a recipe of full assurance, so a medium without reduced content stays readable by a build that predates the field. |
| **Store verification** | [`tobby store verify`](../../passthrough/verify-store/) re-reports the marker for each recorded recipe, as this instance's trust configuration sees it **now**. |

## Across the air gap: a claim, never a verdict

The marker in a media manifest is a **claim of the source zone**. The manifest
is unsigned and Tobby signs nothing (ADR-0006, ADR-0007), so on the destination
side it is data to display, not something to rely on. The rule is symmetrical:

- The destination computes **its own** marker from **its own** trust verdict —
  its own trust roots and its own scopes. A recipe it admits only because one
  of *its* scopes allows unsigned recipes carries *its* marker, naming *its*
  scope.
- The manifest's marker is re-displayed **beside** it, labeled as declared by
  the source zone, and never instead of it. The media verification report
  carries both (`assurance` and `sourceAssurance`), and so do the Media screen
  and `tobby media verify`.
- **A manifest that claims nothing hides nothing.** A medium produced by an
  older build, or one whose marker was removed by hand, still has every recipe
  the destination admits through a relaxed scope marked from the destination's
  own verdict.
- A recipe the destination **blocks** still shows the source's claim, so an
  operator can see why the two zones disagree.
- A marker in a manifest is held to the shape this build writes — reduced, a
  named scope, a known reason, bounded in size. A manifest carrying anything
  else is internally inconsistent and is refused like any other malformed
  manifest.

## Nothing is written into the destination registry

The marker lives in Tobby's own surfaces — reports, the task record, the
manifest, the logs. It is **never** written into a registry: no annotation, no
extra tag, no sidecar artifact. Transferred content stays bit-exact
(RECIPE-SPEC §7.1) — the zone registry holds the same digests the source held,
and a standard client pulling from it sees nothing that Tobby added.

## What the marker is not

- It is not a second kind of trust. It marks content that was admitted by a
  relaxation you declared; it does not make that content more or less
  trustworthy, and it does not block anything.
- It does not cover a scope that only narrows trust. A scope restricting
  `roots` relaxes nothing and marks nothing; a refusal inside it names the
  scope instead ([`TBY-SIG-001`](../../reference/errors/#tby-sig-001)).
- It is derived, not stored on its own authority: the recipe graph keeps the
  admission facts (verified or not, which scope), and the marker is computed
  from them whenever it is read — which is why a store written by an earlier
  release carries the markers too.
