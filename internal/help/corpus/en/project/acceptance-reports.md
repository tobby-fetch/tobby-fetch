---
title: Acceptance reports
description: The raw crucible reports of every delivered milestone, unedited, and the defects the net caught before publication.
sidebar:
  order: 3
---

Every milestone's definition of done includes a raw acceptance report:
the crucible scenario replayed on a real disposable node, published
unedited — every check listed, one line per claim, digests included. The
reports live in
[`docs/acceptance/`](https://github.com/tobby-fetch/tobby-fetch/tree/main/docs/acceptance)
in the repository. This page indexes them.

The five crucible reports in the repository are the output of a **full
replay of every milestone**, run on 2026-08-27 for the v0.5.0 acceptance
(commit `51112c3`), on a disposable bare-metal host created and destroyed
in the same session — every milestone was replayed rather than reasoned
about, which is what makes the cascade a property and not a memory. The
milestone-1 to milestone-4 files are that run reproducing them; the
previous full replay was 2026-08-22, for the v0.4.2 re-acceptance.


## The reports

### [milestone-1-crucible-report.txt](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-1-crucible-report.txt) — foundations

Connected node and air-gapped node on the Incus crucible, a loop-backed
block device as the medium. Checks: embedded-registry round-trip under a
relocated name, egress canary failing as required (the air gap proven by
construction), and the media round-trip — a store filled in the connected
zone, physically re-attached, serving digest-identical content across the
gap. All checks pass.

### [milestone-2-crucible-report.txt](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-2-crucible-report.txt) — secure user journey

Source registry plus a secure node. Checks: the refusal to start without
an account (exit 3, `TBY-AUTH-001`), anonymous rejected on the API
(RFC 9457) and the registry (Basic challenge), an import driven entirely
through `/api/v1` with the pinned digest preserved, a standard third-party
client pulling with credentials, and a task surviving a SIGKILL restart
with zero re-transfer. All checks pass.

### [milestone-2-topology-report.txt](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-2-topology-report.txt) — hermetic tier

The same journey on the hermetic CI topology (2026-08-12, developer
workstation), including idempotence: the second import moved zero bytes.
Its trailer documents the two-tier split — this containerized scenario
gates merges, the crucible run above gates the milestone.

### [milestone-3-crucible-report.txt](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-3-crucible-report.txt) — recipe engine

Real cosign key pairs generated on the node. Checks: a signed recipe
synchronized with pinned digests intact and a sparse index copied as such,
a foreign-signed recipe refused (`TBY-SIG-001`) with nothing landed, zero
bytes on the second synchronization, **both** cosign signature layouts
verifying (the Sigstore bundle of cosign 3.x and the classic tag), FileSet
serving with range requests and traversal refused, and a two-hop cascade
with the recipe unmodified. All checks pass.

### [milestone-4-crucible-report.txt](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-4-crucible-report.txt) — passthrough

The instance deployed from the reference Helm chart, a private PKI issued
by the scenario, an authenticated forward proxy built from the repository.
Checks: secure-by-default posture and its banner-carrying override, a full
promotion cycle with signatures re-verified before push, an idempotent
second cycle, the runtime interval change audited and surviving restart,
an off-list destination refused before any byte moved — and the same
promotion from a sealed node whose only route is the authenticated proxy,
over private-PKI TLS. All checks pass.

### [milestone-5-crucible-report.txt](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-5-crucible-report.txt) — mirror and air-gap

The whole physical transfer, on a real detachable block device across a
network gap that has no route rather than an unused one. Thirty-two checks:
an undersized medium refused before any transfer with the shortfall in
bytes and nothing written, a real FAT32 volume identified with its 4 GiB
per-file ceiling, no synchronization firing on its own in mirror mode, a
plan run leaving the store byte-identical, the prune running *before* the
manifest so the inventory describes what the medium finally holds, a
credentials file planted under the store stopping the instance dead — then
the trip. On the far side: nothing served before verification, a medium
addressed to another zone refused and its administrator waiver recorded,
and the arbitration this milestone exists for — one delivery blocked whole
and named with the file that failed, its intact neighbour pushed. Then the
cleared delivery pulled back inside the isolated zone by a standard OCI
client, the return log on the medium outside the manifest's coverage, a
medium rewound in time refused with both timestamps named, `skopeo` reading
the exported layout, the guides answering offline in both languages, a
directory packed inside the zone served under `/files/` with no write
method accepted, and a reset refused without its typed confirmation. All
checks pass.

One product defect was found here and nowhere else: `tobby sync` could not
trigger a serving instance, because it read the API's task envelope as a
bare task. Its own test suite was green — the fake instance in it answered
what the client expected instead of what the API sends.

### [milestone-4-quality-audit.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-4-quality-audit.md) — point-in-time audit

Run 2026-08-21 on v0.4.1, between milestone 4 and milestone 5: eight
review dimensions plus tooling, then an adversarial round where every
high-severity finding was handed to an independent reviewer instructed to
refute it. Four confirmed high-severity findings (two concurrency defects,
unbounded task history, a spec-side digest defect), each proven by
execution; all fixed in v0.4.2, the remainder explicitly deferred with
owners. The findings are published with the same candor as the green
reports.

## B-014 and B-015 — proof the net catches

The point of a two-tier proof system is what it finds. The milestone-4
header of the roadmap records it plainly: **two defects were found by the
crucible and corrected before publication.**

- **B-014** — trust scopes were matched against two different pattern
  spaces on the two halves of a promotion. On any registry carrying a
  port, a correctly written scope admitted a recipe at import, then
  refused it before the push with `TBY-SIG-001`. Invisible to unit tests:
  the two code paths were each tested separately, and without a port.
  Found by replaying the milestone-4 crucible scenario.
- **B-015** — a Sigstore-bundle signature (cosign 3.x's default) verified
  on the zone that fetched it and was gone one hop down: the verifier had
  learned both layouts at milestone 3, the *copy* path had only learned
  the old one, so a downstream zone refused content its upstream had
  accepted. Found by replaying the milestone-*3* crucible scenario during
  milestone-4 acceptance — the reason every delivered scenario stays
  runnable forever.

Neither defect was observable below the crucible tier: both needed real
registries, real signatures and a real multi-hop topology. Both fixes
shipped in v0.4.0 with regression tests that were first run against the
unfixed code, and the changelog names them with their root causes.

How the scenarios and reports are built is on
[tests and proofs](../tests-and-proofs/); how each release train relates
to a milestone is on [releases and compatibility](../release-compatibility/).
