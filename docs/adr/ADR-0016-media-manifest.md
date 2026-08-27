# ADR-0016 — Media manifest: an unsigned inventory, and what verification actually rests on

## Status

Accepted — 2026-08-26 (milestone 5, mirror & air-gap; implements FR-054 and
its R-19/R-28 amendments)

## Context

ADR-0006 made the storage directory the unit of transport: a self-contained,
relocatable tree carried on a physical medium from a connected zone into an
air-gapped one. ADR-0007 fixed authenticity on key-based cosign signatures
verified offline against trust roots distributed out of band.

Milestone 5 turns that into an operating mode, and the destination side needs
to answer a question the store alone cannot: **is what I am holding complete,
and is it what left the other zone?** A directory tree says nothing about
what should have been in it. A blob that never made it across is
indistinguishable from a blob that was never meant to be there.

The obvious answer — write an inventory next to the content — raises a
harder question immediately. An inventory that verification *depends on* is
a trust anchor, and Tobby holds no private key (ADR-0007): it
could not sign one. An attacker with write access to the medium would then
corrupt a blob, rewrite the inventory to agree, and hand back a medium that
verifies. Writing an unsigned inventory and treating it as authoritative
would be worse than writing none: it would convert a visible absence of
proof into an invisible false one.

A second question surfaced during the milestone. FR-054 said "integrity or
completeness failure SHALL block with no override" — a verdict on the whole
medium — while RECIPE-SPEC §12.3 required consumers to fail closed **per
item**. Two conforming implementations could read the same damaged medium
and do opposite things, and the operator could not tell which they were
looking at.

## Decision

### 1. The manifest is an inventory, never a trust anchor

Every mirror synchronization ends by writing `meta/media.json` into the
store, **after any prune**: the store format version, a media identifier,
the zone identity, the producing version and run, the Retriever resolution
timestamp, the fulfilled recipes with their ingredients and pinned digests,
and a file-by-file inventory (path, size, sha256).

It is **unsigned, and nothing rests on it**. Authenticity comes from the
recipes' cosign signatures verified against the **destination instance's**
configured trust roots, and from every ingredient matching its pinned digest.
The manifest answers "is anything missing, and does the medium describe
itself coherently" — a completeness and integrity aid for an operator, not
evidence.

### 2. Content addressing is checked alongside the inventory

Verification compares each covered file against its inventory entry **and**
against its own content address. The second check is what makes the first
one harmless: an attacker who corrupts a blob and rewrites the inventory to
match defeats the inventory and is still caught by the digest the content is
stored under. Dropping it would make the unsigned manifest load-bearing,
which is exactly what section 1 refuses.

### 3. Coverage stops where the medium is written after the fact

The inventory covers every regular file under the registry tree and under
`meta/`, excluding `meta/media.json` itself. Everything under `_tobby/` is
**outside coverage** — the task area, the operation logs, and the
destination-side return logs (the audit back-channel of FR-054). Files that
keep being written after the inventory is taken cannot be inventoried
without invalidating it on the next line they receive.

### 4. The unit of a block is the recipe

Verification and the push decision are taken **recipe by recipe**. A recipe
whose signature verifies and whose every reachable object matches its pinned
digest is pushable; a recipe failing either is blocked **whole**, with no
override, and named in the report with its offending file. A delivery that
verified in part is not a delivery — but withholding what failed is not the
same as discarding what did not, and a medium carrying several deliveries
still delivers the intact ones.

Four verdicts stay **medium-wide**, because per-recipe salvage is meaningless
for them:

| Condition | Override |
|---|---|
| Manifest absent, unparseable, or schema-invalid | none |
| Recipe graph (`meta/recipes.json`) altered | none |
| Zone identity does not match this instance | admin, audit-logged |
| Medium older than the last import recorded for this zone | admin, audit-logged |

The first two leave nothing to reason about; the last two are addressed to
someone else, or to an earlier moment. Content covered by the manifest but
reachable from no verified recipe is **reported and never pushed**, and
blocks nothing.

This is the reconciliation of FR-054 with RECIPE-SPEC §12.3, and §12.3 was
amended to state the granularity normatively rather than leave "the affected
item" to the reader.

### 5. Media identity and freshness are anti-accident guards

A media identifier is minted when the store is created and stays stable
across re-synchronizations onto the same store; it appears in the manifest
and in the logs on both sides, so an incident traces back to a physical
object. The destination persists, **in its state directory and never in the
store**, the identifier and resolution timestamp of the last medium it
imported per zone, and refuses an older one by default.

These guard against an operator re-importing last month's medium and rolling
a zone backwards. They are **not** security controls: the manifest is
unsigned, so a hostile party can forge a timestamp. Both refusals are
admin-overridable and audit-logged, and the recorded high-water mark never
moves backwards when an override restores an older delivery.

### 6. Trust material on the medium is ignored

Trust roots found on the transported store have no effect. They arrived with
the content they would be vouching for.

## Consequences

- A destination operator gets a legible answer — what is complete, what is
  damaged, which recipe is affected and which file broke it — without that
  answer ever being the basis of a trust decision.
- A partially damaged medium is not a wasted trip.
- The manifest can be inspected with `jq` and rebuilt by hand. Nothing is
  lost if it is: it is bookkeeping, and re-synchronizing regenerates it.
- Verification cost is a full hash of the medium's covered content. It is
  reported with progress rather than hidden, because on a large delivery it
  is minutes, not seconds.
- The state directory becomes load-bearing for the freshness guard, which
  reinforces its existing role as the single backup target (R-27) and the
  place secrets live — never the store (NFR-020).

## Alternatives considered

**Signing the manifest.** Rejected: Tobby holds no private key and gains
nothing it does not already have. The recipes are already signed, and a
recipe signature transitively covers every ingredient's pinned digest — one
signature already attests the exact bytes of the whole delivery.

**No manifest at all, reachability only.** The recipe graph alone tells you
what *should* be reachable, and re-verification would catch damage. But
nothing would then detect content that is present and unaccounted for, and
the operator would have no inventory to look at before plugging the medium
into the isolated zone. Pre-flight and reporting both want the inventory.

**Blocking the whole medium on any failure.** The simpler rule, and the one
FR-054 read like at first. Rejected: it makes a single corrupted byte
discard an entire physical trip, which pushes operators toward overrides —
and the override that would matter here is precisely the one we refuse to
offer.

**Blocking per ingredient rather than per recipe.** Would let half a
delivery through. A recipe is one delivery by construction (RECIPE-SPEC
§12.2: one signature, the exact bytes of the whole set); shipping half of it
delivers something nobody declared.
