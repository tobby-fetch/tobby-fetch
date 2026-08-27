---
title: Prepare the source workstation
description: Installing and configuring a mirror instance, what the pre-flight check refuses, how to plan a synchronization without running it, and what ends up on the medium.
sidebar:
  order: 2
---

The source side of a physical transfer is one binary on one workstation.
This page covers everything that happens on the connected side: installing
it, configuring it for mirror mode, checking that the transfer will fit,
rehearsing it, and running it.

The step names come from [the journey](../../air-gap/media-workflow/) —
prepare, pre-flight, export — and are the same ones the screens and the
error messages use.

## Install the workstation

A mirror instance is a single statically linked binary with no runtime
dependency. Install it the way your site installs software on a machine
that will touch a medium destined for an isolated zone:

- **Linux** — the release binary, or the `.deb`, `.rpm` or `.apk` package,
  all of which install with no remote repository configured.
- **Windows** — the release binary, `tobby-windows-amd64.exe` or
  `tobby-windows-arm64.exe`. There is no installer and no package-manager
  channel: the winget and Scoop manifests are built and attached to each
  release but neither index carries Tobby, so the archive is the way in.
  See [supported platforms](../../reference/platforms/).

Verify the release before you trust it — provenance, SBOM and checksums are
all independently checkable, and the procedure is on
[verify a release](../../project/verify-a-release/). Do it on a connected
machine, before the binary goes anywhere near the medium.

## Configure mirror mode

`tobby quickstart` asks the questions and writes the configuration file;
`--mode mirror` pre-answers the mode. It never overwrites an existing
configuration and it is not a requirement — everything it writes can be
written by hand.

The settings a mirror source needs:

```yaml
mode: mirror

storage:
  root: /media/usb/tobby-store     # the transportable store — the medium
state:
  root: /var/lib/tobby/state       # accounts, tokens, keys — the workstation

retriever:
  source: https://registry.example.com/zones/isolated/retriever.yaml

trust:
  roots:
    - name: qualification-2026
      keyFile: /etc/tobby/pki/cosign-qualification-2026.pub

registries:
  allowlist:
    - registry.upstream.example.com
```

Three of those deserve a sentence.

**`storage.root` is the medium.** There is no packing step: the store *is*
the transfer, a plain relocatable directory. Point it at the mount point of
the medium, or at a staging directory you copy to the medium afterwards —
the store does not care where it sits, which is what makes it transportable.

**`state.root` is not.** Accounts, tokens, the TLS key, the resume spool and
the per-zone import record live there, on the workstation, and never travel.
This is enforced, not merely advised: an instance **refuses to start**
(`TBY-CFG-002`) when `state.root`, `registries.credentialsFile` or
`server.tls.keyFile` resolves inside the store — through symbolic links,
`..` and relative spellings included. See
[secrets](../../security/secrets/).

**`zone:` is not set on the source.** A source instance learns which zone it
serves from the Retriever it resolves. Only a *destination* instance — which
has no Retriever, because its content arrives on a medium — is told its zone
identity by configuration. That difference is also how the destination-side
serving guard tells the two sides apart, so setting `zone:` on a source
workstation changes its behaviour and is not a harmless extra.

### Synchronization is manual, always

A mirror instance has no scheduler. `sync.interval` and `sync.prune` are
passthrough-only settings and are refused in mirror mode. Preparing a medium
is triggered from the interface, from `POST /api/v1/sync`, or from
[`tobby sync`](../../reference/cli/#tobby-sync) against the running instance
— always by a person.

This is a design position and not a missing feature: an unattended process
must not decide what crosses an air gap, and a medium being written while
nobody is watching is a medium nobody can vouch for.

## Pre-flight: does it fit, and will it pass?

Before any transfer starts, Tobby computes what would travel and compares it
with what the target can hold. Two refusals and two warnings come out of it
(FR-055).

**Volume against space.** The bytes to transfer are computed per recipe from
the source manifests, deduplicated by digest and net of what the store
already holds. The projection is compared with the target's free space minus
a safety margin — `preflight.safetyMarginPercent`, **10 % by default** —
and the synchronization is refused before a single byte moves when it does
not fit, stating the shortfall in bytes
([`TBY-STO-004`](../../reference/errors/#tby-sto-004)). The margin exists
because the store is never the only writer on its volume.

**Filesystem capability.** A filesystem positively identified as unable to
hold the largest single file the run would write is refused by name
([`TBY-STO-005`](../../reference/errors/#tby-sto-005)). FAT32 and its 4 GiB
per-file ceiling is the canonical case, and a single-tar export archive is
one file. Identification is per platform and deliberately narrow: `statfs`
on Linux and macOS, `GetVolumeInformationW` on Windows.

**A filesystem this build knows no ceiling for is reported as
unidentified, never as capable.** That is a warning, not a refusal: the run
proceeds and the report says the guarantee was not available. The same holds
when free space cannot be read.

**A "file too large" error arriving mid-write fails cleanly**, store intact.
The pre-flight check is a courtesy that turns a corrupted transfer into an
early refusal; it is not the only thing standing between you and a truncated
blob.

`preflight.disabled: true` turns the gate into a report: the volumes and the
filesystem verdicts are still computed and still shown, and they no longer
refuse anything. It is an explicit, announced removal of a safety check —
logged at startup and again every time it lets a refusal through — and the
verdict keeps its refusal code, so a disabled gate can never be mistaken for
a passed one.

## Plan mode: the whole run, without the run

`tobby sync --dry-run` reports everything a synchronization would do and
does none of it:

```sh
tobby sync --dry-run --storage-root /media/usb/tobby-store
tobby sync --dry-run --retriever ./candidate-retriever.yaml --output json
```

The report carries the resolved version of every recipe, the per-digest
status of every ingredient against the store, the deduplicated volume to
transfer, the projected store size against the target's free space and
filesystem capability, the content a prune would remove, and the policy
verdicts that need no transfer — the registry allow-list and the recipes'
own signatures.

Nothing is written, nothing is pushed, and a passthrough instance's
reconciliation cadence is left exactly where it was. The guarantee is
structural — the planner holds a read-only view of the store and no
scheduler — and a test fingerprints the whole store tree before and after a
plan and fails on any difference.

Exit codes make it a gate rather than a report to read by eye:

| Exit | Meaning |
|---|---|
| `0` | nothing to do |
| `5` | changes planned |
| `3` | refused by policy (a registry outside the allow-list) |
| `4` | verification failed (a recipe signature no trust root validates) |
| `1` | the plan could not complete |

The same report is on `POST /api/v1/plan` and on the `/recipes/plan` screen,
where a **candidate** Retriever can be planned instead of the configured one
— a file, a URL, an OCI reference, or a document pasted in. That is how you
review a Retriever change before adopting it.

`--dry-run` refuses the flags that only make sense against a running
instance (`--wait`, `--instance`, `--token-file`, …) as a usage error rather
than ignoring them: a pipeline that wrote `tobby sync --dry-run --wait` is
told it waited for nothing.

<!-- TODO: screenshot: the plan screen — a candidate Retriever planned against the store, per-recipe volumes and the space verdict -->

## Export: filling the medium

Trigger the synchronization. Tobby resolves the Retriever, downloads what is
missing, verifies signatures and digests on the way in, and writes
everything into the store.

### What the store looks like

The transportable store is a plain directory, self-contained and
self-describing:

```text
<store>/
├── docker/registry/v2/   # the OCI content-addressed store: images, charts,
│                         # artifacts, filesets — and the recipes themselves
├── meta/                 # bookkeeping: store format version, the recipe
│                         # graph, provenance ledger, and media.json
└── _tobby/               # Tobby's own area: the task queue and the
                          # operation logs, on both sides of the trip
```

Two of those three are covered by the media manifest: `docker/registry/v2/`
and `meta/`. `_tobby/` is not, by construction — it keeps being written
after the inventory is taken, and a file that is still growing cannot be
inventoried without invalidating the inventory on its next line.

### The media manifest, written last

The last thing a mirror synchronization does — **after any prune** — is
write `meta/media.json`: the store format version, the medium's identifier,
the zone identity, the producing version and run, the Retriever resolution
timestamp, the recipes fulfilled with their pinned digests, and a
file-by-file inventory of path, size and SHA-256.

It is **unsigned, and nothing rests on it**. What it buys you is a precisely
localized failure at the other end — which file, which recipe — instead of a
generic verification error at push time, plus an inventory you can read
before plugging the medium into an isolated zone. Why it is safe for it to
be unsigned is the subject of
[the media security model](../../air-gap/media-security/).

### Before you unmount

- The synchronization completed, or every blocked recipe is understood and
  accepted as absent.
- The media manifest was written — it is always the last write.
- The run ID is recorded in your transfer paperwork.
- The medium is cleanly unmounted. On Windows in particular, a served
  FileSet used to hold the volume open; it no longer does, but shutting the
  instance down before ejecting is still the procedure.

The **Media screen** on the source side is the packing list for exactly this
moment: which zone the medium is addressed to, when it was resolved, what it
delivers, what it weighs.

<!-- TODO: screenshot: the Media screen on the source side — the handover card read before the medium is unmounted -->

Next: [import on the isolated side](../../air-gap/import-destination/).
