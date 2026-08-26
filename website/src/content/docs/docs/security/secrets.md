---
title: Secrets
description: Every secret a Tobby instance holds, where it lives, how it is stored, and why it cannot leak through logs, errors, or configuration dumps.
sidebar:
  order: 6
---

An auditor should be able to enumerate every secret an instance holds and
verify where each one lives. This page is that inventory — exhaustive as
of v0.4.x — followed by the mechanisms that keep secrets out of every
output channel.

## Inventory

| Secret | Where it lives | Form at rest |
| --- | --- | --- |
| Account passwords | state root (`state.root`) | argon2id salted hashes only (FR-073, NFR-015) |
| API token secrets | state root | hashes only; the clear secret is shown exactly once, at creation (FR-072) |
| Registry credentials | the `registries.credentialsFile` path (`dockerconfigjson` format, FR-004), chosen by the operator | as provided; keep it outside the store |
| Proxy password | configuration file or `TOBBY_NETWORK_PROXY_PASSWORD` (FR-080) | never accepted as a flag — flags are readable in the process table |
| TLS private key | the `server.tls.keyFile` path (FR-082) | PEM file; never returned by any surface, in any form |
| UI session material | memory only | a restart signs everyone out; cookies carry `Secure`, `HttpOnly`, `SameSite` (NFR-015) |

Two things are deliberately *absent* from this inventory. Trust roots are
public keys — configuration, not secrets
([content trust](../../security/content-trust/)). And there is no signing
key, because Tobby signs nothing, in any mode, ever (ADR-0007).

**The state root, never the store.** The store (`storage.root`) is
self-contained and made to travel — that is the whole point of FR-050.
So identity lives strictly apart: accounts, tokens, and instance state
belong to the state root, and the credentials file must sit outside the
store too. The reference Helm chart mounts the two as separate volumes
and refuses to render if they point at the same path.

**The separation is enforced, not merely documented.** An instance
**refuses to start** (`TBY-CFG-002`) when a configured secret path
resolves inside the store: `state.root`, `registries.credentialsFile`, or
`server.tls.keyFile`. The check resolves through the real filesystem —
relative paths, `..`, and symbolic links included — so a path that reads
as "outside" and lands inside is caught, and the refusal names both the
setting and the path it resolved to (NFR-020).

The proxy password has no file form and so has no path to check: it is a
value in the configuration that cannot serialize itself
([redaction by construction](#redaction-by-construction)). Trust roots and
CA bundles are public keys, not secrets, and are deliberately allowed
anywhere.

### Permissions on the files Tobby creates

| File | Unix | Windows |
|---|---|---|
| `accounts.yaml` (accounts, token digests) | `0600`, in a `0700` directory | access list granting the owning account only |
| Generated self-signed TLS key | `0600` | access list granting the owning account only |
| Replaced TLS private key | `0600` | access list granting the owning account only |
| Replaced TLS **certificate** | keeps the mode it had (public by construction) | inherited |

On Windows the Unix mode is not what enforces anything: `chmod` there maps
the write bit onto the read-only attribute and discards the rest, so a
file "created 0600" would be readable by every account the inherited
access list admits. Tobby replaces the file's discretionary access list
outright with a single entry naming the file's own owner, and marks it
protected so the parent directory's inheritable entries are not merged
back in. Both operating systems are in the validated scope (NFR-018).

A private key whose file an operator once loosened comes back owner-only
on the next replacement. The certificate beside it does not: it is public
by construction, and a deployment that published it on purpose must not
silently lose that.

## Hashes are computed by the tool

No surface — CLI, UI, or API — accepts a pre-computed password hash.
`tobby user add`, `tobby user passwd`, the account screens, and the API
all take a clear password over the authenticated channel and derive the
argon2id hash internally (FR-066). This closes the pass-the-hash shortcut
and guarantees the hashing parameters are the tool's, not whatever a
script produced.

## Redaction by construction

NFR-015 requires that secrets never appear in logs, error messages, API
responses, or configuration dumps. Tobby implements this in the type
system rather than by review discipline: sensitive configuration values
are a dedicated `Secret` type whose every serialization path — string
formatting, YAML, JSON, debug verbs — yields `REDACTED` instead of the
value. Code that legitimately needs the value must call a single,
greppable accessor. A secret reaching an output channel is therefore a
compile-visible act, not an oversight.

Concretely:

- `tobby config dump` prints the effective configuration with secrets
  redacted by construction (FR-003) — safe to attach to a support ticket.
- Proxy credentials never appear in logs or errors (FR-080 acceptance),
  and the network screens report their presence as a boolean, never a
  value.
- The TLS private key is returned by nothing: not its bytes, not its
  length, not a digest (a digest would be a stable oracle against a
  candidate key). The certificate — public by construction — is what the
  admin surfaces report.
- The acceptance for NFR-015 is a scan of the full e2e suite's logs and
  responses with known planted secrets: zero occurrences, and the
  redaction paths are unit-tested.

## What to back up, what to protect

Protect the state root as you would any credential store: it holds the
hashes and the instance identity, and it is the single backup target.
The store holds verified public content and operation logs — valuable,
but not secret.

:::note[Upcoming — milestone 7]
A one-command **diagnostic bundle** — version, redacted configuration,
logs, reports, integrity results, with the redaction itself tested —
arrives at milestone 7 (R-30). Until then, `tobby config dump` plus your
log excerpts are the safe equivalents.
:::
