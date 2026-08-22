# Milestone 4 — quality audit

Point-in-time quality audit of **tobby-fetch v0.4.1** and **recipe-spec**,
run 2026-08-21, after the milestone-4 acceptance (see
`milestone-4-crucible-report.txt`) and before opening milestone 5
(mirror & air-gap). Method: eight independent review dimensions plus a
tooling pass, followed by an adversarial verification round — every
high-severity finding was handed to an independent reviewer instructed to
refute it, with proof by execution where possible. All findings below the
fixes list are addressed in **v0.4.2**; the remainder is explicitly deferred.

## Measured baseline

| Measure | tobby-fetch | recipe-spec |
|---|---|---|
| `go build` / `go vet` | clean | clean |
| `golangci-lint` (strict profile) | 0 issues | 0 issues |
| Test packages | 26/26 pass | 3/3 pass |
| Coverage (per-package mean) | 86.8 % (min 76.5, max 100) | 93.7 % |
| `govulncheck` (reachable) | 24 — all Go stdlib, toolchain 1.25.6 | 3 — same cause |
| TODO/FIXME | 2 (both: FileSet media type pending in the spec) | 0 |
| Largest non-test file | `internal/config/config.go`, 887 lines | `recipe/v1alpha1/constraint.go`, 602 lines |

Dimension scores (0–10): architecture & maintainability 8.5 · security 8 ·
tests 8.5 · performance & runtime robustness 7 · UX 8.5 · tooling & CI 8.5 ·
recipe-spec design 8 · recipe-spec robustness 8.

## Confirmed high-severity findings

Each of these survived the adversarial verification round.

1. **Data race between task persistence and parallel ingredient sync**
   (B-016). Ingredient goroutines mutate the task clone under `syncRecipe`'s
   *local* mutex, while `save()` marshals the same fields under the queue's
   `q.mu` — two disjoint locks, no happens-before. Reproduced with
   `go test -race` against the real queue (6 race warnings, including the
   exact writer/reader pair).

2. **The GC can sweep blobs of an in-flight transfer** (B-017).
   `internal/store/store.go` documents gcMu as "content writes hold it
   shared", but no `RLock()` exists anywhere: `WriteBlob`/`PutManifest`
   write lock-free while `DeleteRepository` (reachable straight from the
   UI/API handlers) runs an exclusive mark-and-sweep; `pruneRepositoryLinks`
   also ignores the sweep grace period, so freshly-committed layer links of
   a not-yet-tagged manifest are collectable.

3. **Tasks are never pruned** — one task per scheduler cycle, no retention,
   no pagination on `GET /api/v1/tasks` or the tasks screen, everything
   reloaded at startup: ~52 000 tasks/year at a 10-minute interval, with
   `List` copying and sorting the full set on every poll. No scheduler
   coalescing either: cycles pile up behind the serial worker, and a full
   pending channel leaves an orphan task re-queued at next start.

4. **recipe-spec: the cookbook's digest-identity promise is false** (B-018).
   `cookbook.Build` serialises manifest fields in an order that matches
   neither `ocispec` v1.1 (used by oras/crane) nor the normative §11.2
   example — proven by execution: identical content, two different manifest
   digests. `DecideRepublication` founds §8 immutability on that digest, so
   cross-tool republication misbehaves by construction.

5. **No e2e coverage of the flagship "signed recipes over media into
   air-gap" journey** — m1 covers media without recipes, m3 recipes without
   air-gap, m4 connected passthrough. Downgraded from high to medium on
   verification: that journey *is* the milestone-5 scope, and the project's
   definition of done already requires the m5 crucible scenario. Kept here
   as an entry requirement for milestone 5, not as debt.

## Notable medium findings

- No `recover()` in the task runner: a panic in a third-party parser kills
  the whole passthrough service, and the interrupted task is re-queued at
  startup — a persistent crash loop.
- Unchecked type assertion on `ReferrersLister` in the sync path.
- No rate limiting on authentication: every failed Basic attempt costs an
  argon2id at 64 MiB — memory-amplified DoS from inside the zone.
- Session cookie `Secure` flag keyed on `r.TLS`, absent behind the
  documented TLS-terminating reverse-proxy deployment; no HTTP security
  headers on UI responses.
- Nothing bounds a stalled HTTP body read, and the single serial worker
  turns one frozen stream into instance-wide starvation.
- Usage errors exit 1 instead of the documented 2; `recipe push` leaks raw
  transport errors around the error taxonomy.
- Entry documentation frozen pre-v0.1: the README quick-start example fails
  against v0.4.1 and never mentions `tobby quickstart`.
- `deploy-pages.yml` is the only workflow not SHA-pinned (with
  `pages: write` + `id-token: write`); secret scanning exists only as the
  opt-in local hook, not in CI.
- The GC grace period (`sweepGrace`) had no positive test — an inverted
  timestamp comparison would have passed the whole suite.
- recipe-spec: no size bound at the SDK entry (a 95 MB document validates
  at ~900 MB RSS); `VerifyManifest` checks neither digest format nor a
  negative layer size; `metadata.version` is unbounded although it becomes
  an OCI tag (128-char limit); the JSON Schemas are never exercised in the
  *reject* direction; partial-version constraint semantics (`>1.2`, `^1`)
  exist only in the SDK, not in the spec; the milestone-4 spike record was
  never committed; no semver release of the module exists.

## What the audit found strong

Strictly layered internal dependency graph with consumer-side interfaces;
centralised bilingual error taxonomy rendered consistently on CLI, UI and
API (RFC 9457); fail-closed cosign verification end to end with correct
DSSE PAE and digest binding — no bypass of signature verification or policy
was found; secrets that redact themselves by construction; zero
`InsecureSkipVerify`; `os.Root`-confined extraction and serving; streaming
everywhere with bounded readers; exemplary intra-blob resume
(Range/If-Range, fsync-before-sidecar, per-digest locks); OCI protocol
never mocked in tests (real embedded registry on ephemeral ports);
reproducible releases verified by independent double build, SLSA L3
provenance, signed SBOMs, distroless non-root image, weekly rebuild behind
a Trivy gate.

## Disposition

Fixed in **v0.4.2**: findings 1–4 above (B-016, B-017, B-018), the task
runner recover, the `ReferrersLister` assertion, auth rate limiting, the
cookie/headers hardening, the stalled-stream watchdog, usage exit codes,
`recipe push` taxonomy mapping, task retention + pagination + scheduler
coalescing, the positive `sweepGrace` test, the Go toolchain bump to
1.25.13 (clears every govulncheck finding), a `govulncheck` gate added to
CI and the mise tasks in both repositories, SHA-pinning of
`deploy-pages.yml`, a gitleaks job in CI, the README/landing refresh, and
the recipe-spec hardening batch (input size bound, `VerifyManifest`
validation, `metadata.version` bound, reject-direction schema tests, §9.2
partial-version semantics, renovate configuration, committed spike record).

Deferred, with owners: the m5 crucible scenario and the media/store index
(milestone 5 scope); `Browse`/`Counts` full-store scans (resolved by the
milestone-5 media manifest index); machine-readable CLI output (`--json`,
scheduled as R-08); sweep decoupling/coalescing on deletes; extraction of
the duplicated blob-copy loop into a shared package; line/column positions
in recipe-spec validation errors.
