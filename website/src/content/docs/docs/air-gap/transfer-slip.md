---
title: The transfer slip
description: The printable, bilingual account of one physical transfer — medium, freshness, inventory, verification, vulnerability scan and assurance markers — as HTML or plain text, from the Media screen, the API or the command line, and why it is an unsigned aid and never a proof.
sidebar:
  order: 9
---

A disk changes hands between people who do not share a screen: the person
who wrote it, the person who carries it, the person who imports it, and
whoever has to sign off on the transfer. The **transfer slip** is the page
they can hold. It restates what the [Media screen](../../air-gap/import-destination/)
already shows, in one printable document, in English or French.

It decides nothing and adds no behaviour of its own. Every line is read from a
record Tobby already keeps — the manifest, the last verification report, the
scan reports of the task documents, the assurance markers — and drawing a slip
**hashes nothing and runs no verification**.

## What it is not

Every slip opens and closes with the same notice, in the language of the
slip:

> **Unsigned aid document — not proof of authenticity.**

Tobby holds no private key and signs nothing ([ADR-0006, ADR-0007](../../reference/srs-adr/)).
The slip carries no signature, and neither does the manifest it summarizes:
that inventory is an **unsigned claim** until verification re-hashes the
medium. Authenticity comes from the recipes' signatures, verified against the
trust roots of the destination instance, and the slip names the key
fingerprint that verified each one so the claim is checkable rather than
decorative. Do not file a slip as evidence that a medium is genuine; file it
beside the medium it describes.

The notice cannot be switched off, and it is in every format: the HTML page,
the plain text, and the `"signed": false` of the JSON form.

## What is on it

| Section | Content |
| --- | --- |
| **The medium** | Side (source or destination), zone served, zone the medium is addressed to, media identifier, resolution and writing times, producing Tobby version and run identifier, volumes. |
| **Freshness** | The last medium imported for this zone, whether the zone identity matches, whether the medium is older than the last import. |
| **Inventory** | One row per recipe (digest, resolution time, assurance) and one per ingredient (kind, tag, digest). |
| **Verification report** | The verdict, the medium-wide refusals (with whether an administrator may waive them, and whether one did), the three stages — manifest, checksums, signatures — and one row per delivery naming the offending file when it is blocked. |
| **Vulnerability scan** | The policy in force and the database in use with its age; then, **per recipe and per element**, the verdict, the mode the report was evaluated under (**advisory** or **blocking**), the threshold, the age of the database *that report ran against*, the counts by severity, the violations, and the worst findings. |
| **Reduced assurance** | Every recipe admitted under a relaxed trust scope, with the scope and what it waived ([R-22](../../security/reduced-assurance/)). |

The **reduced-assurance markers** are not only collected in their own section.
They also sit beside the recipe they qualify — in the inventory, in the
verification table and in the scan section — because a marker that must be
looked for is a marker that gets missed. On a destination, the slip shows the
instance's own verdict next to the source zone's claim, labelled as a claim.

### About the scan section

Scanning runs where content is retrieved, on the source side. The reports are
kept in the task documents of the instance that scanned, and the manifest does
not carry them. A slip drawn on the instance that retrieved the content lists
the reports of the most recent task that scanned each element. A slip drawn
on the destination says, for each recipe, that **it holds no scan report** —
it does not invent one. Hand the source-side slip over with the disk when the
destination's reviewers need the scan results.

The scanner runs in **advisory mode by default**: findings are reported and
nothing is withheld. The mode column makes that visible per element, so a
slip never leaves a reader assuming that a clean-looking page means a
blocked policy. See [vulnerability scanning](../../security/vulnerability-scanning/)
to set a blocking threshold.

## Getting it

**From the Media screen** — the *Transfer slip* card offers *Open the slip*
(a page for the browser's print dialog — use your browser's Print and save to
PDF if you need a PDF), the same in the other language, and *Save as HTML* /
*Save as text*. Reading the screen is a `viewer` action and so is the slip.

**From the API** — `GET /api/v1/media/slip`, role `viewer`:

```sh
curl -u viewer:… 'https://tobby.zone.example/api/v1/media/slip?format=text&lang=fr'
curl -u viewer:… -OJ 'https://tobby.zone.example/api/v1/media/slip?format=html&download=1'
```

| Parameter | Values |
| --- | --- |
| `format` | `html` (default), `text`, `json` — the language-neutral document, codes and states rather than sentences. |
| `lang` | `en` or `fr`; the `Accept-Language` header when absent. |
| `download` | `1` adds a `Content-Disposition: attachment` header. |

**From the command line** — on the host that holds the medium, with no
serving instance:

```sh
tobby media slip --storage-root /mnt/usb --zone production --lang fr > bordereau.txt
tobby media slip --storage-root /mnt/usb --zone production --output html > slip.html
```

With a zone identity the command verifies the medium first, exactly as
`tobby media verify` does and writing nothing, so the slip carries the
verdict. Without one it is a source-side slip. It exits `0` whatever the
verdict: a blocked medium is a successful slip of a bad medium. The command
does not read the scanner, so its scan section says the scanner and database
state were not read; the per-element reports stand on their own.

## The HTML file

The HTML export is **self-contained**: the print stylesheet is inline, and the
page loads nothing — no font, no image, no script, no link — and says so in
its own `Content-Security-Policy`. It reads the same on a laptop in a zone
with no network, in a mail attachment and in an archive. Its print rules set
an A4 page, keep table rows and the two notices whole, and need no colour:
everything that needs attention is also a word in capitals.
