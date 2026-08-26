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

The four crucible reports currently in the repository are the output of a
**full replay of every milestone** run on 2026-08-22 for the v0.4.2
re-acceptance (commit `377dfa6`), on a disposable bare-metal host created
and destroyed within the same half hour — the hardening release was
replayed rather than reasoned about.

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
