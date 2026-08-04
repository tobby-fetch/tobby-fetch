# ADR-0006 — Removable-media transport: self-contained store + OCI image layout export

## Status

Accepted — 2026-07-11

## Context

In **mirror** mode, Tobby synchronizes the recipes selected by a zone's
Retriever into a local store on a transportable workstation or removable
medium. The medium then physically crosses the air gap — typically via the
site's media decontamination station — and a Tobby instance on the
destination side pushes the content into the air-gapped zone's registry.

The question this ADR answers: **what exactly travels on the medium?** The
answer determines whether the destination side can operate autonomously,
whether transfers can be audited, and whether third-party tools can read the
payload in an emergency.

Constraints:

- The air-gapped side has **nothing** but what arrives on the medium: the
  payload must carry artifacts, the recipes that justify them, and the
  operation logs — the medium is also the log channel back out.
- Media pass through organizational controls (decontamination, integrity
  review); the payload must be verifiable without network access.
- Interoperability is a safety net: if Tobby is unavailable on one side,
  standard tooling should still be able to extract the artifacts.

## Decision

**The unit of transport is the complete Tobby storage directory.** The
directory is self-contained and self-describing:

```text
<store>/
├── registry/        # OCI content-addressed store (blobs, manifests, tags)
│                    #   images, charts, artifacts, filesets — and the
│                    #   recipe artifacts themselves (ADR-0002)
├── logs/            # structured JSON operation logs (mirror mode writes
│                    #   its logs onto the medium; location configurable)
├── meta/            # store metadata: format version, sync state,
│                    #   media manifest (see integrity below)
```

A destination-side Tobby instance points at the transported directory and is
immediately operational: it can serve the store, verify it, and push to the
zone registry. No import step is required in the nominal flow — moving the
directory *is* the transfer.

**Optionally**, Tobby can export to and import from the standard **OCI image
layout** (as a directory or single tar), for interoperability with `skopeo`,
`oras`, and `crane`:

```bash
tobby export --format oci-layout --output /media/usb/payload.tar   # outbound
tobby import --format oci-layout /media/usb/payload.tar            # inbound
```

### Integrity

At sync completion (after any store pruning), Tobby writes a **media manifest**
into `meta/`: an inventory of every file with its checksum, the recipes the
payload fulfills with their digests, the zone identity (the served Retriever's
name), and the store format version. The manifest is an integrity and
completeness *aid* — it is deliberately **unsigned**. Tobby transports content;
it is not a provenance authority, and a signing key held by the transport tool
would add no security: an attacker able to alter the store could re-sign with
it.

Authenticity rests exclusively on the cosign signatures of the recipes and
ingredients themselves (ADR-0007), verified on the destination side against
*that environment's* configured trust roots: content signed for the target
environment is accepted; everything else — including content present on the
medium but not reachable from a verified recipe — is not pushed, and is
reported. Completeness is independently derivable from the signed recipes
(every pinned ingredient digest must be present and correct), so tampering with
the manifest cannot hide missing or extraneous content from verification; the
manifest's role is to make failures fast and precisely localized (which file,
which recipe), and its zone identity field is an anti-accident guard against
plugging the wrong medium into the wrong zone.

Verification runs on arrival, **before** anything is pushed, served, or written
locally; destination-side writes (the return logs) land in a dedicated path
outside manifest coverage so the outbound payload's inventory stays checkable
on the way back.

### Encryption — delegated, out of application scope

Encryption of the medium at rest is **delegated to the operating system**
(LUKS on Linux, BitLocker on Windows, or the site's approved equivalent).
Tobby does not implement volume or file encryption: doing so would duplicate
mature, certified OS mechanisms, put key management inside an application
that crosses trust boundaries, and still not satisfy sites that mandate
specific approved tooling. The operations documentation covers the
recommended setups explicitly.

### Decontamination station — assumed, not implemented

Industrial sites commonly require removable media to pass through a
decontamination/inspection station before entering a protected zone. Tobby
treats this as a generic operational step in the documented workflow: the
payload is designed to survive it (plain files, verifiable checksums, no
exotic filesystem features) and to be re-verified after it.

## Consequences

- The air-gapped side is genuinely autonomous: artifacts, the recipes that
  authorize them, verification material, and logs arrive together. Audit
  trails travel with the data, and the returning medium carries the
  destination-side logs.
- "Transfer" has no bespoke packing/unpacking step in the nominal flow —
  fewer moving parts, and the store format (already required by ADR-0004/0005)
  is the only format to get right. Its version is recorded in `meta/` so
  mixed-version workstations fail loudly instead of misreading.
- The optional OCI image layout path guarantees an exit ramp: any OCI tooling
  can extract artifacts from an export, and third-party-produced layouts can
  be imported. The layout export carries *artifacts only* semantics-wise —
  logs and sync state are Tobby-store concepts — which is exactly why it is
  the interop format and not the primary one.
- The media manifest adds a required verification step on both sides; this is
  deliberate friction and is surfaced in the UI/CLI as a first-class check,
  not an option.
- Delegating encryption means Tobby documentation must be prescriptive about
  it, and deployments are trusted to follow it; this matches how sites
  actually govern media handling (centrally, per policy, per approved tool).

## Alternatives considered

### Bespoke archive format

A custom `.tobby` archive (artifacts + recipes + logs + manifest in one
sealed file) would allow tight control of integrity and atomicity. Rejected:
it is opaque to every existing tool, creating a single point of failure — if
Tobby cannot run, the payload is unreadable — and it reinvents packaging,
indexing, and partial-update semantics the OCI store already provides. The
media manifest achieves the integrity goal without the opacity.

### OCI image layout as the *primary* store and transport

Using the standard layout directly for storage would make the store natively
readable by all OCI tooling with no export step. Rejected as primary: the
image layout is a distribution snapshot format, not an operational store — it
has no place for logs, sync state, task metadata, or the media manifest, and
the embedded distribution registry (ADR-0004) does not serve from it.
Retained where it shines: as the optional import/export interop format.

### Per-artifact files via `skopeo dir` / individual tars

Exporting each ingredient as its own archive keeps items independently
copyable and is a common ad-hoc air-gap practice. Rejected: blob
deduplication across images is lost (media capacity is a real constraint),
integrity and completeness become a per-file bookkeeping exercise, and
reassembling recipe-level consistency on the destination side reintroduces
exactly the error class the recipe/cookbook model eliminates.
