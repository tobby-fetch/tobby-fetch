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
[process exit code](#exit-codes).

This page is generated from the taxonomy catalog embedded in the binary
(`internal/taxonomy`): what happened, probable cause and corrective action
are, word for word, what the instance prints. Each code has its own
heading, so the anchor `#tby-reg-003` is stable — the same anchors the
in-instance troubleshooting guide (`/help#TBY-REG-003`) resolves.

A check regenerates the page and fails when it differs from the catalog,
in either language — the catalog is the source of truth, this page is its
published rendering.

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

## Exit codes

The class of a code decides the process exit code: it is the command-line
projection of this taxonomy (FR-066, R-08 amendment). **The table below is
produced from the code** (`internal/taxonomy`), and a test fails when the
two differ — in both directions: a code the table does not list, or a row
nothing can produce.

It is covered by the project's semantic-versioning promise: removing a
code or renumbering one is a breaking change. The `Name` column is the
stable machine name of the row; it is part of the contract and is never
translated.

<!-- generated:exit-codes -->
| Code | Name | Meaning |
|---|---|---|
| `0` | `ok` | **Success** — The command did what was asked. |
| `1` | `failure` | **Operational failure** — Network, storage or internal error — everything that failed without a policy or a verification verdict. Every code of the operational class exits here, and so does a command that started a task the instance then failed. |
| `2` | `usage` | **Usage error** — Bad flag, unknown command, or a command asked for something it cannot honour. The message carries a `see 'tobby … --help'` hint naming the command that misparsed. |
| `3` | `policy` | **Policy refusal** — Refused by explicit policy or authorization: registry allow-list, roles, secure-by-default startup refusals, immutable recipe tags, the two waivable media guards. |
| `4` | `verification` | **Verification failure** — A failed integrity or authenticity check: signatures, pinned digests, artifact types, media checksums. The most severe class, and the one no override reopens. |
| `5` | `changes-planned` | **Changes planned** — A side-effect-free run found work to do — `tobby sync --dry-run` over a Retriever with pending changes. A success with something to say, so a gate can branch on it without treating it as a broken build. |
<!-- /generated:exit-codes -->

The [CLI reference](../../reference/cli/#exit-codes) repeats this table
with every command, the `--output json` contract and the non-interactive
mode.

## Index by domain

| Domain | Codes |
|---|---|
| Authentication and accounts (`TBY-AUTH`) | [`TBY-AUTH-001`](#tby-auth-001) … [`TBY-AUTH-019`](#tby-auth-019) (19) |
| Configuration (`TBY-CFG`) | [`TBY-CFG-001`](#tby-cfg-001) … [`TBY-CFG-002`](#tby-cfg-002) (2) |
| Outbound network and TLS (`TBY-NET`) | [`TBY-NET-001`](#tby-net-001) … [`TBY-NET-004`](#tby-net-004) (4) |
| Recipe and retriever validation (`TBY-VAL`) | [`TBY-VAL-001`](#tby-val-001) (1) |
| Source registry access (`TBY-REG`) | [`TBY-REG-001`](#tby-reg-001) … [`TBY-REG-008`](#tby-reg-008) (8) |
| Policy refusals (`TBY-POL`) | [`TBY-POL-001`](#tby-pol-001) … [`TBY-POL-006`](#tby-pol-006) (6) |
| Signature and digest verification (`TBY-SIG`) | [`TBY-SIG-001`](#tby-sig-001) … [`TBY-SIG-003`](#tby-sig-003) (3) |
| Vulnerability scanning (`TBY-SCN`) | [`TBY-SCN-001`](#tby-scn-001) … [`TBY-SCN-028`](#tby-scn-028) (17) |
| Destination limits (`TBY-DST`) | [`TBY-DST-001`](#tby-dst-001) (1) |
| Helm charts (`TBY-CHT`) | [`TBY-CHT-001`](#tby-cht-001) (1) |
| Local store and state (`TBY-STO`) | [`TBY-STO-001`](#tby-sto-001) … [`TBY-STO-006`](#tby-sto-006) (6) |
| Removable-media transport (`TBY-MED`) | [`TBY-MED-001`](#tby-med-001) … [`TBY-MED-034`](#tby-med-034) (23) |
| Store integrity (`TBY-INT`) | [`TBY-INT-001`](#tby-int-001) … [`TBY-INT-006`](#tby-int-006) (6) |
| OCI image layout export and import (`TBY-LAY`) | [`TBY-LAY-001`](#tby-lay-001) … [`TBY-LAY-003`](#tby-lay-003) (3) |
| Packing file sets (`TBY-FIL`) | [`TBY-FIL-001`](#tby-fil-001) … [`TBY-FIL-003`](#tby-fil-003) (3) |
| Consumption aids (`TBY-AID`) | [`TBY-AID-001`](#tby-aid-001) (1) |
| Diagnostic bundle (`TBY-DIA`) | [`TBY-DIA-001`](#tby-dia-001) … [`TBY-DIA-003`](#tby-dia-003) (3) |
| Clock plausibility (`TBY-CLK`) | [`TBY-CLK-001`](#tby-clk-001) … [`TBY-CLK-002`](#tby-clk-002) (2) |
| Instance state directory (`TBY-STA`) | [`TBY-STA-001`](#tby-sta-001) … [`TBY-STA-003`](#tby-sta-003) (3) |
| Tasks (`TBY-TSK`) | [`TBY-TSK-001`](#tby-tsk-001) (1) |
| Instance (`TBY-SRV`) | [`TBY-SRV-001`](#tby-srv-001) … [`TBY-SRV-005`](#tby-srv-005) (5) |

## Authentication and accounts (TBY-AUTH)

### TBY-AUTH-001

- **What happened:** The instance refuses to start: no local account is configured.
- **Probable cause:** No administrator account has been created on this instance yet, or its state directory was reset.
- **Corrective action:** On the instance host, run `tobby user add --role admin <name>` (the tool computes the password hash), then start the instance again. Tobby never starts with an open UI.
- **Class:** policy refusal (exit 3)
- **Fixable offline:** yes · **Blocks:** the whole instance (secure-by-default startup refusal)

### TBY-AUTH-002

- **What happened:** Authentication failed.
- **Probable cause:** The credentials are unknown or the password is wrong.
- **Corrective action:** Check the account name and password, then try again. Accounts are managed on the host with `tobby user`.
- **Note:** The message is deliberately parameter-free: it never reveals whether the account exists.
- **Class:** operational failure (exit 1) · **HTTP status:** 401
- **Fixable offline:** yes · **Blocks:** the sign-in attempt only

### TBY-AUTH-003

- **What happened:** This action is not allowed for your role.
- **Probable cause:** It requires the *\<role\>* role.
- **Corrective action:** Sign in with an account holding the *\<role\>* role, or ask an administrator to grant it.
- **Class:** policy refusal (exit 3) · **HTTP status:** 403
- **Fixable offline:** yes · **Blocks:** the action, for this session

### TBY-AUTH-004

- **What happened:** The form could not be submitted safely.
- **Probable cause:** The anti-forgery token is missing, wrong or expired, or the request came from a page of another site — the page was probably left open for a long time.
- **Corrective action:** Reload the page and submit again. A script or tool calling the API authenticates with a token (Authorization: Bearer), not with the browser session.
- **Note:** Every state-changing request authenticated by the browser session carries the token, UI forms and API calls alike (`X-CSRF-Token` header on the API), and is refused when the browser says it comes from another origin.
- **Class:** operational failure (exit 1) · **HTTP status:** 403
- **Fixable offline:** yes · **Blocks:** the submitted request only

### TBY-AUTH-005

- **What happened:** Your session has expired.
- **Probable cause:** No activity for longer than the idle timeout, the session reached its maximum lifetime, or the instance restarted.
- **Corrective action:** Sign in again; you will be returned to the page you were on.
- **Note:** The idle timeout is `auth.session.idleTimeout` (default 30m) and the absolute lifetime is `auth.sessionTTL` (default 12h). Sessions live in memory, hence the restart case.
- **Note:** Each expiry is recorded as a `session.expire` audit event with its reason, `idle` or `lifetime`.
- **Class:** operational failure (exit 1) · **HTTP status:** 401
- **Fixable offline:** yes · **Blocks:** the session only

### TBY-AUTH-006

- **What happened:** The current password is wrong.
- **Probable cause:** The current password sent with the change request does not match the account's password.
- **Corrective action:** Type your current password again, then retry. If it is lost, an administrator can set a new one on the instance host with `tobby user passwd <name>`.
- **Class:** operational failure (exit 1) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the password change only

### TBY-AUTH-007

- **What happened:** The new password was rejected.
- **Probable cause:** It is empty, identical to the current password, or its confirmation does not match.
- **Corrective action:** Choose a non-empty password different from the current one, type the same value in both new-password fields, then submit again.
- **Class:** operational failure (exit 1) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the password change only

### TBY-AUTH-008

- **What happened:** The account could not be created or updated.
- **Probable cause:** The login is empty, the role is not one of viewer, operator, or admin, or the password is empty or was mistyped in the confirmation field.
- **Corrective action:** Give a non-empty login, pick one of the three roles, and type the same non-empty password in both password fields, then submit again.
- **Class:** operational failure (exit 1) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the account operation only

### TBY-AUTH-009

- **What happened:** The account *\<name\>* already exists.
- **Probable cause:** This instance already holds a local account with that login; logins are unique.
- **Corrective action:** Choose another login, or manage the existing account from the accounts screen — its role can be changed and its password reset without creating a second one.
- **Class:** operational failure (exit 1) · **HTTP status:** 409
- **Fixable offline:** yes · **Blocks:** the account creation only

### TBY-AUTH-010

- **What happened:** No account named *\<name\>* on this instance.
- **Probable cause:** The account was removed, or the login is misspelled — the screen you acted from may predate the removal.
- **Corrective action:** Reload the accounts screen to see the current list, then retry on an account it shows.
- **Class:** operational failure (exit 1) · **HTTP status:** 404
- **Fixable offline:** yes · **Blocks:** the account operation only

### TBY-AUTH-011

- **What happened:** Refused: *\<name\>* is the last administrator of this instance.
- **Probable cause:** Removing it, or taking its admin role away, would leave nobody able to manage the instance — and an instance without an account refuses to start at all.
- **Corrective action:** Create a second admin account first, then retry. On the instance host the same result comes from `tobby user add --role admin <name>`.
- **Class:** policy refusal (exit 3) · **HTTP status:** 409
- **Fixable offline:** yes · **Blocks:** the removal/demotion only

### TBY-AUTH-012

- **What happened:** Too many failed authentication attempts from your network address; further attempts are temporarily refused.
- **Probable cause:** Repeated wrong credentials were presented from the same origin. Each failed check is deliberately expensive to verify, so the instance throttles origins that keep failing rather than burn CPU and memory for them.
- **Corrective action:** Wait a moment, check the credential (account password or token secret), then try again. If this is a shared egress address, another client behind it may be misconfigured — the audit trail lists the failed attempts and the account names they claimed.
- **Note:** The expensive check is an argon2id computation.
- **Class:** operational failure (exit 1) · **HTTP status:** 429
- **Fixable offline:** yes · **Blocks:** the network origin, temporarily

### TBY-AUTH-013

- **What happened:** The API token has expired.
- **Probable cause:** The token was created with an expiry date, and that date has passed. An expired token is refused like a revoked one, and the refusal is audit-logged.
- **Corrective action:** Ask an administrator for a new token (the accounts screen or `POST /api/v1/tokens`), then replace the expired one in the client that presented it.
- **Class:** operational failure (exit 1) · **HTTP status:** 401
- **Fixable offline:** yes · **Blocks:** the request; the token stays refused

### TBY-AUTH-014

- **What happened:** Sign-in through *\<provider\>* failed.
- **Probable cause:** The identity provider's answer did not pass Tobby's checks (state, nonce, signature, issuer or audience) — often a sign-in started in another tab, a clock difference, or a client registered with another callback URL.
- **Corrective action:** Start the sign-in again from this page. If it fails again, an administrator finds the failed check in the instance log under the same correlation identifier; local sign-in stays available meanwhile.
- **Class:** operational failure (exit 1) · **HTTP status:** 401
- **Fixable offline:** yes · **Blocks:** the sign-in attempt

### TBY-AUTH-015

- **What happened:** Sign-in through *\<provider\>* failed.
- **Probable cause:** The SAML response was refused: it was not signed, was already used, was not answering a sign-in this instance started, was outside its validity window, or named another audience or destination.
- **Corrective action:** Start the sign-in again from this page — Tobby only accepts sign-ins it initiated. If it fails again, an administrator finds the refused check in the instance log under the same correlation identifier; local sign-in stays available meanwhile.
- **Class:** operational failure (exit 1) · **HTTP status:** 401
- **Fixable offline:** yes · **Blocks:** the sign-in attempt

### TBY-AUTH-016

- **What happened:** Sign-in through *\<provider\>* is unavailable: the identity provider cannot be reached.
- **Probable cause:** The provider did not answer in time, or the network path to it is cut. Existing sessions and API tokens are not affected.
- **Corrective action:** Use local sign-in meanwhile — it stays available while federation is down, and its use is recorded in the audit log. Check the network path to the provider (proxy, private CA) and its availability; federated sign-in comes back without a restart.
- **Class:** operational failure (exit 1) · **HTTP status:** 502
- **Fixable offline:** depends: local sign-in works offline; federated sign-in needs the provider · **Blocks:** federated sign-in only

### TBY-AUTH-017

- **What happened:** You are signed in at *\<provider\>*, but no Tobby role is granted to you.
- **Probable cause:** None of the groups the identity provider sent for your account is mapped to a role on this instance. Tobby never grants a role by default.
- **Corrective action:** Ask an administrator to add one of your groups to the role mapping of the instance configuration (`roles.viewer`, `roles.operator` or `roles.admin` under the provider's section), or to add you to a group already mapped.
- **Class:** policy refusal (exit 3) · **HTTP status:** 403
- **Fixable offline:** yes · **Blocks:** the sign-in attempt

### TBY-AUTH-018

- **What happened:** Too many sign-ins through *\<provider\>* were started from your network address; starting another is temporarily refused.
- **Probable cause:** Every sign-in started holds a little memory on this instance until it completes, and may contact the identity provider, before anyone is authenticated. The instance bounds how often one network address may start one, so that a flood from one address can never push out the sign-ins other people have in flight.
- **Corrective action:** Wait a few seconds and sign in again — once. If this is a shared egress address, another client behind it may be looping on the sign-in page.
- **Class:** operational failure (exit 1) · **HTTP status:** 429
- **Fixable offline:** yes · **Blocks:** starting a federated sign-in from that address, for a few seconds; local accounts and tokens are not affected

### TBY-AUTH-019

- **What happened:** The name *\<name\>* cannot name a local account or token.
- **Probable cause:** The character `:` is reserved for federated identities, which are keyed `oidc:<subject>` and `saml:<NameID>`; a local account or token named like one would be indistinguishable from it in the audit trail.
- **Corrective action:** Choose a name without `:` (for example a dash or a dot instead).
- **Class:** operational failure (exit 1) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the creation of that account or token

## Configuration (TBY-CFG)

### TBY-CFG-001

- **What happened:** The configuration is invalid.
- **Probable cause:** *\<detail\>*
- **Corrective action:** Fix the reported setting (precedence: flags, then TOBBY_* environment variables, then the YAML file), check the result with `tobby config dump`, then restart.
- **See also:** See the [configuration reference](../../reference/configuration/).
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** the whole instance (startup refusal) or the command that loaded the configuration

### TBY-CFG-002

- **What happened:** The instance refuses to start: a secret file is configured inside the transportable store.
- **Probable cause:** *\<paths\>* resolves under the store root *\<root\>*. The store is handed to a courier and plugged into a machine in another zone, so everything under it is assumed to be read by someone else — registry credentials, TLS private keys, the local user database and its static tokens must never travel on it.
- **Corrective action:** Move each listed file to a directory outside the store — the state directory (`state.root`) is the intended home — update the setting to the new location, then start the instance again. A path resolving inside the store through a symbolic link counts as inside: the reported resolved path is the one that decides.
- **Note:** The settings checked are `state.root`, `registries.credentialsFile` and `server.tls.keyFile`, each against `storage.root` (NFR-020).
- **Class:** policy refusal (exit 3)
- **Fixable offline:** yes · **Blocks:** the whole instance (startup refusal)

## Outbound network and TLS (TBY-NET)

### TBY-NET-001

- **What happened:** The outbound proxy configuration is unusable, so the instance refuses to start.
- **Probable cause:** *\<setting\>* is set to *\<proxy\>*, which is not a usable forward-proxy URL (expected http:// or https:// with a host).
- **Corrective action:** Correct *\<setting\>* to the form http://proxy.example.com:3128, keeping credentials out of the URL — they belong in network.proxy.username and network.proxy.password, which never appear in logs or in `tobby config dump`. Then restart.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** the whole instance (startup refusal)

### TBY-NET-002

- **What happened:** A configured certificate authority could not be loaded.
- **Probable cause:** *\<source\>* is unreadable, holds no PEM CERTIFICATE block, or adds no authority the instance did not already trust.
- **Corrective action:** Check that *\<source\>* exists, is readable by the instance, and contains the CA certificate in PEM form (`openssl x509 -in <file> -noout -subject` must print a subject). Then restart. Trusting a private authority is the supported way to reach an internal registry; there is no setting that disables certificate verification.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** the whole instance (startup refusal)

### TBY-NET-003

- **What happened:** The listener certificate could not be used, so the instance refuses to serve.
- **Probable cause:** *\<source\>* is missing, unreadable, not a PEM certificate/key pair, or the key does not match the certificate.
- **Corrective action:** Check server.tls.certFile and server.tls.keyFile: both must be readable PEM files forming one pair (`openssl x509 -noout -modulus` and `openssl rsa -noout -modulus` must agree). Remove both to have Tobby generate a self-signed certificate instead — its fingerprint is printed at startup. Then restart.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** the whole instance (startup refusal)

### TBY-NET-004

- **What happened:** The submitted certificate pair was refused; the instance keeps serving the certificate it already had.
- **Probable cause:** *\<detail\>*
- **Corrective action:** Submit a PEM certificate and the matching private key (`openssl x509 -noout -pubkey` and `openssl pkey -pubout` must print the same public key), still valid, on an instance whose server.tls.certFile and server.tls.keyFile are configured. Nothing was written: the listener is unaffected.
- **Note:** Raised for a certificate replacement submitted from the administration surfaces.
- **Class:** operational failure (exit 1) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the submitted replacement only — the instance keeps serving

## Recipe and retriever validation (TBY-VAL)

### TBY-VAL-001

- **What happened:** The recipe or retriever file is invalid.
- **Probable cause:** In *\<file\>*, at *\<path\>*: *\<constraint\>*.
- **Corrective action:** Fix the field at *\<path\>* so it satisfies the constraint, then submit the file again.
- **See also:** The grammar is normative on the [recipe specification site](https://tobby-fetch.github.io/recipe-spec/).
- **Class:** operational failure (exit 1) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the submitted file / the recipe concerned

## Source registry access (TBY-REG)

### TBY-REG-001

- **What happened:** The reference could not be parsed.
- **Probable cause:** *\<reference\>* is not a valid image or chart reference.
- **Corrective action:** Use the form registry/repository:tag or registry/repository@sha256:… — for example docker.io/library/redis:8.10.
- **Class:** operational failure (exit 1) · **HTTP status:** 400
- **Fixable offline:** yes · **Blocks:** the operation using that reference

### TBY-REG-002

- **What happened:** The source registry could not be reached.
- **Probable cause:** No network route to *\<host\>*, or the registry is down (DNS, proxy, or firewall on the path).
- **Corrective action:** Check connectivity to *\<host\>* from the instance host and the proxy settings, then retry.
- **Class:** operational failure (exit 1) · **HTTP status:** 502
- **Fixable offline:** no (needs the network path to the source) · **Blocks:** the task concerned; retried with bounded backoff

### TBY-REG-003

- **What happened:** The source registry refused authentication.
- **Probable cause:** Credentials for *\<host\>* are missing or expired.
- **Corrective action:** Add or renew the credentials for *\<host\>* in the file named by `registries.credentialsFile`, then retry the import.
- **Class:** operational failure (exit 1) · **HTTP status:** 502
- **Fixable offline:** no (the configuration fix is local, but the retry needs the source registry) · **Blocks:** the task concerned

### TBY-REG-004

- **What happened:** The inspection timed out.
- **Probable cause:** *\<host\>* did not answer within *\<timeout\>*.
- **Corrective action:** Retry; if it persists, check the network path to *\<host\>* or raise import.inspectTimeout in the configuration.
- **Note:** Deliberately distinct from "unreachable".
- **Class:** operational failure (exit 1) · **HTTP status:** 504
- **Fixable offline:** no (needs the source to answer) · **Blocks:** the inspection/import concerned

### TBY-REG-005

- **What happened:** The reference does not exist on the source registry.
- **Probable cause:** *\<reference\>* was not found — wrong name or tag, or it was deleted upstream.
- **Corrective action:** Check the repository name and the tag on the source registry, then correct the reference.
- **Class:** operational failure (exit 1) · **HTTP status:** 502
- **Fixable offline:** no (the truth lives on the source registry) · **Blocks:** the task concerned

### TBY-REG-006

- **What happened:** No available version satisfies the requested expression.
- **Probable cause:** For *\<reference\>*, the expression *\<constraint\>* matches none of the available tags: *\<detail\>*.
- **Corrective action:** Check the version expression against the tags actually published (semver constraints only consider semver-parseable tags). Tobby never falls back silently to another version.
- **Class:** operational failure (exit 1) · **HTTP status:** 422
- **Fixable offline:** no (resolution needs the source's tag list) · **Blocks:** the recipe/ingredient concerned

### TBY-REG-007

- **What happened:** The source served an unusable partial response while a large transfer was being resumed.
- **Probable cause:** For *\<reference\>*: *\<detail\>*. The source registry, or a cache in front of it, does not honor byte ranges consistently.
- **Corrective action:** Retry the task: the transfer restarts from the last verified position, or from the beginning if the source content changed. If it recurs on the same source, set transfer.resumeThreshold to 0 to disable in-blob resumption for this instance, and check any caching proxy on the path to that registry.
- **Note:** Typical causes: a 206 response starting at the wrong byte, a `Content-Range` contradicting the manifest, a refused range, or content that changed between two attempts.
- **Note:** Operational, not a verification verdict: nothing was proven wrong about the content — the conversation about it broke.
- **Class:** operational failure (exit 1) · **HTTP status:** 502
- **Fixable offline:** no (source-side; the `resumeThreshold: 0` workaround is local) · **Blocks:** the task concerned

### TBY-REG-008

- **What happened:** The source index does not carry a platform the recipe asks for.
- **Probable cause:** For *\<reference\>*, no child of the index matches *\<platforms\>*; the index publishes *\<available\>*. A platform selector is os/arch with an OPTIONAL variant (RECIPE-SPEC §7.1): an omitted variant matches any, a named one must match exactly.
- **Corrective action:** Confront the platforms list of the ingredient with what the source actually publishes (docker manifest inspect, or the inspection report of a unit import) and correct it. Tobby never silently drops a platform that was asked for.
- **Note:** Registries commonly describe their arm64 child as `linux/arm64` with variant `v8`, so a selector naming a variant the source does not publish matches nothing.
- **Class:** operational failure (exit 1) · **HTTP status:** 422
- **Fixable offline:** no (the truth lives in the source index) · **Blocks:** the ingredient concerned

## Policy refusals (TBY-POL)

### TBY-POL-001

- **What happened:** The registry is not on the allowlist.
- **Probable cause:** *\<host\>* is not among the allowed source or destination registries; the transfer was refused before any data moved.
- **Corrective action:** If this registry is legitimate, add *\<host\>* to registries.allowlist in the configuration; the change is audit-logged.
- **Class:** policy refusal (exit 3) · **HTTP status:** 403
- **Fixable offline:** yes · **Blocks:** every transfer touching that host, pre-data; lifted by an audited configuration change

### TBY-POL-002

- **What happened:** This content cannot be removed individually.
- **Probable cause:** *\<repository\>* is managed by *\<recipes\>*: removing it here would be undone by the next synchronization.
- **Corrective action:** Remove the managing recipe instead — its exclusive content is garbage-collected with it. Only unit-imported content is individually removable.
- **Class:** policy refusal (exit 3) · **HTTP status:** 403
- **Fixable offline:** yes · **Blocks:** the removal request only

### TBY-POL-003

- **What happened:** This content cannot be removed from here.
- **Probable cause:** *\<repository\>* was pushed through the standard registry API (/v2/) by an external client: its provenance is neither a recipe nor a unit import.
- **Corrective action:** Individual removal covers unit-imported content only (FR-044). Manage seeded content with the standard registry tooling that pushed it.
- **Class:** policy refusal (exit 3) · **HTTP status:** 403
- **Fixable offline:** yes · **Blocks:** the removal request only

### TBY-POL-004

- **What happened:** This recipe version is already published, with different content.
- **Probable cause:** *\<reference\>* already points at *\<published\>*; the document offered would publish *\<candidate\>*. A cooked recipe is immutable (RECIPE-SPEC §8).
- **Corrective action:** Publish the change under a new metadata.version and tag. Republishing a version onto different content would silently change what zones already resolved.
- **Note:** Publishing the identical document twice is a no-op, not this error.
- **Class:** policy refusal (exit 3) · **HTTP status:** 409
- **Fixable offline:** no (a new signed version comes from the authoring pipeline) · **Blocks:** the publication only; never overridable

### TBY-POL-005

- **What happened:** This instance does not accept unit imports.
- **Probable cause:** It is hardened with *\<setting\>*: content enters its store through verified recipes only, and a unit import or a local packing would be content no recipe vouches for.
- **Corrective action:** Deliver the content through a signed recipe. If unit imports are needed on this instance, an administrator removes *\<setting\>* from its configuration and restarts it.
- **Where:** the import screen and `POST /api/v1/import` with its inspection (HTTP 403), FileSet packing on the screen, the API and `tobby fileset pack`; each refusal is a `hardening.refused` audit event naming the setting
- **Class:** policy refusal (exit 3) · **HTTP status:** 403
- **Fixable offline:** yes · **Blocks:** the import or packing; recipe synchronizations are unaffected

### TBY-POL-006

- **What happened:** This registry is read-only: pushes are refused.
- **Probable cause:** The instance is hardened with *\<setting\>*: its embedded registry serves content and accepts none from clients, whatever their role. Recipe synchronizations still write to the store.
- **Corrective action:** Pull from this registry, or push to the zone registry instead. Seeding this instance by push requires an administrator to remove *\<setting\>* and restart it.
- **Where:** every write to `/v2/` — blob upload, chunk, commit, mount, manifest put, deletion — for every client and role, administrators included, as the OCI `DENIED` error (HTTP 403) whose message starts with the code, which `docker`, `helm`, `oras` and `skopeo` print; each refusal is a `hardening.refused` audit event naming the setting
- **Class:** policy refusal (exit 3) · **HTTP status:** 403
- **Fixable offline:** yes · **Blocks:** the push; pulls and recipe synchronizations are unaffected

## Signature and digest verification (TBY-SIG)

### TBY-SIG-001

- **What happened:** The recipe signature could not be verified.
- **Probable cause:** No trust root allowed by the trust scope *\<scope\>* validates the signature of *\<recipe\>* (keys tried: *\<fingerprints\>*). A key that is valid for another scope does not apply inside this one. The signature does verify against the trust root *\<reserved\>*, reserved to the trust scope *\<reservedto\>*.
- **Corrective action:** The key that signed this recipe is reserved to the trust scope *\<reservedto\>*: if the recipe must be admitted under that key, add its repository to that scope's repositories (trust.scopes[].repositories); otherwise have it re-signed with a key that applies here. An unverified recipe is never admitted.
- **Ingredients:** an ingredient's own signature raises this code only on an instance that sets `trust.requireIngredientSignatures: true`. The subject then reads *ingredient \<name\> of \<recipe\> (\<repository\>@\<digest\>)*, the fingerprints are those the ingredient's applicable roots offered (or "no signature artifact found"), and the whole recipe is withheld. Without that setting, an ingredient signature that does not verify is recorded and blocks nothing.
- **Note:** When every configured root is reserved to a scope, nothing is tried and the message says so.
- **See also:** [Signatures, trust roots and allowlist](../../security/content-trust/).
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** yes, when the right public key is available locally (trust roots are destination configuration); a genuinely unsigned or wrongly signed recipe must be re-signed upstream · **Blocks:** the recipe concerned; never overridable

### TBY-SIG-002

- **What happened:** A pinned digest does not match the fetched content.
- **Probable cause:** *\<reference\>* pins *\<expected\>* but the registry served *\<actual\>* — the content changed or was tampered with.
- **Corrective action:** Do not force the transfer. Verify the source registry and the recipe; if the change is legitimate, a re-signed recipe pinning the new digest is required.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** no · **Blocks:** the ingredient/recipe concerned; never overridable

### TBY-SIG-003

- **What happened:** The artifact's type does not match the recipe's declaration.
- **Probable cause:** *\<reference\>* declares artifactType *\<expected\>* but the registry served *\<actual\>* — the tag may have been reused for different content.
- **Corrective action:** Verify the source repository. If the type change is legitimate, update and re-sign the recipe; otherwise treat the source as compromised.
- **Note:** This is the check against tag reuse and repository confusion.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** no · **Blocks:** the ingredient/recipe concerned; never overridable

## Vulnerability scanning (TBY-SCN)

### TBY-SCN-001

- **What happened:** The vulnerability scanner is unavailable: content is not being scanned.
- **Probable cause:** *\<detail\>*
- **Corrective action:** Make the scanner available for this platform: in the container image it is bundled; on a Linux or Windows host, synchronize the tobby-system recipe that carries it; on macOS, install trivy on the PATH. Under `scan.mode: block`, nothing that could not be scanned is pushed until then.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes, once the tobby-system recipe has crossed · **Blocks:** scanning; under block mode, every push

### TBY-SCN-002

- **What happened:** The scanner binary at *\<path\>* does not match its pinned digest and was not executed.
- **Probable cause:** Expected *\<expected\>*, found *\<actual\>*: the file was replaced or damaged after it was pinned.
- **Corrective action:** Restore the binary from the tobby-system recipe (or from the pinned release), or update `scan.trivy.digest` if the change is deliberate. Content is reported not scanned until then.
- **Class:** verification failure (exit 4)
- **Fixable offline:** yes · **Blocks:** scanning

### TBY-SCN-003

- **What happened:** No vulnerability database is available: content is not being scanned.
- **Probable cause:** The store holds no database under *\<repository\>*.
- **Corrective action:** Synchronize the tobby-system recipe that carries the database first — through the mirror medium in an isolated zone. Media verification does not need the database.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes, once the tobby-system recipe has crossed · **Blocks:** scanning; under block mode, every push

### TBY-SCN-004

- **What happened:** The vulnerability database is *\<age\>* old, beyond the *\<max\>* this instance accepts.
- **Probable cause:** The database has not been refreshed recently enough: findings published since it was built are unknown to the scanner.
- **Corrective action:** Synchronize the tobby-system recipe to bring a fresh database — through the next medium in an isolated zone. Under `scan.maxDbAgeAction: block`, items scanned with a stale database are not pushed until then.
- **Class:** policy refusal (exit 3) · **HTTP status:** 422
- **Fixable offline:** yes, once a fresh database has crossed · **Blocks:** nothing under warn; the item's push under block

### TBY-SCN-005

- **What happened:** *\<reference\>* violates the vulnerability policy and was not pushed.
- **Probable cause:** Findings at or above the threshold of *\<threshold\>* (set at the *\<level\>* level) are not covered by any exception: *\<violations\>*.
- **Corrective action:** Publish a fixed version upstream and update the recipe, or — if the risk is accepted in this zone — list the CVE as an exception in the Retriever's `spec.scanPolicy` or in `scan.exceptions`, with its justification.
- **Class:** policy refusal (exit 3) · **HTTP status:** 422
- **Fixable offline:** no: the fix comes from upstream, or from an exception decided by the zone · **Blocks:** the item and its recipe

### TBY-SCN-006

- **What happened:** *\<reference\>* violates the vulnerability policy; it was pushed because the policy is advisory.
- **Probable cause:** Findings at or above the threshold of *\<threshold\>* (set at the *\<level\>* level) are not covered by any exception: *\<violations\>*.
- **Corrective action:** Review the findings. To refuse such content instead of reporting it, set `scan.mode: block`; to accept a specific CVE in this zone, list it as an exception with its justification.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing

### TBY-SCN-007

- **What happened:** *\<reference\>* could not be scanned.
- **Probable cause:** The scanner stopped with an error: *\<detail\>*
- **Corrective action:** Retry the synchronization. If the error persists, the instance log carries the scanner's output under the same correlation identifier. Under `scan.mode: block` the item is not pushed until it scans.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** the item under block; nothing under warn

### TBY-SCN-008

- **What happened:** The vulnerability database claims to be built at *\<built_at\>*, after this instance's clock (*\<now\>*).
- **Probable cause:** Its age cannot be stated, so it is treated as stale: either the clock of this host is behind, or the database's metadata is wrong.
- **Corrective action:** Check the clock of this host (and of the zone that cooked the tobby-system recipe), then synchronize a database whose build time is in the past. Under `scan.maxDbAgeAction: block`, items scanned with it are not pushed until then.
- **Note:** A build time up to one hour ahead is tolerated as clock skew.
- **Class:** policy refusal (exit 3) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the items scanned with it under `scan.maxDbAgeAction: block`; nothing under warn

### TBY-SCN-020

- **What happened:** The scan threshold carried by *\<recipe\>* was ignored.
- **Probable cause:** The recipe asks for *\<annotation\>*, above the *\<threshold\>* in effect for this zone; a recipe can lower the threshold, never raise it.
- **Corrective action:** Nothing to do on this instance: the zone's threshold applies. The recipe author may remove or lower the `recipe.tobbyfetch.dev/scan-threshold` annotation.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing

### TBY-SCN-021

- **What happened:** The exception for *\<id\>* expired on *\<expires\>* and no longer applies.
- **Probable cause:** The exception declared at the *\<level\>* level carries an expiry date that has passed.
- **Corrective action:** Renew the exception with a new `expires` and its justification if the risk is still accepted, or remove it and deal with the finding.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing by itself

### TBY-SCN-022

- **What happened:** *\<reference\>* carries findings with no severity score: *\<ids\>*.
- **Probable cause:** The advisory sources give these vulnerabilities neither a CVSS score nor a severity, so the policy cannot compare them with its threshold.
- **Corrective action:** Review them by hand. They are not counted against the threshold; if one must be refused, have the content fixed upstream.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing

### TBY-SCN-023

- **What happened:** *\<reference\>*, declared *\<declared\>*, was scanned as a container image, detected as one from its content (*\<evidence\>*).
- **Probable cause:** What is scanned is decided by the content, never by the declared kind: this ingredient is, or carries, an image that a runtime could run, so the vulnerability policy applies to it as to any image.
- **Corrective action:** Nothing to do if the content is expected. If it is not, ask the recipe author why an image is published under this kind; the verdict beside this note applies as usual.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing by itself; the verdict beside it decides

### TBY-SCN-024

- **What happened:** *\<reference\>*, declared *\<declared\>*, holds no container image: it is not scanned.
- **Probable cause:** Its manifest has no image configuration and no filesystem layer, and none of its files is an image archive: the scanner has nothing to read in it.
- **Corrective action:** Nothing to do. Content that is not an image (a model, a database, a document) is outside vulnerability scanning; review it by the means its own format calls for.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing

### TBY-SCN-025

- **What happened:** *\<reference\>*, delivered by the signed *\<recipe\>* recipe, is evaluated under an advisory policy.
- **Probable cause:** It is the scanner's own delivery. Refusing it with the policy it arms would leave the zone unable to arm a scanner, or to update its database.
- **Corrective action:** Nothing to do. Its findings stay reported; the trust in this content rests on the tobby-system recipe's signature.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing

### TBY-SCN-026

- **What happened:** The exception for *\<id\>* does not cover *\<reference\>*.
- **Probable cause:** The exception is scoped to *\<scope\>*, and this item lies outside that scope: the finding is judged as if no exception existed.
- **Corrective action:** Nothing to do if the scope is right. If this item was meant to be covered, widen the exception's scope in the configuration — it stays bounded by its expiry.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing by itself — the finding is judged without the exception

### TBY-SCN-027

- **What happened:** The OpenVEX document *\<path\>* cannot be used.
- **Probable cause:** *\<detail\>*
- **Corrective action:** Fix or remove the document listed under scan.vex, then restart. Tobby reads OpenVEX documents and never fetches them: the file must be a valid local document.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** the start of the instance

### TBY-SCN-028

- **What happened:** The Retriever exception for *\<id\>* has no expiry.
- **Probable cause:** It applies, but it is unbounded in time: exceptions declared by an instance must carry an expiry, and Retrievers will too from the v1 format on.
- **Corrective action:** Ask the owner of the zone's Retriever to give the exception an expires date and a justification.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing — the exception applies and is reported as unbounded

## Destination limits (TBY-DST)

### TBY-DST-001

- **What happened:** The destination registry cannot store this reference.
- **Probable cause:** *\<reference\>* exceeds a destination limit: *\<limit\>*.
- **Corrective action:** Shorten the relocated path or adjust the destination naming; the refusal happened before any push.
- **Note:** The limit is typically a path-length or naming constraint. The settings that shape the path are `destination.basePath` and `storage.basePrefix`.
- **Class:** operational failure (exit 1) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the push of that reference only

## Helm charts (TBY-CHT)

### TBY-CHT-001

- **What happened:** The Helm chart is missing an embedded dependency.
- **Probable cause:** *\<chart\>* declares the dependency *\<dependency\>* but does not embed it under charts/ — it cannot deploy offline.
- **Corrective action:** Repackage the chart with its dependencies embedded (helm dependency build, then helm package), publish it, and retry the import.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** no (the chart must be repackaged where it is built) · **Blocks:** the import of that chart

## Local store and state (TBY-STO)

### TBY-STO-001

- **What happened:** The local store could not be read.
- **Probable cause:** *\<detail\>*
- **Corrective action:** Check that the storage root exists, is mounted, and is readable by the instance, then retry.
- **Note:** Typical causes: an unmounted volume, permissions, an I/O error.
- **Class:** operational failure (exit 1) · **HTTP status:** 500
- **Fixable offline:** yes · **Blocks:** the operation concerned; a persistent condition affects the whole instance

### TBY-STO-002

- **What happened:** Writing to the local store failed.
- **Probable cause:** *\<detail\>*
- **Corrective action:** Check free space and permissions on the storage root, then retry the operation.
- **Note:** Most often free space or permissions.
- **Class:** operational failure (exit 1) · **HTTP status:** 500
- **Fixable offline:** yes · **Blocks:** the operation concerned; a persistent condition affects the whole instance

### TBY-STO-003

- **What happened:** Writing the partial download to the state directory failed.
- **Probable cause:** *\<path\>* could not be written: *\<detail\>* — most often no free space, or permissions the instance user does not hold on the state root.
- **Corrective action:** Free space on the state directory (it temporarily holds one copy of each resumable blob) or fix its permissions, then retry the task. Lower transfer.resumeThreshold to make fewer blobs resumable, or set it to 0 to stream every blob straight to the store without spooling.
- **Note:** Deliberately distinct from [TBY-STO-002](#tby-sto-002): the state directory and the store have different owners, different sizing and different fixes.
- **Class:** operational failure (exit 1) · **HTTP status:** 500
- **Fixable offline:** yes · **Blocks:** the resumable transfers concerned

### TBY-STO-004

- **What happened:** The operation was refused before it started: the target does not have enough free space.
- **Probable cause:** *\<path\>* needs *\<needed\>* bytes and only *\<available\>* are usable — *\<free\>* bytes are free, minus the *\<margin\>* % safety margin. It is short by *\<shortfall\>* bytes.
- **Corrective action:** Free at least *\<shortfall\>* bytes on the target, point the operation at a larger volume, or remove content that is no longer referenced. Lower preflight.safetyMarginPercent only if you accept filling the volume.
- **Note:** The safety margin is `preflight.safetyMarginPercent`, 10 % by default.
- **Note:** `preflight.disabled: true` removes the check entirely and is announced at startup.
- **Class:** operational failure (exit 1) · **HTTP status:** 507
- **Fixable offline:** yes · **Blocks:** the synchronization or export concerned; nothing is written

### TBY-STO-005

- **What happened:** The target filesystem cannot hold a file this large.
- **Probable cause:** *\<path\>* is formatted as *\<filesystem\>*, whose largest single file is *\<limit\>* bytes; the *\<what\>* would write a file of *\<size\>* bytes.
- **Corrective action:** Reformat the medium with a filesystem that has no such limit (exFAT, NTFS, ext4, XFS), or split the transfer so that no single file exceeds the limit.
- **Note:** The usual case is FAT32, whose limit is 4 GiB minus one byte. A single-tar export archive counts as one file.
- **Note:** The same code is raised when the condition arrives mid-write instead of at the pre-flight check: a medium swapped between the two, or a filesystem this build could not identify.
- **Class:** operational failure (exit 1) · **HTTP status:** 507
- **Fixable offline:** yes · **Blocks:** the synchronization or export concerned; on a mid-write failure the store is left intact

### TBY-STO-006

- **What happened:** The store was not reset.
- **Probable cause:** The typed confirmation did not match: resetting the store requires typing *\<phrase\>* exactly, in capitals, with nothing else.
- **Corrective action:** Type *\<phrase\>* in the confirmation field and submit again. A reset removes every artifact of this store; the operation history and the task logs are kept.
- **Note:** The audit trail is kept as well: a trail a destructive action erases is not a trail.
- **Class:** operational failure (exit 1) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** nothing — the store is untouched

## Removable-media transport (TBY-MED)

### TBY-MED-001

- **What happened:** The medium carries no media manifest.
- **Probable cause:** *\<path\>* is absent — the store was not produced by a completed mirror synchronization, or the copy onto the medium was partial.
- **Corrective action:** Re-copy the store from the source instance, or re-run the mirror synchronization that produces it. Nothing can be pushed from a medium without an inventory, and this refusal has no override.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the whole medium, no override

### TBY-MED-002

- **What happened:** The media manifest cannot be read.
- **Probable cause:** *\<path\>* is truncated, unparseable, or internally inconsistent: *\<detail\>*.
- **Corrective action:** Re-copy the store from the source instance and verify again. A manifest that does not parse leaves nothing to check the medium against, and this refusal has no override.
- **Note:** Typical inconsistencies: a path escaping the store, a duplicated inventory entry, a repository name that is not one.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the whole medium, no override

### TBY-MED-003

- **What happened:** The media manifest uses an unsupported format version.
- **Probable cause:** The medium declares media manifest format *\<found\>*; this build reads format *\<supported\>*.
- **Corrective action:** Use a Tobby release matching the medium on this side, or re-produce the medium with the release running here.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the whole medium

### TBY-MED-004

- **What happened:** The medium uses an unsupported store format version.
- **Probable cause:** The medium declares store format *\<found\>*; this build reads format *\<supported\>*.
- **Corrective action:** Use a Tobby release matching the medium, or move the content through the OCI image layout with standard tooling (skopeo, oras, crane).
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the whole medium

### TBY-MED-005

- **What happened:** The medium's recipe graph does not match its inventory.
- **Probable cause:** *\<path\>* hashes to *\<actual\>* but the manifest records *\<expected\>* — the list of what the medium delivers has changed since the manifest was written.
- **Corrective action:** Re-copy the store from the source instance. The recipe graph is what every per-recipe verdict is computed from, so an altered one blocks the whole medium and has no override.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the whole medium, no override

### TBY-MED-006

- **What happened:** The medium is addressed to another zone.
- **Probable cause:** The manifest names zone *\<found\>*; this instance serves zone *\<expected\>*.
- **Corrective action:** Check that this is the medium intended for this zone. An administrator may override the refusal deliberately; the override is recorded in the audit journal.
- **Class:** policy refusal (exit 3) · **HTTP status:** 409
- **Fixable offline:** yes · **Blocks:** the whole medium, admin override available

### TBY-MED-007

- **What happened:** The medium is older than the last one imported for this zone.
- **Probable cause:** Medium *\<media\>* was resolved at *\<resolved\>*, while zone *\<zone\>* last imported content resolved at *\<recorded\>* — importing it would roll the zone backwards.
- **Corrective action:** Check that you plugged in the current medium. An administrator may override the refusal deliberately — to restore an older delivery on purpose, for instance; the override is recorded in the audit journal.
- **Note:** An anti-accident guard, not a security control: the manifest is unsigned.
- **Class:** policy refusal (exit 3) · **HTTP status:** 409
- **Fixable offline:** yes · **Blocks:** the whole medium, admin override available

### TBY-MED-010

- **What happened:** A file the recipe needs is missing from the medium.
- **Probable cause:** *\<recipe\>* reaches *\<path\>*, which the medium does not carry — the copy was partial, or the file was removed.
- **Corrective action:** Re-copy the store from the source instance. The recipe is blocked whole and has no override; recipes intact on the same medium remain pushable.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** that recipe whole, no override

### TBY-MED-011

- **What happened:** A file on the medium has the wrong size.
- **Probable cause:** *\<path\>* holds *\<actual\>* bytes but the manifest records *\<expected\>* — the file was truncated or altered after the manifest was written.
- **Corrective action:** Re-copy the store from the source instance. The recipe reaching this file is blocked whole and has no override.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the recipe reaching that file, no override

### TBY-MED-012

- **What happened:** A file on the medium does not match its recorded digest.
- **Probable cause:** *\<path\>* hashes to *\<actual\>* but the manifest records *\<expected\>* — the content was corrupted or tampered with in transport.
- **Corrective action:** Re-copy the store from the source instance. The recipe reaching this file is blocked whole and has no override.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the recipe reaching that file, no override

### TBY-MED-013

- **What happened:** A file the recipe needs is absent from the inventory.
- **Probable cause:** *\<recipe\>* reaches *\<path\>*, which the media manifest does not list — the manifest cannot vouch for it.
- **Corrective action:** Re-produce the medium from the source instance so the manifest covers everything the recipes reach. The recipe is blocked whole and has no override.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** that recipe whole, no override

### TBY-MED-014

- **What happened:** A manifest on the medium cannot be read.
- **Probable cause:** *\<path\>* is not readable as an OCI manifest or index: *\<detail\>*.
- **Corrective action:** Re-copy the store from the source instance. What the recipe delivers cannot be established from an unreadable manifest, so the recipe is blocked whole.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** that recipe whole

### TBY-MED-015

- **What happened:** A blob on the medium is stored under the wrong digest.
- **Probable cause:** *\<path\>* is addressed as *\<expected\>* but its bytes hash to *\<actual\>* — the content-addressed store disagrees with itself.
- **Corrective action:** Re-copy the store from the source instance. The recipe reaching this blob is blocked whole and has no override.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the recipe reaching that blob, no override

### TBY-MED-016

- **What happened:** The medium's recipe graph does not match the signed recipe.
- **Probable cause:** For *\<recipe\>*, the medium's own bookkeeping lists ingredients the signed recipe document does not pin: *\<detail\>*. The bookkeeping is not signed; the recipe document is.
- **Corrective action:** Do not import this delivery: the bookkeeping was altered, or the medium was assembled by hand. Re-produce the medium from the source instance. The recipe is blocked whole and has no override.
- **Note:** The bookkeeping is `meta/recipes.json`, copied into `meta/media.json`. The code is also raised when the artifact carries the document of another recipe, or when a digest or a location differs from what the signed document pins.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** that recipe whole, no override

### TBY-MED-020

- **What happened:** A file on the medium is not covered by the inventory.
- **Probable cause:** *\<path\>* lies under manifest coverage but the manifest does not list it — it was added after the manifest was written.
- **Corrective action:** Nothing to do to proceed: extraneous content is never pushed. Investigate how it got onto the medium if you did not put it there.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing (reported only)

### TBY-MED-021

- **What happened:** A file on the medium is reached by no recipe.
- **Probable cause:** *\<path\>* is inventoried but no recipe of this medium delivers it — leftover content from an earlier delivery, most often.
- **Corrective action:** Nothing to do to proceed: content reachable from no recipe is never pushed. Prune the source store if you want the medium to carry less.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing (reported only)

### TBY-MED-022

- **What happened:** A bookkeeping file on the medium does not match its recorded digest.
- **Probable cause:** *\<path\>* does not match its inventory entry — the store's own bookkeeping was altered after the manifest was written.
- **Corrective action:** Nothing is pushed from this file, so verification continues; re-copy the store from the source instance if you did not alter it deliberately.
- **Note:** This covers the store's own ledgers other than the recipe graph, whose alteration blocks the whole medium ([TBY-MED-005](#tby-med-005)).
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing (reported only)

### TBY-MED-023

- **What happened:** The medium carries a FileSet extraction cache, which is ignored and rebuilt.
- **Probable cause:** *\<path\>* holds *\<files\>* files (*\<bytes\>* bytes) extracted elsewhere. The cache is outside the manifest's coverage, so none of it was hashed; this instance never serves it and rebuilds it from the verified blobs.
- **Corrective action:** Nothing to do for the import. If you did not expect the medium to carry an extraction, inspect or remove the directory before the medium travels back: it crosses the gap unverified.
- **Note:** The source instance served FileSets from this store.
- **Note:** Anything under that directory that does not have the shape of an extraction is reported separately as [TBY-MED-020](#tby-med-020).
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing (reported only)

### TBY-MED-030

- **What happened:** This medium has not been verified yet, so nothing is served from it.
- **Probable cause:** *\<surface\>* is closed because medium *\<media\>* arrived from another zone and no verification has established that its content is intact and signed (FR-054). The instance itself is running normally.
- **Corrective action:** Open *\<screen\>* in the web interface and run Verify — it re-hashes the medium and can take several minutes on a full disk — or call POST /api/v1/media/verify on this instance. This surface opens by itself as soon as the medium clears. `tobby media verify` reports the same verdict but runs in its own process and does not open this instance's surfaces: only a verification the instance itself performs does.
- **Note:** FR-054 requires verification to precede any push, any serving and any local write, so `/v2/` and `/files/` stay closed until it has run.
- **Note:** There is deliberately no setting that serves a medium without verifying it.
- **Class:** policy refusal (exit 3) · **HTTP status:** 403
- **Fixable offline:** yes · **Blocks:** the embedded registry and the file surface, for this medium

### TBY-MED-031

- **What happened:** A verification of this medium is already running.
- **Probable cause:** One run is walking the medium (stage: *\<stage\>*); starting a second would halve both and answer nothing new.
- **Corrective action:** Wait for the run in progress. Its verdict lands on the Media screen and on GET /api/v1/media/verification.
- **Class:** operational failure (exit 1) · **HTTP status:** 409
- **Fixable offline:** yes · **Blocks:** the second verification only

### TBY-MED-032

- **What happened:** This medium did not clear verification, so nothing is served from it.
- **Probable cause:** *\<surface\>* is closed because medium *\<media\>* verified as *\<verdict\>* rather than whole. Serving is a property of the store as a whole — a blob a blocked recipe reaches is exactly the content that failed — so unlike the push decision it is not taken recipe by recipe.
- **Corrective action:** Read the report on *\<screen\>*: it names each blocked recipe and the file that failed. Re-copy the medium from the source instance and verify again. The intact recipes can still be pushed into the zone registry, which then serves them.
- **Note:** The verdict is *partial* or *blocked*: at least one delivery failed its signature or one of its ingredient digests. `/v2/` and `/files/` hand out blobs.
- **Class:** policy refusal (exit 3) · **HTTP status:** 403
- **Fixable offline:** yes · **Blocks:** the embedded registry and the file surface, for this medium

### TBY-MED-033

- **What happened:** This request is outside the content the verification of the medium cleared.
- **Probable cause:** *\<surface\>* on medium *\<media\>* serves only what the pushable recipes reach, read-only, and *\<path\>* is not part of it — content no verified recipe reaches, a tag added after verification, or a write.
- **Corrective action:** Read the report on *\<screen\>*: content outside the verified set is listed in its findings and is never served. Deliver it through a recipe on a new medium, or push it into the zone registry.
- **Note:** Once the gate is open, what `/v2/` and `/files/` serve is the pushable recipes' artifacts, signatures, ingredients and the ingredients' own signatures. A tag added or moved after verification, and every write, are refused.
- **Class:** policy refusal (exit 3) · **HTTP status:** 403
- **Fixable offline:** yes · **Blocks:** that request only

### TBY-MED-034

- **What happened:** The verification of this medium cleared no content, so nothing is served from it.
- **Probable cause:** Medium *\<media\>* lists no recipe: nothing on it is reachable from a delivery and nothing was re-hashed. *\<surface\>* serves only what the pushable recipes reach, which here is nothing.
- **Corrective action:** Check that the source synchronization delivered its recipes before the medium was written, then re-copy the medium and verify it again on *\<screen\>*.
- **Note:** The verdict is *pushable*, with nothing to push. Such a medium used to open the gate and serve its whole store.
- **Class:** policy refusal (exit 3) · **HTTP status:** 403
- **Fixable offline:** yes · **Blocks:** the embedded registry and the file surface, for this medium

## Store integrity (TBY-INT)

### TBY-INT-001

- **What happened:** The blob *\<path\>* does not match its digest.
- **Probable cause:** Its content hashes to *\<actual\>* instead of *\<expected\>*: the file was damaged or altered on disk.
- **Corrective action:** The verification changes nothing. Re-synchronize the recipes that reach this blob to fetch it again, or restore the store from a sound copy; check the disk for errors.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** depends: re-fetching needs a source; restoring a copy does not · **Blocks:** nothing — the report states it

### TBY-INT-002

- **What happened:** *\<reference\>* is incomplete in the store.
- **Probable cause:** It references content the store does not hold: *\<missing\>*.
- **Corrective action:** Re-synchronize the recipe that reaches it to fetch the missing content, or restore the store from a sound copy.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** depends: re-fetching needs a source; restoring a copy does not · **Blocks:** nothing — the report states it

### TBY-INT-003

- **What happened:** *\<path\>* is reachable from no recipe.
- **Probable cause:** No recorded recipe and no protected root (unit imports, vulnerability database, seeded content) reaches this content.
- **Corrective action:** Nothing is required. To reclaim the space, prune to the Retriever or remove the recipe that used to hold it; the verification itself never deletes.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing

### TBY-INT-004

- **What happened:** A store verification is already running.
- **Probable cause:** Verification *\<task\>* is walking the store; running two at once would halve both and answer nothing new.
- **Corrective action:** Follow the running verification on the tasks screen, or wait for it to finish and start a new one.
- **Class:** operational failure (exit 1) · **HTTP status:** 409
- **Fixable offline:** yes · **Blocks:** the request

### TBY-INT-005

- **What happened:** *\<path\>* cannot be read as a manifest.
- **Probable cause:** *\<detail\>*
- **Corrective action:** Re-synchronize the recipe that reaches it, or restore the store from a sound copy; what this manifest references could not be checked.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** depends: re-fetching needs a source; restoring a copy does not · **Blocks:** nothing — the report states it

### TBY-INT-006

- **What happened:** The store verification was cancelled.
- **Probable cause:** Verification *\<task\>* was stopped before it reached a verdict, so it says nothing about the store.
- **Corrective action:** Start a new verification when you want an answer. Nothing was changed: a verification only reads.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing

## OCI image layout export and import (TBY-LAY)

### TBY-LAY-001

- **What happened:** This is not a usable OCI image layout.
- **Probable cause:** *\<path\>* could not be read as one: *\<detail\>*
- **Corrective action:** Check that the path is the layout itself — the directory (or the uncompressed tar of it) holding `oci-layout`, `index.json` and `blobs/`. A compressed archive must be decompressed first. `skopeo copy oci:<path>:<tag> …` on the same path tells you whether any OCI tool can read it either.
- **Note:** Typical causes: no `oci-layout` marker, an `index.json` that does not parse, or a blob that does not hash to the digest addressing it.
- **Note:** Tobby reads an uncompressed archive by seeking to a recorded blob offset, which also leaves a decompression bomb nothing to expand into.
- **Class:** operational failure (exit 1) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the import concerned; nothing is written

### TBY-LAY-002

- **What happened:** The archive was refused: one of its entries has no place in an image layout.
- **Probable cause:** In *\<path\>*, the entry *\<entry\>* was refused — *\<reason\>*.
- **Corrective action:** Nothing was written. An OCI image layout archive contains only `oci-layout`, `index.json` and `blobs/<algorithm>/<digest>` files: an archive that also carries links or paths leading outside it was not produced by an OCI tool. Get the medium re-made at its source, and report it as an incident if it arrived from outside your organization.
- **Note:** This is a verification failure, not a damaged transfer.
- **Class:** verification failure (exit 4) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the import concerned; nothing is written

### TBY-LAY-003

- **What happened:** The export destination already exists.
- **Probable cause:** *\<path\>* is already there, and this export was not allowed to replace it.
- **Corrective action:** Choose another destination, or re-run the export with the replace option (`--overwrite` on the command line) once you are sure what is there can be lost.
- **Note:** An export writes to a staging path and renames it into place, so it never half-overwrites anything.
- **Class:** operational failure (exit 1) · **HTTP status:** 409
- **Fixable offline:** yes · **Blocks:** the export concerned; nothing is written

## Packing file sets (TBY-FIL)

### TBY-FIL-001

- **What happened:** The file set could not be packed.
- **Probable cause:** *\<detail\>*
- **Corrective action:** Point `tobby fileset pack` at an existing, non-empty directory, and give the file set a lowercase name and a version (for example `tobby fileset pack ./repo site-docs:1.0.0`). Nothing was written to the store.
- **Note:** Most often a directory that does not exist, is empty or is unreadable, or a name and version that do not parse.
- **Class:** operational failure (exit 1) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the packing concerned; nothing is written

### TBY-FIL-002

- **What happened:** The directory holds an entry that cannot be packed safely.
- **Probable cause:** *\<detail\>*
- **Corrective action:** Remove or replace that entry, then pack again — a file set is extracted and served on other machines, so symbolic links must stay inside the directory, special files and setuid bits are not carried, and names must not start with `.wh.`. Nothing was written to the store.
- **Note:** Also refused: a name carrying a backslash or a NUL byte. A name starting with `.wh.` is refused because a layer reader would take it for a deletion.
- **Note:** A file set is extracted and served on other machines, Windows included, so the refusals are about where the content lands, not about this host.
- **Class:** policy refusal (exit 3) · **HTTP status:** 422
- **Fixable offline:** yes · **Blocks:** the packing concerned; nothing is written

### TBY-FIL-003

- **What happened:** Packing that directory is not allowed from this surface.
- **Probable cause:** *\<path\>* is outside the directories `files.packRoots` allows the web interface and the API to read; with no entry configured, they may read none.
- **Corrective action:** Either run `tobby fileset pack` on the instance host, which is not restricted, or add the directory to `files.packRoots` in the configuration file and restart the instance.
- **Note:** Reading an arbitrary host directory on a network request is a capability an instance is given, not one it holds.
- **Note:** `tobby fileset pack` is not restricted on the instance host because whoever runs it already holds those filesystem rights.
- **Class:** policy refusal (exit 3) · **HTTP status:** 403
- **Fixable offline:** yes · **Blocks:** the packing concerned; nothing is written

## Consumption aids (TBY-AID)

### TBY-AID-001

- **What happened:** No consumption aid can be generated: this instance names no registry the zone pulls from.
- **Probable cause:** The mapping table and the registries.yaml snippet point at the zone registry. This instance pushes to no destination.registry, and consumption.registry is not set.
- **Corrective action:** Set destination.registry when this instance pushes to the zone registry, or consumption.registry to the address the zone's clients pull from — this instance itself when it serves the zone.
- **Class:** operational failure (exit 1) · **HTTP status:** 409
- **Fixable offline:** yes · **Blocks:** the generation of the aid

## Diagnostic bundle (TBY-DIA)

### TBY-DIA-001

- **What happened:** The diagnostic bundle could not be produced.
- **Probable cause:** *\<detail\>*
- **Corrective action:** Retry; if it fails again, collect the instance logs by the means of the deployment. Nothing was written or sent: a bundle is local only.
- **Class:** operational failure (exit 1) · **HTTP status:** 500
- **Fixable offline:** yes · **Blocks:** the bundle

### TBY-DIA-002

- **What happened:** The diagnostic bundle was withheld: *\<member\>* still contained a secret.
- **Probable cause:** Before releasing a bundle, Tobby searches every member for the secret values it knows. One was found, so the bundle was not written rather than written with it.
- **Corrective action:** Report this defect to the Tobby maintainers with the member name — never with the bundle. Meanwhile, collect the information by hand.
- **Class:** policy refusal (exit 3) · **HTTP status:** 500
- **Fixable offline:** yes · **Blocks:** the bundle — never shipped with a secret in it

### TBY-DIA-003

- **What happened:** A diagnostic bundle is already being built on this instance.
- **Probable cause:** One bundle is built at a time: each holds the members in memory and searches them for secrets, and concurrent requests would multiply that.
- **Corrective action:** Wait a few seconds for the bundle in progress to finish, then ask again. Nothing was written or sent.
- **Class:** operational failure (exit 1) · **HTTP status:** 409
- **Fixable offline:** yes · **Blocks:** this bundle only — nothing was written or sent

## Clock plausibility (TBY-CLK)

### TBY-CLK-001

- **What happened:** The local clock reads *\<now\>*, earlier than the build of this binary (*\<anchor\>*).
- **Probable cause:** The clock is *\<gap\>* behind a date this binary proves has passed. Expiries, freshness checks and certificate validity are judged against this clock.
- **Corrective action:** Set the host's clock (time source of the zone, or by hand from a trusted reference), then restart. Tobby warns and records; it does not correct the clock.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing — a warning, recorded in the audit log

### TBY-CLK-002

- **What happened:** The local clock reads *\<now\>*, earlier than the production of medium *\<medium\>* (*\<anchor\>*).
- **Probable cause:** The medium was resolved *\<gap\>* after the time this host believes it is: one of the two clocks is wrong, and freshness and expiry checks depend on it.
- **Corrective action:** Check this host's clock against a trusted reference before trusting expiry and freshness verdicts. Tobby warns and records; it does not correct the clock.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing — a warning, recorded in the audit log

## Instance state directory (TBY-STA)

### TBY-STA-001

- **What happened:** The instance refuses to start: the state directory *\<root\>* has an unknown layout (*\<version\>*).
- **Probable cause:** It was written by another version of Tobby, or it is not a Tobby state directory.
- **Corrective action:** Start the version that wrote it, or restore the state directory from a backup made with this version. The documented layout is the backup contract.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** the start of the instance

### TBY-STA-002

- **What happened:** The instance refuses to start: *\<path\>* is open to every user of the host (*\<mode\>*).
- **Probable cause:** It is a secret entry of the state directory — password or token hashes, a private key — readable by other users, or a directory holding one that other users can write to (and so replace the secret), or an entry whose access list could not be read, which Tobby refuses rather than assume it is narrow. Tobby creates them for its owner only; a broader mode usually comes from a restore that did not keep permissions.
- **Corrective action:** Remove the access of other users (chmod o-rwx on Unix, on the entry and on the directories that hold it, and keep the state root out of a shared sticky directory owned by another account; on Windows, remove Everyone, Users, Authenticated Users and the other broad principals from its access list), check who could read or replace it meanwhile, then restart. Group access granted by the volume (Kubernetes fsGroup) is tolerated.
- **Class:** policy refusal (exit 3)
- **Fixable offline:** yes · **Blocks:** the start of the instance

### TBY-STA-003

- **What happened:** The state directory holds no *\<entry\>*.
- **Probable cause:** An identity entry is missing — typically after an incomplete restore. The instance starts without it, and behaves as a new instance for what it held.
- **Corrective action:** If this instance was restored, restore the missing entry from the same backup and restart; otherwise nothing to do.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** nothing by itself — the instance starts without the entry

## Tasks (TBY-TSK)

### TBY-TSK-001

- **What happened:** The task does not exist.
- **Probable cause:** No task with identifier *\<id\>* on this instance — wrong link, or the store was reset.
- **Corrective action:** Open the task list and follow the link of an existing task.
- **Class:** operational failure (exit 1) · **HTTP status:** 404
- **Fixable offline:** yes · **Blocks:** the request only

## Instance (TBY-SRV)

### TBY-SRV-001

- **What happened:** An internal error occurred.
- **Probable cause:** An unexpected condition interrupted the request; details are in the instance logs.
- **Corrective action:** Retry; if it persists, search the instance logs for the correlation identifier shown with this error.
- **See also:** [Metrics and logs](../../reference/metrics-logs/).
- **Class:** operational failure (exit 1) · **HTTP status:** 500
- **Fixable offline:** yes (diagnosis is local) · **Blocks:** the request only

### TBY-SRV-002

- **What happened:** This resource does not exist.
- **Probable cause:** The address is wrong, or the content was removed.
- **Corrective action:** Go back to the content browser or use the search.
- **Class:** operational failure (exit 1) · **HTTP status:** 404
- **Fixable offline:** yes · **Blocks:** the request only

### TBY-SRV-003

- **What happened:** The instance is unreachable.
- **Probable cause:** The network connection dropped, or the instance is restarting.
- **Corrective action:** Check your network link and retry; the page resumes as soon as the instance answers.
- **Note:** A client-side condition rendered by the UI shell on transport failure, never served by the instance itself; it is catalogued so that this guide documents it.
- **Class:** operational failure (exit 1)
- **Fixable offline:** yes · **Blocks:** the browser session's view only

### TBY-SRV-004

- **What happened:** The request is too large.
- **Probable cause:** Its body exceeds the *\<limit\>* bytes this endpoint accepts; it was refused before being read.
- **Corrective action:** Send a smaller body. A document over the bound is not one this endpoint was built for: check that the right file was sent.
- **Note:** Every API endpoint has a bound sized to its largest legitimate document: see the [input validation](../../security/input-validation/) table (NFR-023).
- **Class:** operational failure (exit 1) · **HTTP status:** 413
- **Fixable offline:** yes · **Blocks:** the request only

### TBY-SRV-005

- **What happened:** The request body is not of a type this endpoint accepts.
- **Probable cause:** It is declared *\<type\>*; this endpoint decodes *\<want\>* only. A browser page of another origin cannot send that type without a preflight the instance never grants, which is why nothing else is read.
- **Corrective action:** Send the body with the header Content-Type: *\<want\>* (curl: -H 'Content-Type: *\<want\>*'). The CLI and the interface already do.
- **Note:** A request carrying no `Content-Type` is refused the same way. Every API endpoint that reads a document decodes `application/json`.
- **Class:** operational failure (exit 1) · **HTTP status:** 415
- **Fixable offline:** yes · **Blocks:** the request only
