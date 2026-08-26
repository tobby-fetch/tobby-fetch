---
title: Connected zones — overview
description: How a passthrough instance keeps a zone registry at the level its Retriever asks for, and the order to work through this section.
sidebar:
  order: 1
---

A passthrough instance is Tobby's delivered use case (milestone 4,
train v0.4.x): a long-lived service standing between two connected
network zones. On one side it reads from source registries — directly,
or through the enterprise proxy. On the other side it pushes into the
zone's registry, the one your clusters and hosts actually pull from.
In between sits its own store and an embedded OCI registry, so the zone
can also consume content straight from Tobby itself.

<svg viewBox="0 0 640 230" role="img" aria-label="Source registries feed a Tobby passthrough instance whose Retriever drives each cycle — re-read, reconcile, differential push — into the zone registry, which zone clients pull from" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
  <defs>
    <marker id="po-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--sl-color-gray-3)" />
    </marker>
  </defs>
  <!-- retriever -->
  <rect x="245" y="16" width="130" height="36" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="310" y="31" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Retriever</text>
  <text x="310" y="44" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">desired state of the zone</text>
  <line x1="310" y1="52" x2="310" y2="82" stroke="var(--sl-color-gray-3)" marker-end="url(#po-arrow)" />
  <text x="320" y="70" font-size="9.5" fill="var(--sl-color-gray-3)">re-read each cycle</text>
  <!-- sources -->
  <rect x="16" y="93" width="130" height="44" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="81" y="112" text-anchor="middle" font-size="12" fill="var(--sl-color-gray-1)">Source registries</text>
  <text x="81" y="126" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">direct or via proxy</text>
  <!-- tobby -->
  <rect x="230" y="88" width="160" height="54" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="310" y="109" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Tobby — passthrough</text>
  <text x="310" y="124" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">store + embedded registry</text>
  <!-- loop -->
  <path d="M 288 146 C 272 176, 348 176, 334 148" fill="none" stroke="var(--sl-color-gray-3)" marker-end="url(#po-arrow)" />
  <text x="310" y="192" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">each cycle: re-read → reconcile → push</text>
  <!-- zone -->
  <rect x="460" y="64" width="172" height="156" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <text x="546" y="54" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Zone</text>
  <rect x="476" y="96" width="140" height="38" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="546" y="119" text-anchor="middle" font-size="12" fill="var(--sl-color-gray-1)">Zone registry</text>
  <rect x="476" y="160" width="140" height="44" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="546" y="179" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Zone clients</text>
  <text x="546" y="193" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">docker · containerd · helm</text>
  <!-- flows -->
  <line x1="146" y1="115" x2="226" y2="115" stroke="var(--sl-color-gray-3)" marker-end="url(#po-arrow)" />
  <text x="186" y="107" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">fetch + verify</text>
  <line x1="390" y1="115" x2="472" y2="115" stroke="var(--sl-color-gray-3)" marker-end="url(#po-arrow)" />
  <text x="431" y="107" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">differential push</text>
  <text x="431" y="129" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">recipes included</text>
  <line x1="546" y1="134" x2="546" y2="156" stroke="var(--sl-color-gray-3)" marker-end="url(#po-arrow)" />
  <text x="556" y="149" font-size="9.5" fill="var(--sl-color-gray-3)">pull</text>
</svg>

## Continuous promotion

The loop is deliberately simple, and each step is a requirement you can
audit:

1. **Re-read the Retriever.** At every cycle the instance re-fetches the
   desired-state document from `retriever.source` (FR-010) and resolves
   every listed recipe from the cookbook, honouring version constraints
   (`~`, `^`, `12.x`) at each pass — a patch release lands by being
   published, with no file to edit.
2. **Reconcile.** The destination registry is compared against what the
   recipes pin. Only what is missing moves.
3. **Push differentially.** Blobs and manifests already present at the
   destination are never re-sent (FR-028). A second cycle over unchanged
   content transfers zero bytes — the crucible plays exactly that
   scenario.
4. **Re-verify before every push.** Signatures are checked against the
   local copy before each push, not once at import (FR-033). A store
   that was tampered with between cycles does not propagate.
5. **Propagate the recipes.** The signed recipe artifacts themselves are
   pushed to the zone's cookbook alongside their ingredients (FR-034),
   so the zone's cookbook always reflects what the zone actually holds —
   and a further downstream zone can chain from it.

The cycle runs every `sync.interval` (default 15m). The interval is
changeable at runtime from the administration screen and the API without
redeploying (FR-013), and that change is audited as sensitive
configuration (FR-094). A synchronization can also be triggered by hand,
from the recipes screen or with `POST /api/v1/sync`.

Ingredients land at the destination under their nominal source host —
`docker.io/library/nginx` becomes
`registry.zone.example/docker.io/library/nginx` — with digests and
signatures unchanged. Tobby never rewrites and never re-signs content;
the naming rule and its consequences for your clients are the subject of
[Connect your clients](../../passthrough/connect-clients/).

## Prerequisites

- A Linux host or a Kubernetes cluster for the instance — see the
  [OS matrix](../../passthrough/deploy/#operating-system-matrix).
- Two directories on separate volumes: the **store** (large,
  refetchable) and the **state** (small, the backup target). They must
  not be nested in one another; Tobby refuses to start otherwise.
- Network reach to the source registries, directly or through a forward
  proxy, and to the destination registry.
- A destination registry that accepts nested repository paths — Tobby
  probes for this before pushing and fails explicitly when it does not
  (FR-035).
- A Retriever document and signed recipes in a cookbook. To understand
  those first, read [recipes, cookbook, retriever](../../recipes/understand/).

## Working through this section

The pages are ordered the way a deployment actually proceeds:

1. **[Deploy](../../passthrough/deploy/)** — Kubernetes (Helm chart or raw
   manifests), VM packages, the container image, and the first account.
2. **[Enterprise network](../../passthrough/network/)** — authenticated
   proxy, private certificate authorities, and the TLS the instance
   itself serves.
3. **[Zone Retriever and cascade](../../passthrough/retriever-cascade/)** —
   the desired-state document, and how zones chain without touching
   recipes.
4. **[Connect your clients](../../passthrough/connect-clients/)** — why the
   paths look the way they do, containerd mirrors, GitOps pitfalls, and
   the OS-package endpoint.
5. **[One-off imports](../../passthrough/one-off-import/)** — pulling a
   single image or chart in by reference, outside any recipe.
6. **[Operate over time](../../passthrough/operate/)** — probes, task
   tracking and resume, backup, growth, upgrades.

If you have not run Tobby at all yet, the ten-minute
[install and start](../../try/install-and-start/) path and the
[first promotion](../../try/first-promotion/) walkthrough are the faster
introduction; this section assumes you are past them and heading to
production.
