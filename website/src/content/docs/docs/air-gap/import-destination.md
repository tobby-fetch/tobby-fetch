---
title: Import on the isolated side
description: The Media screen and its Verify → Report → Push sequence, what blocks a recipe and what blocks a whole medium, the two waivable refusals, and why an unverified medium serves nothing.
sidebar:
  order: 3
---

The destination instance is the same application, in the isolated zone,
pointed at the transported store. It treats the medium as untrusted until
proven otherwise, and the order is the guarantee rather than a sequence of
steps: **nothing is pushed, served or written before the whole medium has
been re-verified** (FR-054).

## What makes an instance a destination

One setting: `zone:` — the identity of the zone this instance serves, the
`metadata.name` of the Retriever that describes it.

```yaml
mode: mirror
zone: isolated-production
storage:
  root: /mnt/usb/tobby-store
state:
  root: /var/lib/tobby/state
destination:
  registry: registry.zone.example.com
```

A source-side instance reads its zone from the Retriever it resolves and
sets nothing here. A destination has no Retriever — its content arrives on a
medium — and without `zone:` it cannot tell whether a medium is addressed to
it at all. `tobby media verify`, `tobby media import` and the four
`/api/v1/media` endpoints refuse to run without it, naming the setting
([`TBY-CFG-001`](../../reference/errors/#tby-cfg-001)).

Verifying needs nothing else. **Importing additionally needs
`destination.registry`**: verification reads, the push writes, and the push
has to have somewhere to go.

## The Media screen

`/media` is the guided counterpart of the verification pipeline. It opens
with the medium's inventory summary — the zone it is addressed to, when the
delivery was resolved, which recipes it carries, how many files and bytes —
and then a numbered sequence of three steps.

**1 — Verify.** Re-reads and re-hashes every covered file, then checks each
recipe's signature against *this* instance's trust roots. On a full disk
this is minutes of I/O, so it runs in the background with live progress and
the page polls: you can close the tab and come back. Asking for a second
verification while one is walking the medium is refused
([`TBY-MED-031`](../../reference/errors/#tby-med-031), HTTP `409`) — two
walks over the same disk halve each other and answer nothing new.

**2 — Report.** Three stages named separately — manifest completeness and
checksums, ingredient digests, recipe signatures — and one verdict per
delivery: `pushable`, `partial` or `blocked`. A blocked delivery names the
file that failed, which is the difference between "re-copy the disk" and
"call the source zone". The raw report is the same JSON document
`GET /api/v1/media/verification` serves.

**3 — Push.** The control **does not exist** until a verdict has cleared at
least one delivery. Not greyed out: absent from the document. What is pushed
then goes through the same controls a passthrough promotion goes through —
the registry allow-list and the recipe signatures, re-checked over the exact
bytes about to leave — only what the zone registry is missing moves, and the
signed recipes land in the zone's own cookbook with their signatures.

<!-- TODO: screenshot: the Media screen on the destination side — the three-step sequence with a verification running and its progress -->

Reading the screen is a `viewer` action. Verifying and importing are
`operator` actions. Waiving one of the two refusals below is an
administrator's, and the waiver checkboxes are rendered only for one.

## What blocks what

The unit of a block is the **recipe**. A recipe whose signature verifies and
whose every reachable file matches its pinned digest is pushable; a recipe
failing either is blocked whole, with no override, and named in the report
with the file that decided it. Its neighbours on the same medium are
unaffected.

That is deliberate. A delivery that verified in part is not a delivery — a
recipe is one signature over the exact bytes of one set — but withholding
what failed is not the same as discarding what did not, and a medium
carrying several deliveries still delivers the intact ones. The alternative,
blocking a whole physical trip over one corrupted byte, is what pushes
operators toward overrides, and the override that would matter here is
precisely the one this product refuses to offer.

Per-recipe refusals, none of them waivable by anyone:

| Code | Condition |
|---|---|
| [`TBY-MED-010`](../../reference/errors/#tby-med-010) | a file the recipe reaches is not on the medium |
| [`TBY-MED-011`](../../reference/errors/#tby-med-011) | a covered file's size differs from its inventory entry |
| [`TBY-MED-012`](../../reference/errors/#tby-med-012) | a covered file's content does not hash to its inventory entry |
| [`TBY-MED-013`](../../reference/errors/#tby-med-013) | a file the recipe reaches that the inventory does not list |
| [`TBY-MED-014`](../../reference/errors/#tby-med-014) | a reachable manifest or index that cannot be parsed |
| [`TBY-MED-015`](../../reference/errors/#tby-med-015) | a blob whose bytes do not hash to the digest its own path claims |

The last one is what keeps the unsigned inventory from being load-bearing:
an attacker who corrupts a blob and rewrites the inventory to agree defeats
the inventory and is still caught by the content address.

### Four refusals stay medium-wide

Per-recipe salvage is meaningless for these, so they block everything:

| Condition | Code | Waiver |
|---|---|---|
| Manifest absent, unreadable, or in a format this build does not read | [`TBY-MED-001`](../../reference/errors/#tby-med-001), [`TBY-MED-002`](../../reference/errors/#tby-med-002), [`TBY-MED-003`](../../reference/errors/#tby-med-003), [`TBY-MED-004`](../../reference/errors/#tby-med-004) | **none** |
| The recipe graph (`meta/recipes.json`) does not match its inventory entry | [`TBY-MED-005`](../../reference/errors/#tby-med-005) | **none** |
| The medium is addressed to another zone | [`TBY-MED-006`](../../reference/errors/#tby-med-006) | administrator, audited |
| The medium is older than the last one imported for this zone | [`TBY-MED-007`](../../reference/errors/#tby-med-007) | administrator, audited |

The first two leave nothing to reason about — without the inventory there is
no completeness question to ask, and the graph *is* the reachability set, so
an altered one makes every per-recipe verdict meaningless.

The last two are addressed to someone else, or to an earlier moment. They
are **anti-accident guards, not security controls**: the manifest is
unsigned, so a hostile party can forge either field. That is exactly why
they are the only two an administrator may waive — `--allow-zone-mismatch`
and `--allow-stale` on the command line, checkboxes on the screen,
`allowZoneMismatch` and `allowStale` on the API — and why both the attempt
and the applied waiver are written to the
[audit log](../../security/audit-log/) with the actor and the origin.

**No role waives an integrity or a signature verdict.** There is no flag, no
confirmation dialog and no configuration key for it, for anyone,
administrators included.

### Findings that block nothing

Three conditions are reported and never pushed, without blocking anything:
a file under manifest coverage the inventory does not list
([`TBY-MED-020`](../../reference/errors/#tby-med-020)), an inventoried file
no verified recipe reaches
([`TBY-MED-021`](../../reference/errors/#tby-med-021)), and a covered
bookkeeping file other than the recipe graph that does not match its
inventory entry ([`TBY-MED-022`](../../reference/errors/#tby-med-022)).

There is no side door for loose artifacts: content the medium carries that
no verified recipe reaches is reported by name and stays where it is.

## An unverified medium serves nothing

"Verification precedes any push, any **serving**, and any local write" has
three verbs. A destination instance holding a transported medium withholds
`/v2/` and `/files/` until a verification it performed has cleared the
medium.

- Both surfaces answer **`403`** with
  [`TBY-MED-030`](../../reference/errors/#tby-med-030) — or
  [`TBY-MED-032`](../../reference/errors/#tby-med-032) once a verification
  has run and the medium did not come out whole — in the shape each
  surface's clients understand: the OCI error envelope for `docker` and
  `helm`, plain text for `apt` and `dnf`. Never a `404`, never a silent
  `503`. The refusal names the medium and the screen that opens the gate.
- **The instance stays live and ready.** `/healthz` and `/readyz` both
  answer `200`, and `/readyz` states in its body which surfaces are closed
  and where to open them. A `503` would take the instance out of rotation
  and remove the very screen that fixes the condition.
- **The gate opens on a whole medium and on nothing else.** A `partial`
  verdict does not open it. The push decision is per recipe because a recipe
  is a delivery; serving is not that decision, because `/v2/` hands out
  blobs and a blob a blocked recipe reaches is exactly the byte range that
  failed. Push the intact recipes into the zone registry, which then serves
  them from content that arrived by the checked path.
- **There is no opt-out, and no verdict survives a restart.** A cached
  verdict says these bytes were right once; the question the gate asks is
  whether they are right now. Re-hashing a disk on restart is minutes.
- **No role bypasses it.** A closed gate is not an authorization refusal:
  the role matrix decides who may ask, the gate decides whether this
  instance is willing to hand anything out yet.

A source-side mirror instance is unaffected. Its store carries a manifest
too — it wrote one — but the medium is its own output rather than something
that changed hands, and it has no `zone:` configured, which is precisely how
the requirement tells the two sides apart. A passthrough instance is
unaffected for the same reason: its store is a transit cache, not a
delivery.

## From the command line

Both commands re-verify first, and both refuse to run without a zone.

```sh
tobby media verify --storage-root /mnt/usb/tobby-store --zone isolated-production
tobby media verify --storage-root /mnt/usb/tobby-store --zone isolated-production --output json | jq .verdict
tobby media import --config /etc/tobby/config.yaml
```

`tobby media verify` reports and writes **nothing at all** — not even the
medium's own operation log. `tobby media import` does the whole journey and
journals what it did onto the medium, under `_tobby/logs/` and therefore
outside the manifest's coverage: that file is the return audit channel of
the transfer.

| Command | `0` | `1` | `3` | `4` |
|---|---|---|---|---|
| `tobby media verify` | every delivery is pushable | — | refused by policy (zone identity, freshness) | a verification failure |
| `tobby media import` | imported | a push failed | refused by policy | a verification failure |

These commands run in their own process, against the store directory
directly. **They do not open a running instance's surfaces**: the serving
gate is opened by a verification *that instance* performed, from its screen
or through `POST /api/v1/media/verify`. The refusal message says so, because
the mistake is an easy one to make.

## After the import

A completed import advances the per-zone freshness record — which is what
makes re-importing last month's medium a refusal rather than a silent
rollback — and the zone registry now serves the transferred content.
Connecting the zone's clusters and hosts to it works identically in both
modes: see [connect your clients](../../passthrough/connect-clients/).

Media that come back for a second cycle, and the housekeeping that keeps
them a sane size, are on
[managing media over time](../../air-gap/manage-media/).
