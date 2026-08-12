# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
starting with `v0.1.0`.

## [Unreleased]

Milestone 3 — the recipe engine: a signed Recipe becomes verified content
in the local store — automatically, completely, and replayable with no
side effect.

### Added

- Recipe engine (roadmap 3.1–3.5): the configured Retriever (HTTP(S) URL,
  OCI reference, or local file) is parsed and validated through the
  recipe-spec Go SDK (strict: unknown field = rejection, actionable
  file/path/constraint errors); every entry resolves from its cookbook —
  exact tags or semver constraints (`12.x`, `^`, `~`, `>=`), highest
  match, never a silent fallback — and lands bit-exactly under its
  relocated path with an optional instance-wide base prefix. All four
  ingredient kinds transfer (ContainerImage with sparse platform
  selection preserving the pinned index digest, HelmChart with offline
  dependency verification, OCIArtifact with artifactType enforcement,
  FileSet), streamed, with bounded parallelism, bounded retries, and
  per-digest new/outdated/up-to-date statuses: a second identical run
  transfers zero bytes.
- Signature verification at entry (roadmap 3.4): cosign key-based, fully
  offline — trust roots configured inline, as files, or as HTTPS URLs
  fetched at configuration time; multiple keys for rotation by overlap.
  Verification is on by default for every recipe; relaxation exists only
  as explicitly declared trust scopes (repository patterns), visible in
  the configuration report, a permanent banner, the logs, and the task
  report — never a silent switch. Recipe and ingredient signature
  artifacts travel with the content.
- Source substitution and cascade (roadmap 3.5): a downstream zone
  fetches nominal references from its upstream zone registry without
  modifying the recipes; the relocated path is invariant across hops;
  logs and the resolution report show nominal and effective endpoints.
  Registry credentials load from a standard dockerconfigjson file.
- FileSet HTTP serving (roadmap 3.6): explicitly enabled, verified
  FileSets are extracted (OCI whiteout semantics, strict path-safety
  rules, decompression-bomb bounds) and served read-only under
  `/files/<name>/` with byte-range support; anonymous read is a
  per-FileSet opt-in, reported like every security reduction.
- Recipes screens and API: `/recipes` with the per-recipe
  source→destination mapping table, the configured Retriever source, and
  the Synchronize action; `/admin/retriever` for the admin view; strict
  API parity (`/api/v1/recipes`, `/api/v1/sync`, `/api/v1/retriever`).
  Synchronizations are tracked tasks with per-ingredient items and a
  resolution report (requested → resolved → digest → status).
- Admin removal of unit-imported content (FR-044 amendment): from the
  repository page or `DELETE /api/v1/content/{repo}`, audit-logged, with
  mark-and-sweep garbage collection preserving shared blobs and attached
  signatures; recipe-managed content shows the action disabled, naming
  the managing recipe. The store now records provenance (recipe-managed,
  unit import, seeded) and the recipe→content graph, and stamps its
  format version with an explicit compatibility policy.
- Guided first start: `tobby quickstart` fills the missing configuration
  step by step (directories, mode, first admin account, config file) and
  offers to serve — never mandatory, flags and environment keep full
  control; configuration validation is now scoped per command.
- Self-service password change: any account changes its own password on
  `/account` (current password required) or through the API mirror,
  audit-logged; other sessions of the account are signed out.
- Helm charts import directly from HTTPS chart repositories
  (`https://…/charts/<name>`), converted to standard OCI chart artifacts
  that `helm pull` reads back unchanged, with the FR-024 dependency
  verification; optional per-operation dependency vendoring produces a
  traced, self-contained chart (original and new digests recorded).
- Releases now also ship `.deb`, `.rpm`, and `.apk` packages (nfpm,
  linux amd64/arm64) inside the same reproducible chain — same
  SHA256SUMS, same SLSA provenance, same double-build gate — installable
  fully offline.
- Crucible scenario m3: real cosign-signed recipes on real nodes —
  verified synchronization, foreign-signature refusal, idempotence,
  FileSet serving with ranges, and a two-hop cascade with unmodified
  recipes and identical relocated paths.
- Trivy integration spike (ADR-0008 exit criterion): measured
  library-vs-binary footprint; recommendation recorded in
  `docs/spikes/trivy-library-vs-binary.md`.

### Fixed

- Copy chips fired one toast per page visited: the layout script re-ran on
  every boosted navigation and stacked its document-level listeners; it now
  wires them exactly once per browser page.
- The task detail reached right after starting an import never refreshed its
  item statuses or badge: the body zone now polls while the task is active,
  with the same auto-terminating, server-decided contract as the task list.
- The language switcher highlighted the language you would switch to instead
  of the current one.
- The theme toggle and language switch had no visible effect under boosted
  navigation: both live on `<html>`, so their forms now force a full page
  load.
- Opening the user menu grew the header bar; the menu is now a pop-under.
- `tobby user` demanded `--mode` although it only uses the state directory:
  configuration validation is now scoped per command.
- Tag tables and the manifest heading showed the total platform count of an
  index even when only a few platforms are local; they now show
  present/total (e.g. `2/16`), in the UI and as `presentPlatforms` in the
  API.
- `tobby config dump` and `tobby version` wrote their output to standard
  error, so redirecting the dump to a file produced an empty file — the
  very command the configuration error message recommends.
- Unit import refused the helm-style `oci://` reference form.
- Unit import of a reference without a tag failed as "not found" on chart
  repositories, which publish versions and no `latest` tag: the highest
  stable semver tag is now resolved and reported.

## [0.2.0] - 2026-08-12

Milestone 2 — user-experience preview: the first complete journey (sign in,
import, track, browse, pull) behind authentication that is on by default.

### Added

- Error taxonomy (`TBY-<domain>-<nnn>`): every user-visible error carries a
  short stable code and a structured bilingual message — what / probable
  cause / corrective action — rendered identically by the web UI, the CLI,
  and the REST API (RFC 9457 problem documents). CLI exit codes follow the
  taxonomy classes (0 success, 1 failure, 2 usage, 3 policy refusal,
  4 verification failure).
- Local authentication, secure by default: argon2id accounts created
  through `tobby user add|passwd|list` (the tool computes the hash), an
  instance refuses to start without an account, session-based sign-in with
  CSRF protection for the web UI, Basic/Bearer for the API and the embedded
  registry (`docker login` works with accounts and static API tokens),
  viewer/operator/admin role gating, and audit events for sign-ins and the
  token lifecycle. Disabling authentication is an explicit opt-in with a
  permanent banner and an audit record.
- Server-rendered web UI (Go templates + a vendored htmx and idiomorph,
  no Node toolchain): bilingual EN/FR from the first screen, dark and
  light themes on CSS design tokens with a WCAG-contrast regression test,
  embedded assets served with ETag revalidation, an operator theme
  override without rebuild.
- Content browsing: repositories grouped by canonical source host with
  search, kind filters and server-side pagination; repository and manifest
  detail down to per-platform presence (sparse indexes shown as such);
  copyable pull commands; identical parameters on the `/api/v1/content`
  mirror endpoints.
- On-demand unit import: bounded remote inspection with per-digest status
  (new / outdated / up-to-date), platform selection, direct-to-storage
  streaming transfer verified against pinned digests at commit, original
  index preserved bit-exactly (sparse when partially selected), Helm chart
  dependency verification (a chart missing an embedded dependency is
  refused, naming it), and per-host insecure-registry opt-in.
- Persistent task queue inside the store: per-item status, task-scoped log
  streams with correlation fields, resumption after interruption (a task
  caught mid-run restarts, never orphaned), live-updating screens through
  self-terminating polling, and full `/api/v1` mirrors including raw log
  download.
- Administration screen for accounts and API tokens (secrets shown exactly
  once), an embedded troubleshooting stub generated from the taxonomy
  (`/help#<code>` anchors), an about page, and a self-served OpenAPI 3.1
  document with a dependency-free HTML viewer.
- Milestone-2 scenarios in both test tiers: the hermetic CI topology and
  the crucible, covering the no-account refusal, anonymous rejection,
  API-driven import, bit-exact digests, authenticated standard-client
  pulls, idempotence, and task resumption across a hard kill.
## [0.1.0] - 2026-08-11

### Added

- Repository governance: license, `CONTRIBUTING.md`, `SECURITY.md`, DCO
  enforcement in CI, issue and pull request templates.
- Application skeleton: configuration precedence (flags > environment >
  YAML file), structured JSON logging, `/healthz` and `/readyz` probes,
  an OpenMetrics endpoint, and graceful shutdown.
- Audit journal foundation and per-run identifier (run ID) propagated
  through logs and the audit trail.
- Embedded OCI registry (CNCF `distribution/distribution` v3 as a library)
  serving read/write on a filesystem backend, with the on-disk storage
  layout following the ingredient-relocation convention (nominal,
  canonicalized source host as repository prefix).
- Quality gates enforced as blocking CI checks from the first commit: unit
  tests with the race detector and anti-flaky double run, per-package
  coverage floors, strict lint with zero suppressions, dependency-license
  compliance, and a Trivy vulnerability scan.
- Release chain groundwork for SLSA Build L3 provenance and signed
  artifacts.

[Unreleased]: https://github.com/tobby-fetch/tobby-fetch/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/tobby-fetch/tobby-fetch/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/tobby-fetch/tobby-fetch/releases/tag/v0.1.0
