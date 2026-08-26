---
title: Errors and troubleshooting (TBY-*)
description: The full error taxonomy — what happened, probable cause, corrective action per code, whether it is fixable offline, and what it blocks.
sidebar:
  order: 4
---

Every user-visible Tobby error carries a short stable code (`TBY-<area>-<nnn>`)
and a structured message — what happened, probable cause, corrective action —
rendered identically by the web UI, the CLI and the API (as RFC 9457 problem
documents). Codes are part of the product contract: **a code is never
renumbered or reused**, and its class decides the
[CLI exit code](../../reference/cli/#exit-codes).

This page is generated from the taxonomy catalog embedded in the binary
(`internal/taxonomy`). Each code has its own heading, so the anchor
`#tby-reg-003` is stable — the same anchors the in-instance troubleshooting
guide (`/help#TBY-REG-003`) resolves.

:::note[Upcoming — milestone 5]
The media verification verdicts (TBY-MED) are documented below. The
embedded `/help` troubleshooting guide (R-05) and the remaining
media-journey codes (export, pre-flight) ship with the rest of milestone 5,
on these same anchors. Track it on the
[project status](../../discover/status/) page.
:::

**How to read each entry:**

- **Fixable offline** — whether an operator in an isolated zone can resolve
  the condition with local means only (configuration, disk, accounts), or
  whether the fix requires reaching a source registry or the upstream
  authoring pipeline.
- **Blocks** — the blast radius: the whole instance (startup refusal), one
  task or recipe, or just the request at hand.
- **Override** — policy refusals are lifted by changing the audited
  configuration that enforces them (allowlist, trust scopes, accounts);
  verification failures cannot be overridden at all. The only runtime
  overrides are admin-only and audited, and both sit at media import: the
  zone mismatch (TBY-MED-006) and the staleness guard (TBY-MED-007).

## Authentication and accounts (TBY-AUTH)

### TBY-AUTH-001

- **What happened:** the instance refuses to start: no local account is
  configured.
- **Probable cause:** no administrator account has been created on this
  instance yet, or its state directory was reset.
- **Corrective action:** on the instance host, run
  `tobby user add --role admin <name>` (the tool computes the password
  hash), then start the instance again. Tobby never starts with an open UI.
- **Fixable offline:** yes · **Blocks:** the whole instance (secure-by-default startup refusal — policy class, exit 3)

### TBY-AUTH-002

- **What happened:** authentication failed.
- **Probable cause:** the credentials are unknown or the password is wrong.
  The message is deliberately parameter-free: it never reveals whether the
  account exists.
- **Corrective action:** check the account name and password, then try
  again. Accounts are managed on the host with `tobby user`.
- **Fixable offline:** yes · **Blocks:** the sign-in attempt only

### TBY-AUTH-003

- **What happened:** the action is not allowed for your role.
- **Probable cause:** it requires the *\<role\>* role.
- **Corrective action:** sign in with an account holding that role, or ask
  an administrator to grant it.
- **Fixable offline:** yes · **Blocks:** the action, for this session (policy class, exit 3)

### TBY-AUTH-004

- **What happened:** the form could not be submitted safely.
- **Probable cause:** the anti-forgery token is missing or has expired —
  the page was probably left open for a long time.
- **Corrective action:** the form has been reloaded with a fresh token:
  submit it again.
- **Fixable offline:** yes · **Blocks:** the submitted request only

### TBY-AUTH-005

- **What happened:** your session has expired.
- **Probable cause:** no activity for longer than the session lifetime
  (`auth.sessionTTL`, default 12h), or the instance restarted — sessions
  live in memory.
- **Corrective action:** sign in again; you will be returned to the page
  you were on.
- **Fixable offline:** yes · **Blocks:** the session only

### TBY-AUTH-006

- **What happened:** the current password is wrong (self-service password
  change).
- **Probable cause:** the current password sent with the change request
  does not match the account's password.
- **Corrective action:** type your current password again, then retry. If
  it is lost, an administrator can set a new one on the instance host with
  `tobby user passwd <name>`.
- **Fixable offline:** yes · **Blocks:** the password change only

### TBY-AUTH-007

- **What happened:** the new password was rejected.
- **Probable cause:** it is empty, identical to the current password, or
  its confirmation does not match.
- **Corrective action:** choose a non-empty password different from the
  current one, type the same value in both new-password fields, submit
  again.
- **Fixable offline:** yes · **Blocks:** the password change only

### TBY-AUTH-008

- **What happened:** the account could not be created or updated.
- **Probable cause:** the login is empty, the role is not one of `viewer`,
  `operator`, `admin`, or the password is empty or mistyped in the
  confirmation field.
- **Corrective action:** give a non-empty login, pick one of the three
  roles, type the same non-empty password in both fields, submit again.
- **Fixable offline:** yes · **Blocks:** the account operation only

### TBY-AUTH-009

- **What happened:** the account *\<name\>* already exists.
- **Probable cause:** this instance already holds a local account with that
  login; logins are unique.
- **Corrective action:** choose another login, or manage the existing
  account from the accounts screen — role change and password reset need no
  second account.
- **Fixable offline:** yes · **Blocks:** the account creation only

### TBY-AUTH-010

- **What happened:** no account named *\<name\>* on this instance.
- **Probable cause:** the account was removed, or the login is misspelled —
  the screen you acted from may predate the removal.
- **Corrective action:** reload the accounts screen for the current list,
  then retry on an account it shows.
- **Fixable offline:** yes · **Blocks:** the account operation only

### TBY-AUTH-011

- **What happened:** refused: *\<name\>* is the last administrator of this
  instance.
- **Probable cause:** removing it, or demoting it, would leave nobody able
  to manage the instance — and an instance without any account refuses to
  start at all.
- **Corrective action:** create a second admin account first
  (`tobby user add --role admin <name>` on the host also works), then
  retry.
- **Fixable offline:** yes · **Blocks:** the removal/demotion only (policy class, exit 3)

### TBY-AUTH-012

- **What happened:** too many failed authentication attempts from your
  network address; further attempts are temporarily refused (HTTP 429).
- **Probable cause:** repeated wrong credentials from the same origin. Each
  failed check costs a deliberately expensive argon2id computation, so the
  instance throttles origins that keep failing rather than burn CPU for
  them.
- **Corrective action:** wait a moment, check the credential (account
  password or token secret), then try again. Behind a shared egress
  address, another client may be misconfigured — the audit trail lists the
  failed attempts and the account names they claimed.
- **Fixable offline:** yes · **Blocks:** the network origin, temporarily

## Configuration (TBY-CFG)

### TBY-CFG-001

- **What happened:** the configuration is invalid.
- **Probable cause:** stated verbatim in the message (the violated
  constraint).
- **Corrective action:** fix the reported setting (precedence: flags, then
  `TOBBY_*` environment variables, then the YAML file), check the result
  with `tobby config dump`, then restart. See the
  [configuration reference](../../reference/configuration/).
- **Fixable offline:** yes · **Blocks:** the whole instance (startup refusal) or the command that loaded the configuration

### TBY-CFG-002

- **What happened:** the instance refuses to start — a secret file is
  configured inside the transportable store.
- **Probable cause:** one of `state.root`, `registries.credentialsFile` or
  `server.tls.keyFile` resolves under `storage.root`. The store is handed
  to a courier and plugged into a machine in another zone, so everything
  under it is assumed to be read by someone else (NFR-020).
- **Corrective action:** move each listed file outside the store — the
  state directory is its home — update the setting, then start again. The
  check resolves through the filesystem, so a path that reaches the store
  through a symbolic link counts as inside; the message reports the
  resolved path that decided.
- **Fixable offline:** yes · **Blocks:** the whole instance (startup refusal)

## Outbound network and TLS (TBY-NET)

### TBY-NET-001

- **What happened:** the outbound proxy configuration is unusable, so the
  instance refuses to start.
- **Probable cause:** *\<setting\>* is set to *\<proxy\>*, which is not a
  usable forward-proxy URL (expected `http://` or `https://` with a host).
- **Corrective action:** correct the setting to the form
  `http://proxy.example.com:3128`, keeping credentials out of the URL —
  they belong in `network.proxy.username` and `network.proxy.password`,
  which never appear in logs or in `tobby config dump`. Then restart.
- **Fixable offline:** yes · **Blocks:** the whole instance (startup refusal)

### TBY-NET-002

- **What happened:** a configured certificate authority could not be
  loaded.
- **Probable cause:** *\<source\>* is unreadable, holds no PEM
  `CERTIFICATE` block, or adds no authority the instance did not already
  trust.
- **Corrective action:** check that the file exists, is readable by the
  instance, and contains the CA certificate in PEM form
  (`openssl x509 -in <file> -noout -subject` must print a subject), then
  restart. Trusting a private authority is the supported way to reach an
  internal registry; there is no setting that disables certificate
  verification.
- **Fixable offline:** yes · **Blocks:** the whole instance (startup refusal)

### TBY-NET-003

- **What happened:** the listener certificate could not be used, so the
  instance refuses to serve.
- **Probable cause:** *\<source\>* is missing, unreadable, not a PEM
  certificate/key pair, or the key does not match the certificate.
- **Corrective action:** check `server.tls.certFile` and
  `server.tls.keyFile`: both must be readable PEM files forming one pair.
  Remove both to have Tobby generate a self-signed certificate instead —
  its fingerprint is printed at startup. Then restart.
- **Fixable offline:** yes · **Blocks:** the whole instance (startup refusal)

### TBY-NET-004

- **What happened:** a certificate replacement submitted from the
  administration surfaces was refused; the instance keeps serving the
  certificate it already had.
- **Probable cause:** stated verbatim in the message (mismatched pair,
  expired certificate, unconfigured file paths…).
- **Corrective action:** submit a PEM certificate and the matching private
  key, still valid, on an instance whose `server.tls.certFile` and
  `server.tls.keyFile` are configured. Nothing was written: the listener is
  unaffected.
- **Fixable offline:** yes · **Blocks:** the submitted replacement only — the instance keeps serving

## Recipe and retriever validation (TBY-VAL)

### TBY-VAL-001

- **What happened:** the recipe or retriever file is invalid.
- **Probable cause:** in *\<file\>*, at *\<path\>*: the violated constraint
  is named.
- **Corrective action:** fix the field at that path so it satisfies the
  constraint, then submit the file again. The grammar is normative on the
  [recipe specification site](https://tobby-fetch.github.io/recipe-spec/).
- **Fixable offline:** yes · **Blocks:** the submitted file / the recipe concerned

## Source registry access (TBY-REG)

### TBY-REG-001

- **What happened:** the reference could not be parsed.
- **Probable cause:** *\<reference\>* is not a valid image or chart
  reference.
- **Corrective action:** use the form `registry/repository:tag` or
  `registry/repository@sha256:…` — for example `docker.io/library/redis:7.2`.
- **Fixable offline:** yes · **Blocks:** the operation using that reference

### TBY-REG-002

- **What happened:** the source registry could not be reached.
- **Probable cause:** no network route to *\<host\>*, or the registry is
  down (DNS, proxy, or firewall on the path).
- **Corrective action:** check connectivity to the host from the instance
  host and the proxy settings, then retry.
- **Fixable offline:** no (needs the network path to the source) · **Blocks:** the task concerned; retried with bounded backoff

### TBY-REG-003

- **What happened:** the source registry refused authentication.
- **Probable cause:** credentials for *\<host\>* are missing or expired.
- **Corrective action:** set `registries.credentialsFile` content for that
  host in the configuration, then retry the import.
- **Fixable offline:** no (the configuration fix is local, but the retry needs the source registry) · **Blocks:** the task concerned

### TBY-REG-004

- **What happened:** the remote inspection timed out.
- **Probable cause:** *\<host\>* did not answer within *\<timeout\>*.
  Deliberately distinct from "unreachable".
- **Corrective action:** retry; if it persists, check the network path or
  raise `import.inspectTimeout` in the configuration.
- **Fixable offline:** no (needs the source to answer) · **Blocks:** the inspection/import concerned

### TBY-REG-005

- **What happened:** the reference does not exist on the source registry.
- **Probable cause:** *\<reference\>* was not found — wrong name or tag, or
  it was deleted upstream.
- **Corrective action:** check the repository name and the tag on the
  source registry, then correct the reference.
- **Fixable offline:** no (the truth lives on the source registry) · **Blocks:** the task concerned

### TBY-REG-006

- **What happened:** no available version satisfies the requested
  expression.
- **Probable cause:** for *\<reference\>*, the expression *\<constraint\>*
  matches none of the available tags.
- **Corrective action:** check the version expression against the tags
  actually published (semver constraints only consider semver-parseable
  tags). Tobby never falls back silently to another version.
- **Fixable offline:** no (resolution needs the source's tag list) · **Blocks:** the recipe/ingredient concerned

### TBY-REG-007

- **What happened:** the source served an unusable partial response while a
  large transfer was being resumed.
- **Probable cause:** for *\<reference\>*: a 206 starting at the wrong
  byte, a `Content-Range` contradicting the manifest, a refused range, or
  content that changed between attempts. The source registry, or a cache in
  front of it, does not honor byte ranges consistently. Operational, not a
  verification verdict: nothing was proven wrong about the content — the
  conversation about it broke.
- **Corrective action:** retry the task: the transfer restarts from the
  last verified position, or from the beginning if the source content
  changed. If it recurs on the same source, set
  `transfer.resumeThreshold: 0` to disable in-blob resumption, and check
  any caching proxy on the path.
- **Fixable offline:** no (source-side; the `resumeThreshold: 0` workaround is local) · **Blocks:** the task concerned

## Policy refusals (TBY-POL)

### TBY-POL-001

- **What happened:** the registry is not on the allowlist.
- **Probable cause:** *\<host\>* is not among the allowed source or
  destination registries; the transfer was refused before any data moved.
- **Corrective action:** if this registry is legitimate, add it to
  `registries.allowlist` in the configuration; the change is audit-logged.
- **Fixable offline:** yes · **Blocks:** every transfer touching that host, pre-data (policy class, exit 3); lifted by an audited configuration change

### TBY-POL-002

- **What happened:** this content cannot be removed individually.
- **Probable cause:** *\<repository\>* is managed by the named recipes:
  removing it here would be undone by the next synchronization.
- **Corrective action:** remove the managing recipe instead — its exclusive
  content is garbage-collected with it. Only unit-imported content is
  individually removable.
- **Fixable offline:** yes · **Blocks:** the removal request only (policy class, exit 3)

### TBY-POL-003

- **What happened:** this content cannot be removed from here.
- **Probable cause:** *\<repository\>* was pushed through the standard
  registry API (`/v2/`) by an external client: its provenance is neither a
  recipe nor a unit import.
- **Corrective action:** individual removal covers unit-imported content
  only. Manage seeded content with the standard registry tooling that
  pushed it.
- **Fixable offline:** yes · **Blocks:** the removal request only (policy class, exit 3)

### TBY-POL-004

- **What happened:** this recipe version is already published, with
  different content.
- **Probable cause:** *\<reference\>* already points at a published digest;
  the document offered would publish a different one. A cooked recipe is
  immutable.
- **Corrective action:** publish the change under a new `metadata.version`
  and tag. Republishing a version onto different content would silently
  change what zones already resolved. Publishing the identical document
  twice is a no-op, not this error.
- **Fixable offline:** no (a new signed version comes from the authoring pipeline) · **Blocks:** the publication only (policy class, exit 3); never overridable

## Signature and digest verification (TBY-SIG)

### TBY-SIG-001

- **What happened:** the recipe signature could not be verified.
- **Probable cause:** no configured trust root validates the signature of
  *\<recipe\>* (the tried key fingerprints are listed).
- **Corrective action:** check that the zone's trust roots include the key
  that signed this recipe (see
  [Signatures, trust roots and allowlist](../../security/content-trust/)). An
  unverified recipe is never admitted.
- **Fixable offline:** yes, when the right public key is available locally (trust roots are destination configuration); a genuinely unsigned or wrongly signed recipe must be re-signed upstream · **Blocks:** the recipe concerned (verification class, exit 4); never overridable

### TBY-SIG-002

- **What happened:** a pinned digest does not match the fetched content.
- **Probable cause:** *\<reference\>* pins one digest but the registry
  served another — the content changed or was tampered with.
- **Corrective action:** do not force the transfer. Verify the source
  registry and the recipe; if the change is legitimate, a re-signed recipe
  pinning the new digest is required.
- **Fixable offline:** no · **Blocks:** the ingredient/recipe concerned (verification class, exit 4); never overridable

### TBY-SIG-003

- **What happened:** the artifact's type does not match the recipe's
  declaration.
- **Probable cause:** *\<reference\>* declares one `artifactType` but the
  registry served another — the tag may have been reused for different
  content (anti tag-reuse and repository-confusion check).
- **Corrective action:** verify the source repository. If the type change
  is legitimate, update and re-sign the recipe; otherwise treat the source
  as compromised.
- **Fixable offline:** no · **Blocks:** the ingredient/recipe concerned (verification class, exit 4); never overridable

## Destination limits (TBY-DST)

### TBY-DST-001

- **What happened:** the destination registry cannot store this reference.
- **Probable cause:** *\<reference\>* exceeds a destination limit (the
  limit is named — typically a path-length or naming constraint).
- **Corrective action:** shorten the relocated path or adjust the
  destination naming (`destination.basePath`, `storage.basePrefix`); the
  refusal happened before any push.
- **Fixable offline:** yes · **Blocks:** the push of that reference only

## Helm charts (TBY-CHT)

### TBY-CHT-001

- **What happened:** the Helm chart is missing an embedded dependency.
- **Probable cause:** *\<chart\>* declares the dependency *\<dependency\>*
  but does not embed it under `charts/` — it cannot deploy offline.
- **Corrective action:** repackage the chart with its dependencies embedded
  (`helm dependency build`, then `helm package`), publish it, and retry the
  import.
- **Fixable offline:** no (the chart must be repackaged where it is built) · **Blocks:** the import of that chart (verification class, exit 4)

## Local store and state (TBY-STO)

### TBY-STO-001

- **What happened:** the local store could not be read.
- **Probable cause:** stated verbatim (unmounted volume, permissions,
  I/O error…).
- **Corrective action:** check that the storage root exists, is mounted,
  and is readable by the instance, then retry.
- **Fixable offline:** yes · **Blocks:** the operation concerned; a persistent condition affects the whole instance

### TBY-STO-002

- **What happened:** writing to the local store failed.
- **Probable cause:** stated verbatim — most often free space or
  permissions.
- **Corrective action:** check free space and permissions on the storage
  root, then retry the operation.
- **Fixable offline:** yes · **Blocks:** the operation concerned; a persistent condition affects the whole instance

### TBY-STO-003

- **What happened:** writing the partial download to the state directory
  failed. Deliberately distinct from TBY-STO-002: the state directory and
  the store have different owners, different sizing and different fixes.
- **Probable cause:** the named path could not be written — most often no
  free space, or permissions the instance user does not hold on the state
  root.
- **Corrective action:** free space on the state directory (it temporarily
  holds one copy of each resumable blob) or fix its permissions, then retry
  the task. Lower `transfer.resumeThreshold` to make fewer blobs resumable,
  or set it to `0` to stream every blob straight to the store without
  spooling.
- **Fixable offline:** yes · **Blocks:** the resumable transfers concerned

### TBY-STO-004

- **What happened:** the operation was refused before it started — the
  target does not have enough free space (FR-055).
- **Probable cause:** the projected write exceeds the target's free space
  minus the configured safety margin (`preflight.safetyMarginPercent`,
  default 10 %). The message states the exact shortfall in bytes.
- **Corrective action:** free at least the stated number of bytes on the
  target, point the operation at a larger volume, or remove content that is
  no longer referenced. Lowering `preflight.safetyMarginPercent` only makes
  sense if you accept filling the volume; `preflight.disabled: true` removes
  the check entirely and is announced at startup.
- **Fixable offline:** yes · **Blocks:** the synchronization or export concerned; nothing is written

### TBY-STO-005

- **What happened:** the target filesystem cannot hold a file this large
  (FR-055).
- **Probable cause:** the target is formatted with a filesystem whose
  single-file ceiling is below the largest file the operation would
  write — typically FAT32, whose limit is 4 GiB minus one byte. Single-tar
  export archives count as one file. The same code is raised when the
  condition arrives mid-write instead of at the pre-flight check: a medium
  swapped between the two, or a filesystem this build could not identify.
- **Corrective action:** reformat the medium with a filesystem that has no
  such limit (exFAT, NTFS, ext4, XFS), or split the transfer so that no
  single file exceeds the limit.
- **Fixable offline:** yes · **Blocks:** the synchronization or export concerned; on a mid-write failure the store is left intact

## Removable-media transport (TBY-MED)

The medium is a store that changed hands, so everything it says about
itself is a claim until this side has re-hashed it. Four conditions block
a medium as a whole; everything else is decided delivery by delivery, so
a partially damaged medium still hands over its intact recipes.

### TBY-MED-001

- **What happened:** the medium carries no media manifest.
- **Probable cause:** `meta/media.json` is absent — the store was not
  produced by a completed mirror synchronization, or the copy onto the
  medium was partial.
- **Corrective action:** re-copy the store from the source instance, or
  re-run the mirror synchronization that produces it.
- **Fixable offline:** yes · **Blocks:** the whole medium, no override (verification class, exit 4)

### TBY-MED-002

- **What happened:** the media manifest cannot be read.
- **Probable cause:** it is truncated, unparseable, or internally
  inconsistent — a path escaping the store, a duplicated inventory entry,
  a repository name that is not one.
- **Corrective action:** re-copy the store from the source instance and
  verify again.
- **Fixable offline:** yes · **Blocks:** the whole medium, no override (verification class, exit 4)

### TBY-MED-003

- **What happened:** the media manifest uses an unsupported format version.
- **Probable cause:** the medium declares a manifest layout this build does
  not read; both versions are named.
- **Corrective action:** use a Tobby release matching the medium on this
  side, or re-produce the medium with the release running here.
- **Fixable offline:** yes · **Blocks:** the whole medium (verification class, exit 4)

### TBY-MED-004

- **What happened:** the medium uses an unsupported store format version.
- **Probable cause:** the store layout on the medium is of another major
  series; both versions are named.
- **Corrective action:** use a matching Tobby release, or move the content
  through the OCI image layout with standard tooling.
- **Fixable offline:** yes · **Blocks:** the whole medium (verification class, exit 4)

### TBY-MED-005

- **What happened:** the medium's recipe graph does not match its inventory.
- **Probable cause:** `meta/recipes.json` was altered after the manifest was
  written — the list of what the medium delivers has changed.
- **Corrective action:** re-copy the store from the source instance. The
  recipe graph is what every per-recipe verdict is computed from.
- **Fixable offline:** yes · **Blocks:** the whole medium, no override (verification class, exit 4)

### TBY-MED-006

- **What happened:** the medium is addressed to another zone.
- **Probable cause:** the manifest names a zone this instance does not
  serve; both are named.
- **Corrective action:** check that this is the medium intended for this
  zone. An administrator may override the refusal; the override is
  recorded in the audit journal.
- **Fixable offline:** yes · **Blocks:** the whole medium, admin override available (policy class, exit 3)

### TBY-MED-007

- **What happened:** the medium is older than the last one imported for this
  zone.
- **Probable cause:** its resolution timestamp precedes the one recorded for
  the zone's last completed import; both timestamps and the medium are
  named. An anti-accident guard, not a security control — the manifest is
  unsigned.
- **Corrective action:** check that you plugged in the current medium. An
  administrator may override the refusal — to restore an older delivery on
  purpose, for instance; the override is audited.
- **Fixable offline:** yes · **Blocks:** the whole medium, admin override available (policy class, exit 3)

### TBY-MED-010

- **What happened:** a file the recipe needs is missing from the medium.
- **Probable cause:** the copy was partial, or the file was removed.
- **Corrective action:** re-copy the store from the source instance.
- **Fixable offline:** yes · **Blocks:** that recipe whole, no override (verification class, exit 4)

### TBY-MED-011

- **What happened:** a file on the medium has the wrong size.
- **Probable cause:** it was truncated or altered after the manifest was
  written; expected and actual sizes are named.
- **Corrective action:** re-copy the store from the source instance.
- **Fixable offline:** yes · **Blocks:** the recipe reaching that file, no override (verification class, exit 4)

### TBY-MED-012

- **What happened:** a file on the medium does not match its recorded digest.
- **Probable cause:** the content was corrupted or tampered with in
  transport; both digests are named.
- **Corrective action:** re-copy the store from the source instance.
- **Fixable offline:** yes · **Blocks:** the recipe reaching that file, no override (verification class, exit 4)

### TBY-MED-013

- **What happened:** a file the recipe needs is absent from the inventory.
- **Probable cause:** the manifest does not cover everything the recipes
  reach, so it cannot vouch for that file.
- **Corrective action:** re-produce the medium from the source instance.
- **Fixable offline:** yes · **Blocks:** that recipe whole, no override (verification class, exit 4)

### TBY-MED-014

- **What happened:** a manifest on the medium cannot be read.
- **Probable cause:** the named file is not readable as an OCI manifest or
  index, so what the recipe delivers cannot be established.
- **Corrective action:** re-copy the store from the source instance.
- **Fixable offline:** yes · **Blocks:** that recipe whole (verification class, exit 4)

### TBY-MED-015

- **What happened:** a blob on the medium is stored under the wrong digest.
- **Probable cause:** its bytes do not hash to the digest its own path
  claims — the content-addressed store disagrees with itself, whatever the
  inventory says.
- **Corrective action:** re-copy the store from the source instance.
- **Fixable offline:** yes · **Blocks:** the recipe reaching that blob, no override (verification class, exit 4)

### TBY-MED-020

- **What happened:** a file on the medium is not covered by the inventory.
- **Probable cause:** it was added after the manifest was written.
- **Corrective action:** none needed to proceed — extraneous content is
  never pushed. Investigate how it got there if you did not put it there.
- **Fixable offline:** yes · **Blocks:** nothing (reported only)

### TBY-MED-021

- **What happened:** a file on the medium is reached by no recipe.
- **Probable cause:** leftover content from an earlier delivery, most often.
- **Corrective action:** none needed to proceed — content reachable from no
  recipe is never pushed. Prune the source store to carry less.
- **Fixable offline:** yes · **Blocks:** nothing (reported only)

### TBY-MED-022

- **What happened:** a bookkeeping file on the medium does not match its
  recorded digest.
- **Probable cause:** the store's own ledgers — other than the recipe graph,
  which blocks globally — were altered after the manifest was written.
- **Corrective action:** re-copy the store from the source instance if you
  did not alter it deliberately. Nothing is pushed out of these files.
- **Fixable offline:** yes · **Blocks:** nothing (reported only)

## Tasks (TBY-TSK)

### TBY-TSK-001

- **What happened:** the task does not exist.
- **Probable cause:** no task with identifier *\<id\>* on this instance —
  wrong link, or the store was reset.
- **Corrective action:** open the task list and follow the link of an
  existing task.
- **Fixable offline:** yes · **Blocks:** the request only

## Instance (TBY-SRV)

### TBY-SRV-001

- **What happened:** an internal error occurred.
- **Probable cause:** an unexpected condition interrupted the request;
  details are in the instance logs.
- **Corrective action:** retry; if it persists, search the instance logs
  for the correlation identifier shown with this error (see
  [Metrics and logs](../../reference/metrics-logs/)).
- **Fixable offline:** yes (diagnosis is local) · **Blocks:** the request only

### TBY-SRV-002

- **What happened:** this resource does not exist.
- **Probable cause:** the address is wrong, or the content was removed.
- **Corrective action:** go back to the content browser or use the search.
- **Fixable offline:** yes · **Blocks:** the request only

### TBY-SRV-003

- **What happened:** the instance is unreachable. A client-side condition
  rendered by the UI shell on transport failure — never served by the
  instance itself; catalogued so this guide documents it.
- **Probable cause:** the network connection dropped, or the instance is
  restarting.
- **Corrective action:** check your network link and retry; the page
  resumes as soon as the instance answers.
- **Fixable offline:** yes · **Blocks:** the browser session's view only
