---
title: The security model
description: How Tobby decides what to trust, what it refuses to do, and where each guarantee is enforced — signatures, secure defaults, no unconfigured connection, the media crossing, trust scopes, vulnerability policy, authentication and audit.
sidebar:
  order: 1.5
---

This page explains the model as a whole: the rules Tobby follows, in the
order a decision is made, and where each rule is enforced and shown. It
does not repeat the detail. Each section ends with the page that carries
it. For a single printable page with delivery status per feature, see
[the security model on one page](../one-pager/); for adversaries and
network flows, the [threat model](../threat-model/).

## 1. Tobby signs nothing

Tobby transports content; it never creates authenticity. It holds no
signing key in any mode, and there is no code in it that signs. A
signature is produced upstream, by the organization's qualification
pipeline, with cosign, and Tobby only **verifies** it.

The consequence is a clean division of labour. Compromising a Tobby
instance yields no signing capability, and an organization can use any
signing infrastructure, a hardware token or a split-custody procedure,
without Tobby needing to know. Even the [emergency path](../../recipes/emergency-path/)
follows this rule: you sign, with your key, outside Tobby.

Verification is key-based and fully offline (no Fulcio, no Rekor, no
online service), and it runs twice: at import into the store, and again
before any push to a destination.
→ [Signatures, trust roots and allowlist](../content-trust/)

## 2. Authenticity comes from the destination's trust roots

A recipe is trusted when its cosign signature verifies against a **trust
root** configured on the instance that receives it: a named public key,
given inline, as a file, or as an HTTPS URL fetched once when the
configuration loads, never at verification time. The roots are a set, so a
key is rotated by overlap with no instant at which verification is
relaxed.

A signed recipe pins every ingredient by digest, so one valid signature
attests the exact bytes of the whole delivery. Content that fails a digest
or a signature check is not pushed or served: **the recipe is blocked
whole**, and sibling recipes that verify are unaffected.
→ [Signatures, trust roots and allowlist](../content-trust/), [errors `TBY-SIG-*`](../../reference/errors/#tby-sig-001)

## 3. Secure by default: relaxations are visible

A weaker posture exists only when someone writes it down, and each one is
announced where the people it affects will see it. Three exceptions are not
written in the instance's own configuration: vulnerability scanning is
advisory by default (`scan.mode: warn`), an absent `registries.allowlist`
restricts nothing (reported as *undeclared*), and the Retriever, a document
that is not signed, can loosen the vulnerability policy. All three are in
the table:

| Relaxation | How it is asked for | Where it shows |
| --- | --- | --- |
| Authentication off | `auth.disabled` in the configuration file or `TOBBY_AUTH_DISABLED`, never by flag | permanent banner, startup log, `auth.override_active` audit event |
| Unsigned recipes admitted | a `trust.scopes` entry with `allowUnsigned`, for named repositories only | permanent banner, the [reduced-assurance marker](../reduced-assurance/) on every item it reached |
| Vulnerabilities reported, not blocking | the default `scan.mode: warn`; `block` is opt-in | advisory banner, `TBY-SCN-006` on each violating item |
| Vulnerability threshold raised, or exceptions added, by the Retriever | `spec.scanPolicy` in the Retriever, which is not signed; its threshold replaces the instance's in either direction | each item's report (`threshold_level: retriever`); the Retriever's exceptions on `/admin/policy`; `TBY-SCN-028` for an exception without expiry; no audit event |
| Registries contacted without restriction | no `registries.allowlist` key | reported as *undeclared* |
| Medium for another zone, or older than the last one imported | a waiver (`TBY-MED-006`, `TBY-MED-007`), per verification or import: `admin` on the screen and the API, anyone with host access on the command line | audit event when an import is requested; none when given at the Verify step (known defect) |
| A FileSet served without a login | `anonymous: true` on its `files.filesets` entry | permanent banner |

The Retriever is unsigned by design: it carries the trust of the instance
configuration, and its location must be protected like the configuration
file — see [the threat model](../threat-model/#adversaries-and-what-stops-them).

An instance with no account refuses to start rather than start open
(`TBY-AUTH-001`), and one with a secret file inside the transportable
store refuses too (`TBY-CFG-002`). There is **no global switch** that
disables signature verification: the only relaxation is a scope, named,
bound to explicit repository patterns.
→ [Authentication and RBAC](../auth-rbac/), [Secrets](../secrets/)

### Hardening switches

The other direction exists too: two settings, off by default, that **close
a way in** rather than open one (SRS FR-098).

| Switch | What it closes | What still works |
| --- | --- | --- |
| `hardening.disableUnitImport` | unit imports, OCI image layout imports and FileSet packing, on the screen, the API **and** the command line — [TBY-POL-005](../../reference/errors/#tby-pol-005) | recipe synchronizations, media imports |
| `hardening.disableRegistryPush` | every write to the embedded registry, for every client and every role, administrators included — OCI `DENIED` carrying [TBY-POL-006](../../reference/errors/#tby-pol-006) | pulls, recipe synchronizations |

With both set, content enters the store only through recipes verified
against the instance's trust roots. A hardening switch needs no
acknowledgement, but it is reported like every security setting — the
startup log, `tobby config dump`, the screen whose action it disables
(shown disabled, naming the setting), the `tobby_hardening_enabled` metric
— and every refusal is a `hardening.refused` audit event, so an operator
never mistakes a refusal for a fault. Seeding the instance by push (UC3) is
then impossible, by design.

A **media import** (`tobby media import`, the media screen and its API) is
not a unit import and neither switch closes it: it is the normal way
content reaches a destination zone. It brings in only what the medium's
signed recipes deliver, after the medium is verified against this
instance's trust roots, and pushes it into the zone registry. On the
destination instance, the medium's content carries the class `recipe` from
the recipe graph the medium carries; nothing it delivers is recorded as
`seed`.

Whether the switches are on or not, every repository carries its
**provenance class** — `recipe`, `unit-import` or `seed` — in the content
browser, the API, the log records that concern it and the
`tobby_store_unmanaged_repositories` metric: the content no verified recipe
vouches for is always counted and always visible. The class is a fact about
this store; it is never written into any registry and decides nothing about
trust. → [Configuration](../../reference/configuration/#hardening)

## 4. No connection that was not configured

Tobby opens no network connection the operator did not configure: no
telemetry, no crash report, no update check. Its
outbound traffic is the set of endpoints in its own configuration, each
gated by the registry allowlist where it reaches a registry, and routed
through the configured proxy and private certificate authorities, with no
setting to disable TLS verification. The allowlist applies to the host
**actually contacted**; when source substitution rewrites an endpoint, the
allowlist and credentials follow the effective host while trust scopes
keep following the nominal reference.

This is tested, not promised: every acceptance run starts from an egress
canary that must fail from the isolated zone, and end-to-end runs behind
an egress-capturing proxy record connections to configured endpoints only.
→ [Threat model and network flows](../threat-model/), [Tests and proofs](../../project/tests-and-proofs/)

## 5. Crossing the air gap

On the destination side of a removable medium, nothing the medium says is
trusted.

- **Content is reachable from a verified recipe, or it is not pushed.**
  Verification starts from the recipes, checks their signatures against the
  destination's own trust roots, and follows their pinned digests to the
  content they reach. Anything not reachable from a verified recipe is never
  pushed and is reported.
- **The media manifest is an inventory, not a proof.** It is deliberately
  unsigned. It detects corruption and incompleteness, and tells a person
  what to expect. A forged manifest cannot make a bad blob good.
- **Verification is double.** Each file is checked against the inventory
  *and* against its own content address (its digest), so rewriting the
  inventory to agree with a damaged blob changes nothing.
- **Trust material on the medium is ignored.** A trust-root file planted on
  a medium has no effect, by requirement and by test.
- **Until a verification clears the medium whole, the destination serves
  nothing** from it: `/v2/` and `/files/` answer `403` (`TBY-MED-030`), for
  every role, with no setting to reopen them. Once cleared, it serves only
  the content the pushable recipes reach, as that verification hashed it,
  read-only (`TBY-MED-033` for anything else); a referrers listing is built
  from the referring manifests themselves, never from the index on the
  medium.

The only two waivers (a medium addressed to another zone, a medium older
than the last imported) are anti-accident guards: they are not security
controls, because a manifest field can be forged. That is why they alone
can be waived: by an administrator on the screen and the API, by anyone
with access to the host on the command line. A waiver applied to an import
is audited; a waiver given at the Verify step is not, although on the
screen and the API it can open the serving gate (a known defect).
→ [Media security model](../../air-gap/media-security/), [Media workflow](../../air-gap/media-workflow/)

## 6. Trust scopes

A trust scope is the only form relaxation or compartmentalization takes.
It is named, bound to explicit repository patterns (matched against the
recipe's nominal cookbook path, first match wins, no match means the strict
default), and it does one of two things:

- **waive**: `allowUnsigned` admits recipes that carry no signature, and
  everything such a recipe reaches carries a visible reduced-assurance
  marker, through the API, task reports, the interface, logs, metrics and
  the media manifest (where the destination treats it as a claim of the
  source, never as a verdict);
- **restrict**: `roots` limits which trust roots may verify the recipes of
  that scope. This narrows trust and waives nothing; it is how a dedicated
  key, such as an emergency key, is fenced to one cookbook.

→ [Signatures, trust roots and allowlist](../content-trust/), [The reduced-assurance marker](../reduced-assurance/), [The emergency path](../../recipes/emergency-path/)

## 7. Vulnerability scanning and policy

Tobby scans the images it retrieves with a pinned Trivy binary, offline,
before anything of a recipe is pushed or written on a medium. The scan
cannot be switched off: there is no `enabled` setting, and the default
advisory mode is a visible mode, not an absence of scanning.

What is scanned is decided by the **content**, not by the declared kind: an
image published as an "artifact" or a FileSet is detected from its bytes
and scanned as an image. One policy (a CVSS threshold, advisory or
blocking, tolerated exceptions) is composed from the instance, the
Retriever and the recipe. A recipe can only make it stricter; the
Retriever's threshold replaces the instance's and can make it laxer as well
as stricter. The
vulnerability database travels with Tobby itself, and its age is part of
every report; a missing, stale or unverifiable scanner is a visible
degraded mode (`TBY-SCN-001` to `TBY-SCN-004`), never a silent pass.
Scanning addresses a different adversary from signatures: *honestly signed
content with known vulnerabilities*.
→ [Vulnerability scanning](../vulnerability-scanning/), [Vulnerability database](../../air-gap/vulnerability-database/)

## 8. Authentication and authorization

Authentication is on by default in both modes. Four methods coexist: local
accounts (basic authentication, argon2id hashes computed by the tool),
static API tokens (shown once, stored as hashes, optional expiry), OpenID
Connect and SAML 2.0. Enabling a federated method never disables the local
ones, so a lost identity provider does not lock the instance. Federated
users receive a role only through an explicit group mapping; Tobby never
grants one by default.

Three fixed roles, `viewer < operator < admin`, gate every surface: the
interface, the API, the embedded registry (by HTTP method, so standard
clients work) and the file server. The table of floors is generated from
the routes the server actually mounts and is checked by a test, so a route
cannot ship without a reviewed floor. The last administrator can be
neither deleted nor demoted. Failed attempts are throttled per origin.

A browser session authorizes nothing by its cookie alone. Every
state-changing request it authenticates, in the interface and in the API
alike, carries the session's anti-forgery token, and the request is refused
when the browser says it comes from another origin. Every response of the
listener carries the browser security headers, and every HTML page a
Content-Security-Policy that allows no script without a hash.
→ [Authentication, accounts and RBAC](../auth-rbac/), [Enterprise identity](../enterprise-identity/)

Every input crossing a trust boundary is bounded, decoded strictly and checked against its grammar → [Input validation](../input-validation/)

## 9. Audit

Security-relevant actions emit audit events with a fixed six-field schema
(actor, action, target, outcome, timestamp, origin), as structured log
records separable by one marker field. The origin is the client network
address; forwarding headers are not trusted. Sign-ins and refusals,
account and token changes, content-affecting actions, session and token
expiry, scan refusals, hardening refusals and store verification are recorded, and every
synchronization carries a run identifier that follows it through logs,
tasks and reports.
→ [Audit log](../audit-log/), [Metrics and logs](../../reference/metrics-logs/)

## What the model does not claim

Stating the limits is part of the model.

- Tobby does not **qualify** upstream content: it ensures that what the
  organization signed is what arrives, not that it is good.
- The audit log is **operational evidence**, not a signed trust anchor. Ship
  it to your SIEM to get independence from the host.
- Key custody (HSM, KMS, revocation) belongs to the organization.
- Encryption at rest is the volume's job; Tobby keeps its secrets in the
  state directory, outside the transportable store, and refuses to start if
  they would travel with it.
- Physical and host-level security of the instance are outside Tobby.

→ [Limits](../../discover/limits/), [Threat model and network flows](../threat-model/)

## Verify rather than believe

The build chain of the binary you run (SLSA Build L3 provenance,
reproducible builds, signed SBOMs) can be checked independently:
[Verify a release](../../project/verify-a-release/). The guarantees above
are backed by named tests and acceptance runs: [Tests and proofs](../../project/tests-and-proofs/).
