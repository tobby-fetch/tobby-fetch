# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
starting with `v0.1.0`.

## [Unreleased]

## [0.4.2] - 2026-08-22

Hardening release. A point-in-time quality audit was run between
milestone 4 and milestone 5 — method and findings are recorded in
`docs/acceptance/milestone-4-quality-audit.md` — and this release carries
its fixes: two confirmed concurrency defects in the long-lived service
path, an unbounded task history, and a batch of surface and toolchain
hardening. No new features; milestone 5 starts from this baseline.

### Fixed

- Data race between task persistence and the parallel ingredient sync
  (B-016). Ingredient goroutines mutated the task under `syncRecipe`'s
  local mutex while the queue's `save()` marshalled the same fields under
  `q.mu` — two disjoint locks, no happens-before, reproduced with the race
  detector. Mutation and persistence now happen under the same lock by
  construction (`taskSink`), and every published copy of a task deep-copies
  the item slices the runner still owns. Both regression tests were played
  against the original code first and failed there.
- The GC could sweep blobs of an in-flight transfer (B-017). The store
  documented `gcMu` as "content writes hold it shared" but no shared
  acquisition existed anywhere: `WriteBlob` and `PutManifest` now actually
  take the read side — a multi-gigabyte stream holding off the sweeper for
  its whole duration is the FR-044 behaviour, not a regression — and the
  sweep grace period now covers repository links too, closing the window
  where committed layers of a not-yet-tagged manifest were collectable.
  The grace period itself gained the positive test it never had: a fresh
  orphan must survive a sweep and be counted as deferred.
- A panic in a task runner killed the whole service — registry, UI and
  all — and the interrupted task was re-queued at the next start, replaying
  the panic forever. The runner and each ingredient goroutine now recover:
  the task fails with `TBY-SRV-001` and the stack in its log, the process
  survives, and a failed task is terminal, not re-queued.
- `tobby recipe push` surfaced raw transport errors ("dial tcp: connection
  refused") instead of the taxonomy blocks the UI shows for the same
  failure; unreachable registries and rejected credentials now come back
  as `TBY-REG-002`/`TBY-REG-003` with the host named.
- Usage errors (unknown flag, unknown command) exited 1 like operational
  failures instead of the documented 2, and gave no pointer to `--help`.
  Scripts can now tell a mistyped invocation from a failed operation.
- The verified spool was destroyed even when handing the blob to the store
  failed, forcing a re-download of bytes that were already on disk and
  verified. An undrained spool now survives its reader.
- Creating a task while the queue channel was full persisted the task and
  then reported failure, leaving an orphan that the next start re-queued.
  The slot is reserved before anything is written.
- The type assertion on the referrers listing in the sync path was
  unchecked; a `Manifests` implementation without `Referrers` support now
  degrades with an explicit warning — signatures travel by referrers in
  the bundle layout (§12.2), so silence would strand the downstream
  zone — instead of panicking.
- Blob reads on the promotion path ran under a background context and
  could not be interrupted by shutdown; they now carry the task context.

### Added

- Task retention and pagination. Finished tasks are kept to the most
  recent `tasks.keepFinished` (default 500, `0` keeps everything);
  entries, their JSON and their logs are purged together, and pending or
  running tasks are never touched. `/tasks` and `GET /api/v1/tasks` are
  paginated exactly like `/content` (FR-061: same parameter, same page
  size, same navigation), and the tasks screen keeps polling the page the
  operator is looking at. The scheduler no longer enqueues a sync when one
  is already pending — a queued follow-up is what reconciles a stale read,
  a pile of them is not — and the raw task log downloads stream from disk.
- Failed-authentication rate limiting per client origin, applied before
  the password hash is computed: every rejected Basic attempt used to cost
  an argon2id at 64 MiB, an amplification an unauthenticated caller could
  drive at will. Exhausted budgets answer 429 (`TBY-AUTH-012`,
  `Retry-After`) on every surface, and successful machine-surface
  verifications are cached for one minute — keyed by credential hash,
  never the password, invalidated on password change and account removal —
  so a registry client no longer pays argon2id per request.
- Security headers on every UI response, including a Content-Security-
  Policy that allows the vendored inline scripts by SHA-256 hash rather
  than `unsafe-inline` — hashed from the *rendered* output, because
  html/template rewrites scripts on the way through, and a hash of the
  source matches nothing a browser ever sees. The browser suite failed on
  the first attempt and passes on the final one, which is the order those
  two facts are useful in.
- `server.secureCookies` for deployments where TLS terminates at a
  reverse proxy: the session cookie's `Secure` flag was keyed on `r.TLS`
  and silently absent in exactly the deployment the charts document. The
  operator declares the topology; a spoofable forwarding header does not.
- A progress watchdog on blob downloads: a connection that stops
  delivering bytes for two minutes is cancelled and retried — header
  timeouts never covered a frozen body, and the serial worker turned one
  stalled stream into an instance-wide famine. Resume makes the
  cancellation nearly free.
- An idle timeout on the shared listener. Read and write deadlines are
  deliberately still absent: the same listener streams multi-gigabyte
  blobs both ways, and a global deadline would cut a slow pull mid-blob.
- `govulncheck` joins the quality gates (`mise run vuln` and a CI job,
  version pinned and renovate-tracked), and a gitleaks job scans the full
  history in CI — the pre-commit hook only ever protected clones that had
  opted in. The Go toolchain moves to 1.25.13, which clears every
  reachable stdlib finding the audit's scan reported.
- The milestone-3 recipe-engine journey is now an e2e gate: a new hermetic
  topology scenario exercises real cosign verification, foreign-key
  refusal, idempotence and the cascade in CI, and the milestone-2 scenario
  that existed but was wired to nothing runs alongside it.

### Changed

- The README and the landing page caught up with reality: the project had
  shipped four milestones while both still described a design-first
  repository with an example that no longer started. The quick start now
  documents `tobby quickstart` — interactive and non-interactive — and
  every documented command was run before being written down.
- `deploy-pages.yml` was the one workflow still referencing actions by
  mutable tag while holding `pages: write`; it is pinned by full SHA like
  everything else (ADR-0011).

## [0.4.1] - 2026-08-18

### Fixed

- An instance serving the generated fallback certificate could not adopt a
  replacement from the administration screen (FR-082). The certificate
  reader only ever re-read a CONFIGURED path, so an instance that started
  self-signed had none and would have kept serving the fallback while the
  operator saw a success. It refused instead, which was the honest half of
  the answer and not the useful one.

  It now offers a destination inside the state directory — the only place
  a private key may live (R-16) — beside the generated pair rather than
  over it, since the fallback's fingerprint is what an operator pinned
  before the replacement. Adopting is a separate step from writing,
  because skipping it is exactly how "replaced" becomes a lie. An instance
  with neither a configured path nor a state directory still refuses, and
  names the reason.

## [0.4.0] - 2026-08-18

Milestone 4 — use case one: Tobby as a long-lived service between two
connected zones, holding the destination registry at the level the
Retriever asks for.

### Added

- Continuous promotion (4.1, FR-013/026/028/034/035). A configured
  `destination:` is reconciled on a schedule in passthrough mode: only the
  blobs the destination lacks are transferred, signatures are re-verified
  against the local copy before EVERY push rather than once at import, and
  the signed recipes are propagated to the zone cookbook alongside their
  ingredients. Destination names follow the relocation convention, and a
  destination that will not accept them says so before anything moves
  (`TBY-DST-001`). The refresh interval is changeable at runtime, survives
  a restart, and the change is audited as sensitive configuration. The
  write path is deliberately NOT built on source substitution:
  substitution answers where content is read FROM, and applying it to a
  write would send bytes to an endpoint nobody named — unattended, once
  per interval, for as long as the instance lives.
- Registry allowlist (4.2, FR-030), evaluated on the host actually
  contacted — which under substitution is not the one the recipe names —
  and refused before the socket is opened, on every outbound path
  including the destination and HTTPS chart repositories. An absent key
  means no restriction and an empty list means nothing is allowed, as with
  a NetworkPolicy; an undeclared policy is reported as undeclared rather
  than rendered like a satisfied one. Refusals are audited and counted.
- Account lifecycle in the UI and the API (4.3, FR-073/074): accounts can
  be created, re-roled and removed without leaving the tool, with the hash
  always computed by the tool. An instance can never lose its last
  administrator, by deletion or by self-demotion — check and write are
  atomic in the store, so no surface added later can route around it. The
  permission matrix is documented in `docs/rbac-matrix.md` and enforced by
  a table that FAILS when a route is registered without declaring its role
  floor.
- Authentication audit coverage (FR-094): every failed verification of a
  presented credential is recorded and never deduplicated — collapsing
  them would hide the brute force the trail exists to show — successes are
  recorded once per credential and origin per window, and a request
  carrying no credential records nothing, that last one being every OCI
  client's opening probe.
- `docker login`, `helm registry login` and `oras login` are exercised
  against the embedded registry over real sockets, with the role ladder
  checked on the wire (FR-076), and CI installs the clients so the checks
  run instead of skipping.
- Enterprise network (4.4, FR-080/081/082): one outbound transport shared
  by every path — authenticated forward proxies, private certificate
  authorities added WITHOUT ever disabling verification, and server TLS
  with an administrator-supplied certificate or a generated fallback whose
  fingerprint is logged and which reloads on replacement. A test proves no
  outbound path bypasses the shared transport, and its negative proves the
  test itself: with the proxy removed, every path must fail.
- Publishing a recipe from the interface (R-40), the counterpart of
  `tobby recipe push`: the document is validated before anything is sent,
  and the published digest comes with the `cosign sign` command to run
  next. Tobby holds no private key and says so on the screen.
- A network posture screen: what the listener actually presents, its
  fingerprint, SANs and validity, with a self-signed fallback shown as the
  degraded posture it is. Certificate replacement takes files, never text
  fields, and the private key is returned in no form at all — not its
  bytes, not its length, not a digest.
- Reference deployment (4.5): a Helm chart and raw manifests — non-root,
  read-only root filesystem, every capability dropped, seccomp
  `RuntimeDefault`, probes wired, no service-account token mounted. The
  store and the state directory get separate volumes, and the chart
  refuses to render when they would share one.
- Milestone-4 crucible scenarios and a hermetic topology scenario, both
  covering promotion behind an authenticated proxy and a private PKI, an
  off-list destination refused, and a second cycle that moves nothing.

- Fine-grained resume of large downloads (R-29, completing FR-029). A blob
  interrupted at 90 % now restarts at 90 %, not at zero: above
  `transfer.resumeThreshold` (default 64MiB) the bytes are spooled in the
  state directory with their offset and the source's validator, and the
  next attempt asks for the rest with an HTTP `Range` request. It survives
  a killed process, not just a dropped connection. Integrity stays
  blocking — the digest is computed over the whole spool, resumed prefix
  included, before a byte reaches the store — and a source that ignores
  `Range` and answers `200` with the full body is detected and restarted
  rather than concatenated. Per-blob progress appears on the task detail,
  including whether the transfer resumed or restarted. The measurement
  behind the design is recorded in
  `docs/spikes/blob-resume-range-vs-gcr.md`: go-containerregistry cannot
  do partial reads (×10 the useful bytes on a 90 % interruption), so the
  blob GET is issued directly — over the same shared transport, the same
  proxy, the same private authorities and the same keychain as every other
  outbound path, and proved so by `internal/netx`'s wiring test. New
  configuration section `transfer`, new environment variable
  `TOBBY_TRANSFER_RESUME_THRESHOLD`, new error codes `TBY-REG-007` and
  `TBY-STO-003`. **Operational note:** the state volume now temporarily
  holds one copy of each resumable blob — the deployment defaults raise it
  from 1Gi to 20Gi, and `transfer.resumeThreshold: 0` restores the previous
  streaming behavior byte for byte.
- Browser non-regression level (R-38): a deliberately narrow chromedp
  suite under `test/browser`, behind the `browser` build tag with its own
  CI job, covering the class of bug that lives in an attribute the CLIENT
  interprets — where the rendered HTML is right and the handler is right
  and the screen is still broken. Scenarios: the Content and Tasks filter
  forms, all five kind badges and both selects (B-011); the copy toasts
  and boosted navigation (B-001); the theme toggle reaching `<html>`
  (B-004); the task detail updating itself and stopping its own polling
  (B-002); the user-menu pop-under not growing the header (B-005); the
  recipe document's copy and download (R-37). Chrome is taken from the
  environment and NEVER downloaded (NFR-019): with no browser the suite
  skips with an explicit message, and `TOBBY_E2E_REQUIRE_CHROME=1` — which
  CI sets — turns that skip into a failure. The license gate now also
  covers test-only dependencies, so the new tree is checked like any other
  (ADR-0011).

### Fixed

- Trust scopes were matched against two different pattern spaces on the
  two halves of a promotion (B-014). On any registry carrying a port, a
  correctly written scope admitted a recipe at import and then refused it
  before the push with `TBY-SIG-001`, and only listing both spellings
  worked. Canonicalization now happens inside the policy instead of at
  each call site: a third caller could not have guessed which of the two
  forms was expected. Found by the milestone-4 crucible run.
- A Sigstore bundle signature — cosign 3.x's default — verified on the
  zone that fetched it and was gone one hop down (B-015). The verifier
  learned both cosign layouts at milestone 3; the copy had only ever
  learned the tag-attached one, so the referring artifact and the index
  that makes it findable were left behind, and a downstream zone refused
  content its upstream had accepted with "no signature artifact found".
  Signatures travel with content whatever shape they arrive in (§12.2).
  Found by the milestone-3 crucible scenario replayed for this milestone.
- The polled zones of the task screens swapped their response INTO
  themselves instead of replacing it (B-012). `hx-swap="morph:outerHTML"`
  is a swap style htmx only knows once the idiomorph htmx extension is
  registered, and the vendored asset was the bare library with no
  `hx-ext` anywhere: htmx silently fell back to its default innerHTML
  swap, nesting a second `#task-body` inside the first — duplicate ids,
  and an outer zone that kept its polling attributes for ever, so the
  auto-terminating polling never terminated. Now vendoring
  `idiomorph-ext.min.js` 0.7.4 and enabling the extension on the shell.
  Found by the R-38 browser scenario; no server-side test could see it,
  the fragment being byte-for-byte correct.
- File downloads were hijacked by `hx-boost` (B-013). A boosted anchor is
  fetched by the client and swapped into the page, so the recipe document
  (R-37), the raw task log and the OpenAPI document were DISPLAYED as raw
  text instead of downloaded — htmx cancels the navigation before the
  `download` attribute gets a say. `hx-boost="false"` on the three links,
  the same remedy the preference forms already use (ADR-0015 §7).

## [0.3.0] - 2026-08-16

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
  offline, in **both published layouts** — the classic attached signature
  and the Sigstore bundle that cosign 3.x produces by default, discovered
  through the OCI 1.1 Referrers API or its fallback tag. Publishers pick a
  format; consumers no longer have to. Trust roots are configured inline,
  as files, or as HTTPS URLs fetched at configuration time; multiple keys
  for rotation by overlap.
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
- `tobby recipe push <file> <ref>` (R-36): publishes a recipe to any OCI
  registry, checking it first — which is the difference with a generic
  push tool. It refuses a document that is not a valid recipe, one that
  is not fully pinned (a cookbook holds cooked recipes only), one whose
  name or version contradicts the reference it is published under, and
  any republication of an existing version onto different content — a
  published recipe version is immutable. Republishing identical bytes is
  a no-op, not a conflict. The published digest goes to stdout, ready for
  `cosign sign`; signing stays outside Tobby, which never holds a private
  key. New refusal `TBY-POL-004`. Source substitution deliberately does
  not apply to a publication: it answers where content is read from, and
  letting it redirect a write would publish to an endpoint the author
  never named.
- The recipe document is now readable in the interface (R-37): the
  manifest page of a recipe shows the YAML this instance holds and
  verified on entry — with its digest, a copy button and a download — so
  deriving the next version no longer means leaving the tool for an
  `oras pull`. Deliberately a download and not an editor: a cooked recipe
  is immutable, so the next version is a new document under a new
  `metadata.version`.
- `examples/`: five recipes for software that really crosses into
  restricted zones — Harbor, Keycloak, MetalLB, the OpenTelemetry
  Collector and the VictoriaMetrics operator — plus the Retriever that
  ties them into one zone. Each carries the reasoning behind its
  ingredient list, because `helm template | grep image:` misses four
  distinct classes of image; the VictoriaMetrics operator is the worked
  example of the worst one, where the components live in the operator's
  own compiled defaults. Every digest and platform label was checked
  against the live registries, and a test parses the whole directory with
  the specification SDK so an example cannot drift from what the engine
  accepts.

### Fixed

- The Content and Tasks filters only reacted to their first control
  (B-011): ticking any kind badge but `ContainerImage`, or changing the
  task type, toggled the widget and requested nothing. `from:find <sel>`
  binds the htmx listener to the FIRST matching descendant — the
  attribute reads "listen to the checkboxes" and means "listen to the
  first checkbox". Both forms now listen on the form itself, where a
  descendant's event bubbles. A template guard rejects the pattern:
  `from:find` is allowed only for a selector unique in its file, and a
  filter form must carry an unscoped `change`.
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

[Unreleased]: https://github.com/tobby-fetch/tobby-fetch/compare/v0.4.1...HEAD
[0.4.1]: https://github.com/tobby-fetch/tobby-fetch/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/tobby-fetch/tobby-fetch/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/tobby-fetch/tobby-fetch/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/tobby-fetch/tobby-fetch/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/tobby-fetch/tobby-fetch/releases/tag/v0.1.0
