# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
starting with `v0.1.0`.

## [Unreleased]

### Added

- **Mirror mode is a complete operating mode** (feature 5.1, FR-014,
  FR-050, FR-054). A synchronization is triggered by hand — never by a
  loop, and mirror mode builds no scheduler at all rather than leaving one
  idle — and what it leaves behind is a self-contained store that is the
  unit of transport: artifacts, recipes, operation logs, and the media
  manifest that describes them.
- **The media manifest** (FR-054). Every mirror synchronization ends by
  writing `meta/media.json` into the store, after any prune, so the
  inventory describes what the medium finally holds: the store and
  manifest format versions, a media identifier, the zone the medium is
  addressed to, the producing version and run, the Retriever resolution
  timestamp, the fulfilled recipes with their pinned ingredients, and a
  file-by-file inventory of paths, sizes and digests.

  It is **unsigned, and nothing rests on it** — Tobby holds no private key
  and could not sign one. Authenticity comes from the recipes' cosign
  signatures verified against the *destination's* trust roots, and from
  every ingredient matching its pinned digest; trust material found on the
  medium is ignored. Verification therefore checks each covered file
  against its inventory entry **and** against its own content address: an
  attacker who corrupts a blob and rewrites the inventory to agree defeats
  the first check and is caught by the second. Without that, an unsigned
  manifest would be load-bearing, which is the one thing it must never be.
  Recorded in `ADR-0016`.
- **Blocking at the right granularity** (FR-054 amendment R-19). FR-054
  read as a verdict on the whole medium while RECIPE-SPEC §12.3 required
  failing closed per item, and the two are now the same sentence. The unit
  is the **recipe**: one whose signature verifies and whose every reachable
  object matches its pinned digest is pushable; one that fails either is
  blocked whole, with no override, and named in the report with the file
  that failed. A delivery that verified in part is not a delivery — but
  withholding what failed is not the same as discarding what did not, so a
  damaged medium still hands over its intact recipes. Four conditions stay
  medium-wide, because per-recipe salvage means nothing for them: an
  unusable manifest and an altered recipe graph, neither overridable; a
  foreign zone and a stale medium, both overridable by an administrator and
  audit-logged. RECIPE-SPEC §12.3 was amended to say this normatively,
  since "the affected item" left two conforming implementations free to
  behave in opposite ways on the same damaged medium.
- **Media identity and freshness** (FR-052/FR-054 amendments, R-28). A
  media identifier is minted with the store and stays stable across
  re-synchronizations onto it, appearing in the manifest and in the logs on
  both sides so an incident traces back to a physical object. The
  destination remembers, per zone, the identifier and resolution timestamp
  of the last medium it imported — in its **state** directory, never on the
  medium — and refuses an older one by default, naming both timestamps.
  These are anti-accident guards, not security controls: the manifest is
  unsigned, so a hostile party can forge a timestamp. What they prevent is
  an operator re-importing last month's disk and rolling a zone backwards.
- **Pre-flight: does it fit, and will it go through** (feature 5.2,
  FR-055). Before a synchronization or an export, the bytes to transfer are
  computed per recipe and in total — deduplicated by digest, net of what
  the target already holds — against the target's free space and its
  filesystem's per-file ceiling. A projection above free space minus a
  configurable safety margin (default 10 %) is refused before any transfer,
  stating the shortfall in bytes (`TBY-STO-004`); a filesystem positively
  identified as unable to hold the largest file to be written is refused
  naming the limit (`TBY-STO-005`), single-tar exports included; an
  unidentifiable filesystem warns rather than refuses. The file-size
  ceiling is judged **before** free space, because a FAT32 volume with
  terabytes free still cannot hold the blob and "not enough space" would
  send an operator to buy a disk that fixes nothing. Detection is
  deliberately honest per platform — `statfs` magic on Linux,
  `f_fstypename` on macOS, `GetVolumeInformationW` on Windows — and a
  filesystem this build knows no ceiling for is reported as unidentified,
  never as capable.
- **Plan mode: simulate before acting** (FR-055 amendment R-04). A
  side-effect-free operation in both modes, from `tobby sync --dry-run`,
  `POST /api/v1/plan` and the screen, over the configured Retriever or a
  candidate document: resolved versions, per-digest statuses, deduplicated
  volumes, projected prune, and the policy verdicts reachable without a
  transfer. It writes nothing, pushes nothing, and does not advance a
  passthrough instance's refresh schedule — a plan that moved the cadence
  would turn a diagnostic into a side effect. Exit codes distinguish
  nothing to do, changes planned, and refused by policy, so it works as a
  CI gate.
- **Prune to the Retriever** (FR-045, and R-33 for passthrough). Content a
  recipe brought and the resolved Retriever no longer references is
  removed: on by default in mirror mode, where the operator sees the list
  and the total size before confirming, and opt-in in passthrough, where
  nobody is watching the loop and a transit store is nobody's delivery
  unit. Eligibility is a **positive** test — only recipe-provenance content
  ever goes — which protects unit imports, seeded content and the offline
  vulnerability database by construction rather than by a namespace list
  that would have to be kept in step. A configurable store-occupancy
  threshold raises a persistent banner and a metric, in both directions.
- **Secrets never travel** (NFR-020, R-16). A configured secret path that
  resolves under the store root makes the instance refuse to start, naming
  the path — the store is handed to a courier and plugged into a machine in
  another zone, so anything under it is assumed to be read by someone else.
  Resolution follows the real filesystem, so a relative path, a `..`, a
  symlink or a differently-cased spelling cannot slip past it. Secret files
  are created owner-only: mode 0600 on Unix, a protected access list
  granting the owning account alone on Windows.
- **`tobby fileset pack`** (FR-048, R-41). A local directory becomes a
  FileSet OCI image, imported through the unit-import path and servable
  under `/files/` — with no HTTP write surface anywhere in the flow, which
  is the whole point: the operator who needs to serve a handful of files
  gets them served without an upload endpoint being reopened. Packing
  refuses first what §14.5 refuses at extraction, where the operator can
  still fix the tree. The layer tar is uncompressed so that the digest is a
  pure function of the directory: a compressor's output is a function of
  its version, and a gzip layer would produce new content from an unchanged
  tree at the next toolchain bump. From the screen and the API, packing is
  confined to the directories `files.packRoots` lists, and no entry means
  no path — reading an arbitrary host directory on a network request is a
  capability an instance is given, not one it holds.

- **Windows is a validated platform, not a compiled one** (NFR-018,
  feature 5.6). `windows-latest` joins the CI test matrix and runs the
  whole suite under the race detector, twice — until now Windows was
  covered by cross-compilation alone, which checks that the code parses.
  The UC2 journey is played end to end there: a mirror synchronization
  produces a store, the store is carried to a path it has never occupied
  (the transport, simulated by a directory copy — the real removable
  device is the crucible's job), and a destination-side instance verifies
  it and pushes its content with digests identical from one end to the
  other. The runner also attaches a genuine FAT32 volume, so the FR-055
  file-size pre-flight is exercised against the filesystem it exists for
  rather than against a fixture, and the NFR-020 owner-only access list is
  read back from the objects it was applied to. Two source files written
  for Windows had been merged and released without ever executing; they
  execute now. The supported feature matrix per operating system is
  documented in *Reference → Supported platforms*, in English and French.
- **winget and Scoop manifests for the Windows workstation**
  (`packaging/winget/`, `packaging/scoop/`). Both describe the portable
  release binary pinned by SHA-256, and the release workflow renders them
  from this run's own `SHA256SUMS` and attaches them to the release.
  Neither is published automatically: submitting to `microsoft/winget-pkgs`
  is a reviewed human step, and the Scoop bucket repository does not exist
  yet. The jobs say so, precisely, with the remaining manual steps.

- **Operation on the isolated side of a physical transfer** (FR-052,
  feature 5.4). The same application, pointed at the transported store,
  now completes the journey the medium was prepared for: re-verification,
  then a differential push into the zone registry, then the signed recipes
  into the zone's own cookbook. The medium is untrusted until proven
  otherwise, and the ORDER is the guarantee rather than a sequence of
  steps — FR-054 makes verification precede any push, any serving and any
  local write, and a test watches the destination registry so that a
  single request arriving before the verification report fails the build.
  `tobby media verify` reports without touching the store; `tobby media
  import` does the whole journey; `GET /api/v1/media`, `POST
  /api/v1/media/verify` and `POST /api/v1/media/import` are their strict
  mirrors (FR-061). Exit codes follow the taxonomy classes: a zone
  refusal is a policy refusal (3), a corrupted blob a verification
  failure (4).
- Only what verification cleared is pushed, recipe by recipe (R-19,
  RECIPE-SPEC §12.3): a medium carrying two deliveries where one arrived
  damaged still delivers the intact one, and names the other with the file
  that decided it. Signatures travel with their content in **both** cosign
  layouts across this hop too (§12.2, B-015) — the classic attached tag
  and the Sigstore bundle's referring artifact with its fallback index —
  so what verified on the source zone verifies again at the destination.
  Trust roots present on the medium are ignored: a key file planted on a
  medium buys nothing, and a test plants one to prove it.
- **The operation log on the transport medium** (FR-053, FR-056). Both
  sides of a transfer now record what they did with the medium, in the
  same JSON schema as every other Tobby log, inside the medium itself —
  under `_tobby/logs/`, outside the media manifest's coverage, because a
  log written inside coverage would invalidate the very inventory it
  accompanies. Size-based rotation bounds it; an explicit `fsync` at every
  task boundary means a yanked medium loses at most the entries of the
  task in progress. Tested by execution: a process killed outright
  immediately after a task leaves that task's records readable on the
  medium. Configurable through `logging.media.*`.
- **The per-zone freshness record** advances on completed imports only
  (R-28): re-importing last month's medium is refused by default, naming
  both timestamps, and an administrator may waive that refusal
  deliberately — audited (FR-094), with the register never rolling
  backwards. The record lives in the instance state directory and never on
  the medium, since a register carried on the medium would be rewritten by
  whoever holds it.
- `zone:` (`TOBBY_ZONE`) states a destination-side instance's zone
  identity. A source-side instance reads it from the Retriever it
  resolves; an instance whose content arrives on a medium has no Retriever
  and cannot otherwise tell whether a medium is addressed to it.

- **The documentation travels inside the binary** (R-05, NFR-003 amendment
  2026-08-11). The destination zone is cut off from the network by
  definition, so documentation that lives on a website is documentation the
  operator who needs it most cannot read. `/help` now serves the whole
  corpus offline: the operations guides of both modes, the *Try* walkthrough,
  the security, recipe, project and reference sections — 50 pages, English
  and French, screenshots and diagrams included — plus the troubleshooting
  index still generated live from the error catalog, so `/help#<code>`
  anchors keep resolving against the codes this binary actually carries.
  Screens carry a contextual pointer into the guide that covers them; the
  dashboard points at the guide of the mode the instance runs.
- The embedded corpus is a byte-for-byte copy of `website/src/content/docs`
  written by `tools/helpsync`, not a second edition of it: `mise run
  help-check` (and a CI job on every documentation change) fails the moment
  the two drift apart. A link check over the corpus as embedded proves no
  cross-page link, anchor, screenshot or error-code reference points at
  nothing, in either language.
- French translations of the whole *Connected zones (passthrough)* section
  (7 pages), on the website and in the binary alike.

- Pre-flight checks before a mirror synchronization (public feature 5.2,
  FR-055). Tobby now computes, per recipe and in total, the bytes it would
  transfer — from the source manifests, deduplicated by digest, net of what
  the local store already holds — the projected store size, and the target's
  free space. A synchronization is REFUSED before any transfer when the
  projection exceeds free space minus a configurable safety margin
  (`preflight.safetyMarginPercent`, default 10 %), stating the shortfall in
  bytes (`TBY-STO-004`); it is refused when the target's filesystem is
  positively identified as unable to hold the largest file to be written —
  FAT32's 4 GiB ceiling, single-tar export archives included
  (`TBY-STO-005`) — and it WARNS, without refusing, when the filesystem
  cannot be identified. A "file too large" error arriving mid-write fails
  cleanly and leaves the store byte-identical.

  Filesystem identification is deliberately honest and per-platform:
  `statfs(2)` magic numbers on Linux, `f_fstypename` on macOS,
  `GetVolumeInformationW` on Windows. A filesystem this build knows no
  ceiling for is reported as unidentified — never as capable.

  `preflight.disabled: true` turns the gate into a report, in the FR-075
  shape: explicit, announced at startup, and logged again every time it
  lets a refusal through. The verdict keeps its refusal code, so a disabled
  gate can never be mistaken for a passed one.

- Plan mode: simulate a synchronization before running it (R-04, FR-055
  amendment). `tobby sync --dry-run`, `POST /api/v1/plan` and the
  `/recipes/plan` screen produce the same report over either the configured
  Retriever or a candidate one (a file, a URL, an OCI reference, or a
  document submitted inline): resolved versions (FR-021), per-digest
  statuses against the store and against the destination (FR-026),
  deduplicated volumes and the space verdict (FR-055), the projected prune
  (FR-045), and the policy verdicts that need no transfer — the registry
  allow-list (FR-030) and the recipes' own signatures (FR-033).

  A plan writes nothing to the store, pushes nothing, and does not touch
  the passthrough refresh schedule (FR-013). The guarantee is structural —
  the planner holds a read-only store interface and no scheduler — and it
  is locked by a test that fingerprints the entire store tree before and
  after a plan and fails on any difference.

  Exit codes make it usable as a CI gate (FR-066): `0` nothing to do,
  `5` changes planned, `3` refused by policy, `4` verification failed,
  `1` the plan could not complete. `--output json` emits the report itself,
  schema documented alongside the OpenAPI document.

- **OCI image layout export and import (FR-051).** The store — or a
  selection of recipes and repositories — can be written as a standard
  OCI image layout, directory or single uncompressed tar, readable by
  `skopeo`, `oras` and `crane`, and imported back at identical digests.
  This is the product's exit ramp, on purpose: the content is recoverable
  without Tobby. The original index of a multi-platform image travels
  unchanged, sparse platform sets included (FR-042, RECIPE-SPEC §7.1), so
  its pinned digest survives; the cosign signature artifacts of both
  published layouts — the attached `sha256-<hex>.sig` tag and the
  referrers fallback index with the referring artifact it names by digest
  — travel with the content they attest (RECIPE-SPEC §12.2). Available as
  `tobby export` / `tobby import`, at `POST /api/v1/oci-layout/…`, and on
  the `/admin/oci-layout` screen.
- **Export pre-flight numbers (FR-055 groundwork).** The export plan is a
  side-effect-free operation — `tobby export --dry-run`, `POST
  /api/v1/oci-layout/plan`, the screen's estimate — reporting the
  projected total and the size of the largest single file the export
  writes. The second number is what a target filesystem's per-file limit
  (FAT32's 4 GiB) is compared with, and a single-tar export is one file.
- **Store reset (FR-046).** A full reset behind an explicit typed
  confirmation, restricted to the admin role and audited (FR-094) —
  including on an instance running with the FR-075 authentication
  override, where the entry records the unauthenticated context. Content
  and its ledgers go; the operation history and task logs stay, because a
  trail a destructive action erases is not a trail.

- **The Media screen** (FR-062 amendment R-02), on both sides of a
  physical transfer. On the source it is the packing list read before the
  disk is unmounted: which zone the medium is addressed to, when it was
  resolved, what it delivers, what it weighs. On the destination it is the
  guided sequence FR-054 already made normative — Verify, Report, Push —
  with the Push control absent from the document, not merely disabled,
  until a verdict has cleared a delivery. Verification runs in the
  background with live progress, because a full medium is minutes of I/O
  and a frozen page is not a progress display. The report names the three
  stages separately (manifest completeness and checksums, ingredient
  digests, recipe signatures) and, for every blocked delivery, the file
  that failed. A zone mismatch and a stale medium are stated in words with
  the course of action and the administrator waiver that takes it; an
  integrity or signature verdict has none, and the screen says so. The
  screen introduces no engine behaviour: every route mirrors one under
  `/api/v1/media`, and `GET /api/v1/media/verification` serves a machine
  the state the screen polls.

- **A command line under a stable contract (R-08, FR-066 amendment).** An
  automation can depend on this CLI across versions, and four promises say
  what that means. `--output json` on every command that reports anything,
  spelled the same way everywhere — three milestone-5 lots had each
  written their own `--output` or `--json` — with the machine document
  ALONE on standard output and every log, prompt, progress line and audit
  record on standard error (B-010, which this closes for good). The
  documents have a published JSON Schema, shipped in the binary beside the
  OpenAPI one and served at `GET /api/v1/cli-output.schema.json`. An
  exhaustive exit-code table, GENERATED from `internal/taxonomy` and
  carried verbatim by the reference pages, covered by the project's
  semantic-versioning promise: removing a code or renumbering one is a
  breaking change, and a test fails the build in both directions — a code
  the table does not list, or a row nothing can produce. A guaranteed
  non-interactive mode: no command prompts, none requires a terminal, and
  the whole command tree is run with a pipe on standard input to prove it.
  And `--wait` on every command that starts a task on an instance.
- **`tobby sync` triggers a synchronization (FR-014, FR-066).** It was a
  usage error without `--dry-run`, because the store is held open for
  writing by whoever serves it and a second process cannot open it too.
  The right answer was never to open the store: the command now DRIVES the
  instance through `POST /api/v1/sync` — the endpoint behind the
  "Synchronize" button — and says so in its first line of help. `--wait`
  follows the task to a terminal state and exits on the TASK's outcome, so
  a policy refusal on the instance is exit `3` on the command line;
  `--prune` and `--prune=false` are distinct from saying nothing at all.
  The instance is named by `--instance`, `TOBBY_INSTANCE_URL`, or nothing
  at all when the command runs on the instance's own host. The API token
  comes from `TOBBY_API_TOKEN` or `--token-file` and is deliberately not a
  flag: flag values are visible in the process table (NFR-015).
- `TBY-REG-008`: an ingredient asking for a platform the source index does
  not publish. It was a bare `fmt.Errorf` that settled as `TBY-SRV-001` —
  "an internal error occurred", whose corrective action is to search the
  logs for a correlation identifier — for a mistake in the operator's own
  recipe (found while fixing B-020). The message now names both sides of
  the comparison the fix requires: the selectors that matched nothing, and
  the platforms the index actually carries.

### Fixed

- **A synchronization's `platforms:` filter made arm64 unusable** (B-020).
  Both readers of the `os/arch[/variant]` notation rendered each index
  child back into a label and compared strings, which silently made the
  optional variant mandatory — and registries publish their arm64 child
  with variant `v8`, so `linux/arm64`, the spelling of RECIPE-SPEC's own
  example, matched nothing and failed the ingredient. The same defect sat
  behind a second door, the FileSet serving selector. One rule now lives in
  one place, and RECIPE-SPEC §7.1 states it normatively: two independent
  call sites getting it wrong the same way is an under-specified rule, not
  two mistakes.
- **A failed ingredient left no trail** (B-021). The item carried its
  taxonomy code and nothing else: no log record at any level, and the
  wrapped cause dropped on the way to persistence. For codes whose
  corrective action is "follow the correlation identifier in the logs"
  (FR-090), that instruction led to an empty result set. The ingredient
  path now logs with its correlation fields, and the technical cause is a
  field of its own beside the code — never localized, so it matches the log
  record character for character.
- **A restart with a long backlog wedged the task queue** (B-019).
  Resuming sent every active task on a bounded channel that nothing drains
  until after `Open` returns, so more than 256 of them blocked startup
  indefinitely. Resumed tasks now go to a backlog the worker drains ahead
  of the channel.
- **One unresolved FileSet declaration hid all the others** (B-022). The
  refresh abandoned the whole synchronization on the first resolution
  failure, leaving `/files/` empty — including for FileSets already in the
  store — with a single warning line as the only trace, while the function
  it called documented the opposite intent. It now serves what resolved and
  reports each failure. Found by running the packing flow end to end; no
  unit test could see it, because each exercised one declaration at a time.
- **The embedded registry could not write anything on Windows** (B-023).
  distribution's filesystem driver renames its temporary file over the
  target and only then closes it, which Unix allows and Windows answers
  with a sharing violation — and that path is how every manifest revision
  link, every layer link and every tag is stored. Its listing also joined
  keys with the platform separator, which the library's own parsers read
  with the slash-only `path` package: repository enumeration came back
  empty and the garbage collector marked nothing reachable. Both are
  corrected by a driver that wraps the library's, on a single code path
  that behaves identically everywhere rather than a Windows branch nobody
  else would run.
- **FileSet extraction validated paths against one separator** (B-025).
  Entry names and symbolic-link targets were checked for `/` only, while
  the packing side has refused `\` since FR-048. On Windows `..\..\evil`
  and `C:\evil` therefore escaped the §14.5 refusal — containment held,
  because every write goes through an `os.Root`, but the rejection never
  fired, the depth limit miscounted and whiteout markers were materialized
  as ordinary files. Worse for links: `os.Root` contains what is written,
  never what a link points at, so a target the check waved through became
  a real out-of-rootfs symlink left in the cache. The extractor now
  refuses the same spellings the packer does, lexically, so a FileSet
  accepted on the Linux side of a mirror cannot arrive at a Windows
  destination carrying something that destination would have refused.
- **A file server that could never let go of its cache** (B-024). Each
  served FileSet held a directory handle for the process's life with no
  way to release it — and that cache lives inside the transportable store,
  so on Windows, where an open handle is what makes a volume refuse to
  unmount, an instance that had served a single FileSet left the operator
  unable to eject the medium they had just shut it down to carry away — and made an interrupted extraction unrepairable
  while the instance ran. `fileserve.Server` now has `Close`, releases the
  tree it is about to clear, and reports a purge it could not complete
  instead of failing a synchronization that installed everything asked of
  it.
- **"The task is done" did not mean the queue had let go of its files**
  (B-026). The task worker was fire-and-forget and the task's log handle
  was released after the terminal status had been published, so a clean
  shutdown could return while a file under the storage root was still
  open — on a mirror workstation, that is an operator being told the
  medium they are ejecting is in use. `Queue.Wait` now exists, `serve`
  waits for the worker within the same grace period the listener drains
  under, and the log is closed before the status is published.
- **The "secrets never travel" guard was evadable by spelling** (B-027,
  NFR-020). Containment is decided by `filepath.Rel`, which refuses to
  relate paths whose volume names differ — so `\\?\C:\store\creds.json`
  was not "under" `C:\store`, and the startup refusal that keeps a
  credentials file off a medium handed to a courier did not fire. The
  extended-length and device spellings are now normalized, and a
  substituted drive or an administrative share is resolved by asking the
  operating system which path the handle actually names.
- Three smaller Windows defects: an operation-log path naming a volume was
  refused with the platform's opaque errno instead of an actionable
  message (B-028); a Retriever source such as `C:/config/retriever.yaml`
  was dialled as a registry named `C:` (B-029); and FileSet names
  differing only by case, by a trailing dot, or matching a DOS device name
  collided on one cache directory, destroying served content (B-030).

- **An unverified medium was served** (FR-054). "Destination-side
  verification SHALL precede any push, any **serving**, and any local
  write" has three verbs; the push and the local write were enforced and
  the serving was not, so `tobby serve` pointed at a transported store
  mounted `/v2/` and `/files/` at startup and handed out its blobs before
  a single digest had been recomputed. A destination instance holding a
  medium now withholds both surfaces until a verification clears it, and
  says so: `403` with the taxonomy's what/cause/action in the shape each
  surface's clients understand — the OCI error envelope for `docker` and
  `helm`, plain text for `apt` and `dnf` — naming the medium and the
  screen that opens the gate, never a `404` and never a silent `503`. The
  instance stays live and ready, because its interface is what an operator
  needs in order to verify, and `/readyz` states in its body which
  surfaces are closed. The gate opens on a whole medium and on nothing
  else: R-19 made the *push* decision per recipe, but `/v2/` hands out
  blobs and a blob a blocked recipe reaches is exactly the content that
  failed. A passthrough instance and a source-side mirror — whose store
  carries a manifest because it wrote one — are unaffected. There is no
  setting that serves an unverified medium, and no verdict is cached
  across a restart. Locked at both levels, each played fallible first: the
  middleware against the real registry and file handlers, and the wiring
  against the instance `runServe` actually starts — plus the counter-test
  a guard needs in order not to be too wide, which pins the shape crucible
  scenario m1 relies on: a store filled through `/v2/` and carried across
  the gap serves normally, because it never went through a mirror
  synchronization and carries no media manifest.

### Changed

- `tobby export` takes its destination as a POSITIONAL argument
  (`tobby export /media/usb/payload.tar`), symmetric with `tobby import
  <path>`, and `--output` names the report format there as everywhere
  else. ADR-0006 is amended in place: the spelling of a flag is not the
  decision it carries, but the document has to stay true.
- `tobby export --json` and `tobby import --json` become `--output json`,
  the one spelling of the contract. Both commands landed in this
  unreleased series, so no published interface changes.
- The audit record of `tobby user add` and `tobby user passwd` moves from
  standard output to standard error, where every other structured log of
  the CLI already goes. Stdout now carries the report, and a log record
  interleaved with a JSON document makes both unparseable. Collecting the
  trail means redirecting stderr, as `tobby fileset pack` already
  required.

### Security

- An imported layout is treated as untrusted data (NFR-011): archive
  entries are matched against the three shapes the image-spec defines
  before they are read, links, absolute paths and traversals are refused
  naming the offending entry, blobs are addressed by the digest they must
  hash to rather than by any name the archive supplied, and compressed
  archives are refused rather than inflated.
- Three error-taxonomy entries for the serving gate above: `TBY-MED-030`
  (not verified yet), `TBY-MED-031` (a verification is already walking the
  medium) and `TBY-MED-032` (verified and not whole), documented in both
  languages in the published error reference.

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
