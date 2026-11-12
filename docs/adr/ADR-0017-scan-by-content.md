# ADR-0017 — Scanning by content, whatever the declared kind

## Status

Accepted — 2026-11-12 (milestone 6, vulnerability scanning; decided by the
project lead on 2026-11-12, ROADMAP milestone 6 arbitration 1; fixes B-054)

## Context

ADR-0008 made Trivy the scanner and FR-031 placed the scan at retrieval,
"where applicable". Until this decision the engine read "applicable" from
the recipe: an ingredient declared `ContainerImage` was scanned, and one
declared `OCIArtifact`, `FileSet` or `HelmChart` was not looked at — not
even under a blocking policy.

The declared kind is written by the recipe author, and the kind is not a
property of the bytes. That makes the scan a filter one sentence of YAML
switches off:

- **An image disguised as an artifact.** An image is pushed with its own
  manifest — image config, rootfs layers — and declared `OCIArtifact`, say
  an "LLM model". It crosses unexamined. On the other side nothing stops a
  runtime from pulling that digest and running it: a container runtime
  reads the manifest, not the recipe.
- **An image carried as a file.** A FileSet is itself an OCI image
  (RECIPE-SPEC §7.4) whose files are the delivery; one of those files can
  be a `docker save` archive or an OCI image layout packed as a tar,
  served by `/files/` and `docker load`-ed on arrival. The same holds for
  an artifact whose layer blob *is* such an archive (`oras pull`, then
  `docker load`).

In both cases the zone's vulnerability policy — the control a regulated
site relies on to refuse what is at or above its threshold — is bypassed
by a choice of label, and the bypass is invisible: no report says
anything was skipped.

Two constraints frame the answer. Scanning must stay offline and add no
outbound connection (NFR-019). And the content is hostile by assumption:
the code that judges it must not be made to read without bound, extract
without bound, or decompress without bound.

## Decision

### 1. What is scanned is decided by content

A declared `ContainerImage` is scanned as before. **Any other ingredient
is inspected, and scanned when its content is, or carries, an image.**
The declared kind only selects *how* the content is looked at, never
*whether*.

The inspection is part of the scanner seam (`scan.Scanner.Inspect`), next
to `Scan`. It needs neither the binary nor the database: an image hidden
under another kind is recognized — and refused under a blocking policy —
in the degraded mode as well.

### 2. The detection rule

For an OCI manifest (and every child of an index the store holds, nested
indexes included, genuine attestation manifests excepted as for images):

- **image config**: `config.mediaType` is
  `application/vnd.oci.image.config.v1+json` or
  `application/vnd.docker.container.image.v1+json` → an image;
- **filesystem layers**: otherwise, at least one layer has a filesystem
  layer media type — the closed set a runtime unpacks: OCI `tar`,
  `tar+gzip`, `tar+zstd`, their non-distributable variants, the Docker
  schema 2 rootfs (and foreign) layers — **and its bytes are a tar** →
  an image.

The set is matched exactly. A type that merely *ends* like one
(`…trivy.db.layer.v1.tar+gzip`, `…helm.chart.content.v1.tar+gzip`) is not
a filesystem layer: no runtime unpacks it. "Any layer" rather than "all
layers", so that adding one custom layer does not hide an image. "And its
bytes are a tar" because ORAS labels plain files with the OCI tar type by
default: a model pushed that way is not an image, and a runtime could not
unpack it as one.

For a byte stream — every layer blob, and every regular file inside a
tar layer, whatever its name or extension:

- **docker-archive**: a tar whose `manifest.json` describes at least one
  image (`Config` and `Layers`) whose named files are all in the archive;
- **oci-layout**: a tar with an `oci-layout` marker and an `index.json`
  reaching at least one manifest that is an image by the manifest rule
  (manifest blobs held only when they hash to their name, so a duplicated
  path cannot show the detector one manifest and a reader another).

Compression is recognized by magic bytes (gzip, zstd), a tar by its
header checksum. Archives are looked into **one level deep**.

### 3. Bounded, streamed, never extracted to judge

Detection reads each stream once, sequentially, and holds only small
metadata documents (`manifest.json`, `index.json`, `oci-layout`, layout
manifests, 1 MiB each, 16 MiB in all) and the shallow entry names a
`docker save` manifest may reference. A stream that is not a tar is
recognized from its first 512 decompressed bytes and not read further.
Bounds: 64 GiB of decompressed bytes per inspection, **shared** with the
streams nested in it (many small compressed files cannot each expand to
the bound), 2^20 tar entries. A stream past a bound, a truncated or
corrupt archive, is an **error, never "not an image"**.

Only a detected archive is materialized, in the scanner's private work
directory: its regular files are written under names the adapter chooses
(an entry name is a lookup key, never a path), bounded in size and count,
links resolved inside the archive only. A `docker save` image is then
described by a synthesized schema 2 manifest, an OCI layout image by its
own; either is copied, blob by blob and digest-verified, into the
one-image OCI layout Trivy is always given (ADR-0008 decision 4: the
filesystem contents are still parsed out of process, by Trivy only).

### 4. The existing policy applies unchanged

A detected image is evaluated exactly like a declared one: threshold,
exceptions, levels, the database age bound, the R-19 granularity (the
recipe is withheld whole, not written on a medium, not pushed), the
advisory default. Under a blocking policy, **a detected image that could
not be scanned — and content that could not be judged — is refused**, as
any item that could not be scanned.

### 5. Every surface says what happened

- A scanned ingredient not declared as an image carries `content`
  (declared kind, detected images with digest, file and evidence) and a
  first reason `TBY-SCN-023`: scanned, *detected as an image from its
  content*, declared X.
- An ingredient holding no image carries a report with the verdict
  `out-of-scope` and `TBY-SCN-024`; it is logged, never silent, and
  never a refusal. It is not counted in `tobby_scan_items_total`, which
  counts scans.

### 6. The scanner's own deliveries are evaluated advisory

The signed `tobby-system` recipe (verified, no relaxed trust scope — the
same test that already decides which recipe arms the scanner, B-040)
delivers the scanner binary as a FileSet, and that FileSet is an image by
content. Refusing it with the policy it arms would deadlock a zone: no
scanner yet, so not scannable, so refused, so never armed; or a fresher
database refused because the Trivy image carries findings, so the
database never updates. Its detected images are therefore scanned and
reported under an **advisory** policy, the database age bound included,
with `TBY-SCN-025`. This grants nothing new: whoever controls that
signed recipe already controls the scanner and its database.

## Consequences

- The disguise attack is closed for the two shapes above; the regression
  is B-054, fixed in v0.6.0.
- Every non-image ingredient is read once per digest per process
  (content-addressed, cached). For the Trivy database itself — a 1.6 GB
  tar — this is a few seconds of decompression on each new database.
- Helm charts, models, databases now carry a visible `out-of-scope`
  report instead of nothing: an operator sees that they were looked at.
- In blocking mode, an artifact whose content cannot be judged (a
  truncated archive) is refused where it used to cross. That is the
  fail-closed side, and the reason is stated.
- The API gains an `out-of-scope` verdict and a `content` object; both
  are additive.

## Limits

- **Only image archives are looked for in a FileSet's files.** Other
  files — binaries, scripts, unpacked language packages — are not
  scanned beyond what Trivy finds in the FileSet's own filesystem.
- **One level deep.** An archive inside a `docker save` file inside a
  FileSet is not looked into.
- **A plain root-filesystem tar is not an image archive.** `docker
  import` can turn one into an image; it is not detected as such when it
  sits in a custom-typed layer or a file.
- **Declared container images are not looked into for archives.** They
  are scanned as images; a `docker save` file inside one is not
  separately scanned.
- **Whiteouts are not applied.** A file an upper layer deletes is still
  inspected — conservative: its bytes still travel.
- **The Java database** is still not handled (ADR-0008, vulnerability
  scanning limits): findings that need it are missing from detected
  images as from declared ones.
- **No re-scan at media import**, unchanged: content is judged where it is
  retrieved.

## Alternatives considered

- **Trust the declared kind** (the previous behaviour). Rejected: the
  kind is the recipe author's word, and the filter then depends on the
  party it filters. It is also invisible — nothing reports the skip.
- **Scan everything as an image.** Hand every ingredient to Trivy.
  Rejected: an artifact that is not an image fails the scan, so under a
  blocking policy every model, chart and the Trivy database itself would
  be refused — the database could never cross, and the scanner could
  never be armed. Trivy's filesystem mode on extracted content would
  instead require Tobby to extract hostile archives in full, which the
  bounded detection avoids.
- **Refuse undeclared images outright.** Rejected: a FileSet *is* an image
  by specification, and an image legitimately published as an artifact
  should be judged by the same policy as any image, not by a second rule.
- **Detect by file name or extension** (`*.tar`, `*.tar.gz`). Rejected:
  trivially evaded by renaming.
