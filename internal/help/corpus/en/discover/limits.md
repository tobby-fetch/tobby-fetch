---
title: Limits and out-of-scope
description: What Tobby deliberately does not do, what it does not do yet, and the operational consequence and justification of each limit.
sidebar:
  order: 5
---

Honesty is a feature of this documentation: every limit below comes with its
operational consequence and its justification. Some are permanent design
decisions ([SRS §5](../../reference/srs-adr/) is the normative source); others
are simply not delivered yet and carry a milestone badge — the
[project status](../../discover/status/) page tracks them.

## Permanent design decisions

### Tobby signs nothing

Tobby holds no private key and produces no signature — not on content, not
on recipes, not on its media manifest or audit log.
**Consequence:** your organization owns the whole signing chain. Keys,
rotation, and the signing step in your qualification pipeline are your
responsibility, outside Tobby.
**Justification:** a transport tool that signs becomes a trust anchor, and a
compromise of that tool would forge trust. Authenticity comes exclusively
from your cosign signatures, verified against
[the destination's trust roots](../../security/content-trust/).

### The media manifest is not signed

The inventory a transportable store carries is an integrity aid — file list,
checksums, zone identity, run ID — not a trust anchor.
**Consequence:** the manifest alone proves nothing. What proves authenticity
on arrival is the verification of every recipe signature against the trust
roots configured on the destination instance; trust roots found on the media
are ignored.
**Justification:** signing the manifest would reintroduce the trust-anchor
problem above, for no security gain. The full reasoning is on
[the media security model](../../air-gap/media-security/) page.

### No destination purge

Tobby never removes content from a destination registry (SRS §5.1).
**Consequence:** obsolete content accumulates in zone registries until you
remove it. The intended pattern is a downstream mark-and-sweep over the
zone's cookbook: enumerate every ingredient referenced by every recipe,
delete everything else.
**Justification:** deleting inside a zone is an authority Tobby should not
hold. Its obligation is not to obstruct: it exposes complete standard
listings and propagates the recipes alongside the artifacts, so the
reference set for a sweep is always available in-zone.

### No upstream qualification

Tobby does not build, test, scan-for-qualification, sign, or select
software, and does not orchestrate CI/CD (SRS §5.3).
**Consequence:** a signed recipe containing a bad choice will be transported
faithfully. Garbage in, verified garbage out.
**Justification:** qualification is your pipeline's job. The verification
Tobby performs — signatures, digests, allowlist, and scanning from
milestone 6 — is a transport safeguard, not a qualification step.

### No generic file-upload server

Serving arbitrary uploaded files is excluded (SRS §5.2).
**Consequence:** you cannot drop loose files onto an instance over HTTP.
Files travel as FileSet ingredients — packaged, digest-pinned, signed — and
verified FileSets are served read-only under `/files/`, which is sufficient
for apt/rpm repositories and bare-host bootstrap.
**Justification:** an ad-hoc upload surface would bypass the recipe model
and its entire verification chain.
The one way local files enter a store is
[`tobby fileset pack`](../../reference/cli/#tobby-fileset-pack), which reads
a directory **of the instance host** — never a request body — is restricted
to administrators, is confined to the directories `files.packRoots` names,
and records what it produced as an unsigned manual import of local origin.

### One mode per instance, fixed at startup

An instance runs in exactly one mode; changing it requires a restart.
**Consequence:** covering both use cases means running two instances.
**Justification:** changing mode re-purposes the instance — its store,
its posture, its trigger model. Making that a runtime toggle would invite
accidents; the UI displays the mode read-only instead.

### Mirror synchronization is manual, always

In mirror mode there is no scheduler: synchronization is a button or an API
call, never unattended.
**Consequence:** air-gap transfers happen when an operator decides, not on
a timer.
**Justification:** preparing removable media is a supervised physical
procedure; an unattended job writing to a medium nobody is watching is a
risk, not a convenience.

## Platform and deployment limits

### OS support matrix

Production scope is deliberately narrow (NFR-018):

| OS | Support |
|---|---|
| Linux (amd64/arm64) | Full: service and workstation, packages and container image |
| Windows | Mirror workstation journey, validated in CI; passthrough is outside the validated scope |
| macOS | Convenience tier: same reproducible build, no validated production scenario |

**Consequence:** run production instances on Linux; use macOS builds for
evaluation and authoring only. The per-capability detail is on
[supported platforms](../../reference/platforms/).

**Windows installation channels are not published.** The winget and Scoop
manifests are rendered from each release's own checksums and attached to it,
but neither destination index carries Tobby yet: submitting to
`microsoft/winget-pkgs` is a reviewed human step and the Scoop bucket does
not exist. **Consequence:** install the Windows workstation from the release
archive, verified as described in
[verify a release](../../project/verify-a-release/).

### FAT32 media are refused

The pre-flight check refuses a transport medium with an incompatible
filesystem, FAT32 explicitly among them, naming the limit in the error.
**Consequence:** format transport media with a filesystem that can hold
large files (exFAT, ext4, NTFS).
**Justification:** FAT32's 4 GiB file-size ceiling would truncate large
blobs mid-transfer; refusing up front beats a corrupted store at the
destination. A filesystem this build knows no ceiling for is reported as
**unidentified**, never as capable: the pre-flight check warns and lets the
run proceed rather than claiming a guarantee it does not have.

### Single replica

The reference deployment runs one replica with a `Recreate` strategy; there
is no high-availability topology.
**Consequence:** a Tobby upgrade means a short unavailability window of the
instance, including its embedded registry.
**Justification:** the store requires an exclusive writer (safe garbage
collection, task queue, consistent state). Tobby is a promotion path, not
your zone's serving registry of record — and if it temporarily is (the
stand-in use case), schedule upgrades accordingly.

## Not yet — known operational gaps

- **Transit-store cleanup is off by default in passthrough.** Retriever-aligned
  pruning exists (R-33) but `sync.prune` is `false` unless you set it: a
  transit store is not a delivery unit, and refreshing it does not imply
  shrinking it. Set `storage.occupancyThreshold` so the instance tells you
  when the store outgrows its volume — unset means unmonitored, not "within
  limits".
- **No on-demand integrity check.** Full store verification with a
  timestamped report arrives with R-31 (milestone 6).
- **No vulnerability scanning yet.** Scanning with a blocking or advisory
  policy is milestone 6 (6.1); until then, scanning belongs to your
  upstream qualification pipeline.
- **Backup procedure not yet formalized.** The state directory is the
  backup target, but the tested end-to-end rebuild procedure is R-27
  (milestone 7). See [operating in the long run](../../passthrough/operate/).

Each of these is tracked, with its milestone, on the
[project status](../../discover/status/) page — and the
[threat model](../../security/threat-model/) states which security controls
exist today versus which are scheduled.
