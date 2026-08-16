# Spike — Trivy integration: Go library vs. pinned external binary

> Decision record for the measurement spike scheduled by ADR-0008 (Decision 4)
> and the v0.3.x plan (risk R3): quantify the cost of importing Trivy as a Go
> library into the Tobby binary, against invoking a pinned `trivy` release
> binary, ahead of the v0.6.x scanning train. Both paths sit behind the
> scanner-neutral `Scanner` seam of ADR-0008 Decision 5 (content digest →
> normalized findings), so this choice is an implementation detail of the
> built-in Trivy scanner, not a format or config change.

**Date:** 2026-08-12 · **Toolchain:** go1.25.6 (Tobby), go1.26.5 (required by
the library, see below) · **Trivy:** v0.73.0 (latest at spike date, pinned for
all measurements) · **Host:** darwin/arm64 (sizes on linux are within a few
percent; the registry-footprint spike confirmed this holds for our stack)

## Methodology

- **Library path:** throwaway module, `go get
  github.com/aquasecurity/trivy@v0.73.0`, then a ~70-line `main` that does what
  a real `Scanner` implementation in Tobby would do — scan a local image tar
  with the vulnerability scanner only, offline DB (`SkipDBUpdate`), JSON
  output — through the entry point Trivy itself designates for library
  consumers (`pkg/flag.Options` + `pkg/commands/artifact.Run`; the source
  comments on `Options` explicitly mention "tools that use Trivy as a
  library"). Static build, `CGO_ENABLED=0 -trimpath -ldflags "-s -w"`.
- **Binary path:** official v0.73.0 release binary (darwin/arm64), DB
  downloaded into a dedicated cache with `--download-db-only`.
- **Workload:** `valkey:8-alpine` exported with `docker save` (17.1 MB tar,
  Alpine 3.23.3), scanned by both paths against the same DB snapshot
  (trivy-db v2, updated 2026-08-12T13:01Z).
- **Baseline:** current Tobby built from this repository with the same flags.

## Measurements

### Build and dependency footprint (library path)

| Metric | Tobby today | Spike module (Trivy lib) | Factor |
|---|---|---|---|
| Static binary size (`-s -w`, CGO_ENABLED=0) | 23.7 MB | 144.9 MB | ×6.1 |
| `go.sum` lines / unique modules | 254 / 115 | 1 164 / 522 | ×4.5 |
| Full module graph (`go list -m all`) | 178 | 967 | ×5.4 |
| Modules actually linked into the binary | 69 | 367 | ×5.3 |
| Cold dependency compile (Apple Silicon) | — | ≈ 35 s wall / 265 s CPU | — |
| Incremental relink after a one-file change | — | ≈ 1 s | — |

41 of Tobby's 69 linked modules already appear in Trivy's graph, so the
projected combined binary is ≈ 145–150 MB (Trivy dominates; the overlap
absorbs most of Tobby's own weight). For comparison, the official `trivy`
CLI binary is 152.1 MB unpacked (45.0 MB compressed): importing the library
imports essentially **all of Trivy**, even with scanning restricted to
vulnerabilities only — dead-code elimination recovers almost nothing because
the analyzer/detector registries are wired at `init` time.

Toolchain coupling, measured on day one:

- Trivy v0.73.0 declares `go >= 1.26.3` while Tobby is on go1.25.6 — adopting
  the library forces the project's Go version on Trivy's schedule.
- It imports `encoding/json/v2`, which on go1.26 only builds with
  `GOEXPERIMENT=jsonv2` — a Go *experiment* flag imposed on every Tobby build
  and CI job.
- No `replace` directives are required anymore (the historical blocker for
  Trivy-as-a-library is gone), and CGO stays disabled. Both genuinely good
  news for the library path.
- 19 of the 41 shared modules are pinned at *different* versions, including
  `go-containerregistry` (Trivy: v0.21.7, Tobby: v0.21.9) — Tobby's most
  critical dependency would live under MVS tension with a 479-requirement
  neighbor.

### Known CVEs imported into Tobby's own dependency set

Scanned with Trivy itself (DB of 2026-08-12):

| Dependency set | Findings |
|---|---|
| Tobby today | 1 (GO-2026-5932, severity UNKNOWN, `golang.org/x/crypto`) |
| With Trivy as a library | 4 — including **2 HIGH**: CVE-2026-71556 (`go-git` v5.19.1), CVE-2026-50163 (`oras-go` v2.6.1) |

The HIGHs come from Trivy's own pins; clearing them means overriding upstream's
tested versions or waiting on Trivy's release cadence. Under ADR-0011's
near-zero-CVE goal and CI scan gate, Tobby's own release pipeline would be
red on day one for vulnerabilities in code Tobby does not call.

### Runtime (both paths, same DB, same tar)

| Metric | Library (in-process) | Pinned binary (exec) |
|---|---|---|
| Findings on `valkey:8-alpine` | **73** | **73** (identical) |
| Scan latency, cold layer cache | 254 ms | 0.58 s |
| Scan latency, warm layer cache | 48 ms | 0.10 s |
| Peak RSS during scan | 96 MB | 149 MB |
| Artifact to ship | (inside Tobby) | 152.1 MB unpacked / 45.0 MB compressed |

Functional equivalence — the premise of ADR-0008's fallback — is confirmed:
identical finding sets from the same DB. The exec overhead (~0.3–0.5 s per
invocation) is noise next to fetch/transfer times, and result caching keyed by
(content digest, DB version) already planned in ADR-0008 makes repeat scans
free in both designs.

The library API, while importable, is CLI-shaped and trap-laden: in a 70-line
integration we hit two silent failure modes — a zero `Timeout` in
`flag.Options` produces an already-expired context (every scan fails), and an
empty `Severities` slice silently filters **every finding out of the report**
(scan "succeeds", zero results). Trivy is versioned v0.x: no SemVer
compatibility promise exists for any of these types, and each Trivy upgrade
would be a code-level migration inside Tobby's scan gate.

### Offline DB footprint (identical for both paths)

| Artifact | Compressed (OCI layer) | On disk |
|---|---|---|
| `ghcr.io/aquasecurity/trivy-db:2` | 104 MiB | 1.17 GB (`trivy.db` bbolt) |
| `ghcr.io/aquasecurity/trivy-java-db:1` | 904 MiB | (not materialized in this spike) |

The DB story is strictly orthogonal to this decision: both paths consume the
same cache layout (`db/trivy.db` + `metadata.json`) fed from the OCIArtifact
ingredient of ADR-0008 Decision 3. The on-disk and transport sizes above are
sizing inputs for the store and the physical media, not differentiators. The
Java DB's 904 MiB compressed is large enough that its inclusion should be a
deliberate, opt-in scope decision at the scanning milestone.

## Analysis

- **Qualification surface.** The library path makes Trivy's 522-module graph
  *Tobby's* graph: every Renovate bump, every CVE in `go-git`, AWS SDK
  subtrees, Rego/OPA, or any of ~450 modules Tobby never calls becomes a
  finding against Tobby's own releases and a line in Tobby's own VEX
  documents. With a pinned binary, Trivy is what it already is for the target
  environments: a separately versioned, separately attested Apache-2.0
  artifact, verified by digest like everything else Tobby handles.
- **Release-cadence coupling.** Trivy releases roughly monthly and moves its
  toolchain requirements aggressively (today: a Go version newer than ours
  plus a GOEXPERIMENT). As a library this cadence is imposed on Tobby's
  compile; as a binary it is a digest bump in packaging, and scanner and
  product can be upgraded independently — including by operators who need a
  newer scanner in the field without waiting for a Tobby release.
- **Isolation.** The scanner is the component that parses hostile input by
  design — that is its job. Out-of-process execution gives the scan gate
  crash and memory isolation from the rest of the last controlled gate; a
  malformed archive that panics or OOMs the scanner kills a child process,
  not the transfer engine.
- **Air gap.** Equivalent in both designs. The DB flows through Tobby as an
  OCIArtifact either way; the pinned binary itself can be delivered and
  updated through the same signed channel (dogfooding, exactly like the DB).
- **Licensing.** Apache-2.0 in both designs, compatible with inclusion in a
  GPL-3.0 work (ADR-0003); no differentiator.
- **What the library buys.** One artifact instead of two, ~0.4 s less latency
  per uncached scan, and about half the peak RSS. None of these matter at
  Tobby's operating point; and since the container image must carry the
  scanner either way, even the total shipped size is a wash (~150 MB combined
  binary vs. 23.7 MB + 152.1 MB).

## Decision: **pinned external binary** — the ADR-0008 fallback becomes the plan

This is precisely the "unreasonable footprint" outcome Decision 4 anticipated.
The `Scanner` implementation for v1.0 executes a **digest-pinned `trivy`
release binary**, shipped through Tobby's own distribution (bundled in the
container image; declared in packaging for the mirror-workstation profile),
verified against its pinned digest before first use, never resolved from
`PATH`. The driver invokes `trivy image --input <tar/OCI layout> --cache-dir
<db> --skip-db-update --format json` and maps Trivy's JSON (SchemaVersion 2)
to the normalized finding model of Decision 5. Config surface is unchanged, as
ADR-0008 promised.

Consequences for the scanning milestone (v0.6.x):

- The `Scanner` interface stays as specified (digest → findings: identifier,
  severity, fixed-in, source); the Trivy driver is its first implementation,
  and the JSON schema version is asserted at parse time so a scanner upgrade
  that changes the schema fails loudly, not silently.
- Packaging gains a pinned Trivy version (digest per platform), bumped by
  Renovate like any dependency — but in packaging metadata, not `go.mod`.
- The driver must materialize scan input from the store (exported tar or OCI
  layout — both accepted by `--input`-style invocation) and the DB cache
  layout (`db/trivy.db`, 1.17 GB on disk) from the trivy-db OCIArtifact;
  store sizing and media budgets should count 104 MiB compressed per DB
  refresh, and the Java DB (904 MiB compressed) stays out of scope unless
  explicitly pulled in.
- Scan-result caching keyed by (content digest, DB `UpdatedAt`) as already
  called for in ADR-0008.
- The library door stays open behind the same seam: if Trivy ever publishes a
  stable, slim scanning API, revisiting costs nothing architecturally.

The final integration decision will be formalized as an **amendment to
ADR-0008** at the scanning milestone, with these measurements as its record.
