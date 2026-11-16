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

## 3. Secure by default: every relaxation is opt-in, visible and recorded

No default and no omitted setting weakens an instance. A weaker posture
exists only when someone writes it down, and each one is announced where
the people it affects will see it:

| Relaxation | How it is asked for | Where it shows |
| --- | --- | --- |
| Authentication off | `auth.disabled` in the configuration file or `TOBBY_AUTH_DISABLED`, never by flag | permanent banner, startup log, `auth.override_active` audit event |
| Unsigned recipes admitted | a `trust.scopes` entry with `allowUnsigned`, for named repositories only | permanent banner, the [reduced-assurance marker](../reduced-assurance/) on every item it reached |
| Vulnerabilities reported, not blocking | the default `scan.mode: warn`; `block` is opt-in | advisory banner, `TBY-SCN-006` on each violating item |
| Medium for another zone, or older than the last one imported | an `admin` waiver at media import (`TBY-MED-006`, `TBY-MED-007`), per import | audit event |
| A FileSet served without a login | `anonymous: true` on its `files.filesets` entry | permanent banner |

An instance with no account refuses to start rather than start open
(`TBY-AUTH-001`), and one with a secret file inside the transportable
store refuses too (`TBY-CFG-002`). There is **no global switch** that
disables signature verification: the only relaxation is a scope, named,
bound to explicit repository patterns.
→ [Authentication and RBAC](../auth-rbac/), [Secrets](../secrets/)

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
can be waived, by an administrator, audited.
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
Retriever and the recipe, and a recipe can only make it stricter. The
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
→ [Authentication, accounts and RBAC](../auth-rbac/), [Enterprise identity](../enterprise-identity/)

## 9. Audit

Security-relevant actions emit audit events with a fixed six-field schema
(actor, action, target, outcome, timestamp, origin), as structured log
records separable by one marker field. The origin is the client network
address; forwarding headers are not trusted. Sign-ins and refusals,
account and token changes, content-affecting actions, session and token
expiry, scan refusals and store verification are recorded, and every
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
