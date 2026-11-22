---
title: Rotate keys and secrets
description: How to rotate recipe signing keys, ingredient trust roots, passwords, API tokens, the TLS pair and the identity-provider and registry credentials, what each change needs to take effect, and what users see while it does.
sidebar:
  order: 11
---

An instance depends on two kinds of material: **public keys** it verifies
against (trust roots) and **secrets** it holds (credentials, private keys,
password hashes). Each is rotated differently, and each takes effect at a
different moment. This page gives the procedures and says, for each, when the
change is read, what breaks if it is done in the wrong order, and how to see
that it worked. The inventory of what is secret, and where it lives, is on
[Secrets](../secrets/); how trust roots and scopes decide is on
[Signatures, trust roots and allowlist](../content-trust/).

:::tip[In short]
- A recipe signing key rotates by overlap. Add the new public key as a second trust root on every instance that verifies, starting with the isolated zone. Then sign with the new key. Remove the old root last, once a store verification under the new configuration is clean. [Procedure](#rotate-a-recipe-signing-key)
- Sign **new recipe versions** with the new key. A signature added later to a version a store already holds never reaches that store, so the old key keeps deciding that version there. [Why](#a-store-keeps-the-signatures-of-its-first-copy)
- Trust roots, registry credentials, OIDC and SAML material and the proxy password are read at startup. A change needs a restart, and a restart signs every browser session out. The TLS pair is re-read on the next handshake. [Table](#what-a-change-needs-to-take-effect)
- Change passwords and tokens on the running instance (screen or API). `tobby user add` and `tobby user passwd` are safe only while the instance is stopped. [Accounts](#account-passwords)
:::

## What a change needs to take effect

Nothing watches the configuration file, and there is no reload signal: the
instance handles `SIGTERM` and `SIGINT` only, as a graceful stop. A value the
instance reads at startup changes when the instance restarts, and not before.
The command-line tools that open a store themselves (`tobby media verify`,
`tobby media import`, `tobby store verify` without `--instance`,
`tobby sync --dry-run`) read the configuration file at each run.

| Material | Where it lives | Taken into account | What users see |
| --- | --- | --- | --- |
| Recipe and ingredient trust roots | `trust.roots`, `trust.scopes` (configuration file) | restart of the instance; each run of the commands above | a recipe no applicable root verifies is refused, [`TBY-SIG-001`](../../reference/errors/#tby-sig-001) |
| Account password | `accounts.yaml` in `state.root` | immediately, through the screen or the API | the account's other sessions are closed |
| API token | `accounts.yaml` in `state.root` | immediately | a revoked token is refused like a wrong one ([`TBY-AUTH-002`](../../reference/errors/#tby-auth-002)), an expired one with [`TBY-AUTH-013`](../../reference/errors/#tby-auth-013) |
| Server TLS pair | `server.tls.certFile`, `server.tls.keyFile` | the next TLS handshake, no restart | the new certificate and fingerprint |
| Registry credentials | `registries.credentialsFile` | restart | nothing, if the old credential still works until then |
| OIDC client secret | `auth.oidc.clientSecretFile` | restart | every session signed out by the restart |
| SAML service-provider pair | `auth.saml.certFile`, `auth.saml.keyFile` | restart | the new certificate in the descriptor at `/auth/saml/metadata` |
| Proxy password | configuration file or `TOBBY_NETWORK_PROXY_PASSWORD` | restart | nothing |
| UI sessions and anti-forgery tokens | memory only | nothing to rotate | a restart signs everyone out ([`TBY-AUTH-005`](../../reference/errors/#tby-auth-005)) |

There is **no session signing key** to rotate. A session is a random
identifier held in the instance's memory, and its anti-forgery token is
another random value drawn for that session. Nothing about sessions is
written to the state directory, so nothing survives a restart: every person
signs in again. Plan restarts accordingly. On Kubernetes or a VM, a restart
also briefly interrupts the embedded registry; see
[Upgrade and roll back](../../passthrough/upgrade-rollback/).

## Recipe signing keys

Tobby never signs and holds no private key (ADR-0007). Rotating a signing key
means two separate actions: the organization signs with a new private key,
and every instance that verifies learns the matching public key as a trust
root. Trust roots are a set, so both keys can be trusted at once. That
overlap is what makes the rotation possible without relaxing verification at
any point.

### When a signature is checked

A rotation is safe only if you know which copy of a signature each check
reads, and against which roots.

| Moment | Copy of the signature read | Roots used |
| --- | --- | --- |
| Synchronization | the cookbook's | the instance's, as loaded at startup |
| Plan (`tobby sync --dry-run`) | the cookbook's | the configuration file given to the command |
| Before every push to `destination.registry` | **the store's** | the instance's |
| Store verification (`tobby store verify`, the Store screen) | **the store's** | the configuration given to the command, or the instance's with `--instance` |
| Media verification and import, destination side | **the medium's** | the destination's own; anything on the medium is ignored |

Two checks do not happen:

- Content already in a passthrough store is **not** re-verified when it is
  served from the embedded registry (`/v2/`, `/files/`). Removing a trust root
  does not withdraw it.
- Content already pushed to a zone registry stays there. Tobby does not reach
  back into a registry to remove what it promoted.

The recorded verdict of a recipe (`verified` in `GET /api/v1/recipes`, the
Recipes screen) is the one taken at its synchronization. It is not
re-evaluated when the roots change; a store verification is what answers
that question.

### A store keeps the signatures of its first copy

A synchronization copies a recipe's signatures into the store when it first
stores that recipe version: both cosign layouts, attached and bundle. On a
later synchronization, a recipe version the store already holds at the same
digest is skipped as up to date, and its signatures are **not copied again**.
The same holds for an ingredient the store already holds.

The consequence for a rotation: signing an **already published** recipe
version a second time, with the new key, adds that signature to the cookbook
only. Every store that held the version before keeps only the old signature,
and so do the media and the zone registry filled from it. Once the old root is
removed, that version is refused on the next push, in the next store
verification and on the destination side, while the plan still calls it
verified because the plan reads the cookbook.

This was replayed locally: version `1.0.0` signed with `release-2025` and
promoted, then signed a second time with `release-2026`, then the instance
restarted with `release-2026` as its only root.

```
$ tobby sync --wait
task tsk_dac2a194b92b on http://127.0.0.1:18090
  type:      sync
  reference: ./retriever.yaml
  status:    failed
  duration:  1s
  items:     3 (0 done, 2 skipped, 1 failed)
  failed   push/alpine@1.0.0/recipe — TBY-SIG-001
  principal: TBY-SIG-001 (verification)
TBY-SIG-001 — The recipe signature could not be verified.
  cause:  No trust root of the default set validates the signature of alpine@1.0.0 (keys tried: sha256:f3212501fb7a…). …
```

The exit code is `4`. With the same configuration, `tobby sync --dry-run`
reports `alpine 1.0.0 … verified` and exits `0`.

**Sign new versions with the new key.** A new `metadata.version` is a new
recipe digest, stored for the first time, with the signatures present in the
cookbook at that moment. Published versions are immutable, so this is also
the only way to change what a version pins. A recipe that has not been
synchronized anywhere yet can simply be signed with both keys.

### Rotate a recipe signing key

The order matters: **add the new root from the bottom up** (isolated zone
first, the zones that read from the cookbook last), and **remove the old one
from the top down**.

1. **Generate the new key pair outside Tobby** and keep the private key where
   the organization keeps its signing keys. Compute the fingerprint of the
   public key. It is the value every Tobby report uses (SHA-256 of the
   key's DER encoding):

   ```sh
   openssl pkey -pubin -in release-2026.pub -outform DER | openssl dgst -sha256 -r
   ```

2. **Add the new root beside the old one** on every instance that verifies:
   each passthrough instance, each mirror source, and each destination of a
   physical transfer.

   ```yaml
   trust:
     roots:
       - name: release-2025
         keyFile: /etc/tobby/keys/release-2025.pub
       - name: release-2026
         keyFile: /etc/tobby/keys/release-2026.pub
   ```

   Restart the instance. A root that cannot be read or parsed stops the
   start, naming the root:

   ```
   tobby: trust root release-2026: open ./keys/missing.pub: no such file or directory
   ```

   Check what the instance now runs with: `tobby config dump --config <file>`
   lists the roots by name and source (a path, a URL or the inline key).

3. **If the old key is listed by a trust scope**, list the new one there too.
   A root a scope lists is reserved to that scope: a new root added only to
   `trust.roots` belongs to the default set and verifies nothing inside the
   scope ([content trust](../content-trust/#relaxation-scopes--and-their-visibility)).
   The [emergency key](../../recipes/emergency-path/) is the usual case:

   ```yaml
   scopes:
     - name: emergency
       repositories: ["registry.example.com/emergency/**"]
       roots: [org-emergency-2025, org-emergency-2026]
   ```

4. **Sign new recipe versions with the new key**, offline as always
   (`--use-signing-config=false --tlog-upload=false`, see
   [Sign with cosign](../../recipes/write-and-publish/#sign-with-cosign)).
   Move the Retrievers to those versions. They are synchronized, carried and
   pushed with the new signature.

5. **Rehearse the removal on each store** before doing it. Write the
   configuration without the old root to a separate file and verify the
   store against it. The command only reads, and names the key that verified
   each recipe:

   ```
   $ tobby store verify --config proposed.yaml
   verdict: defects — 14 blobs (4134641 bytes), 7 manifests, 2 recipes checked
     recipe alpine@1.0.0  complete, signature refused
     recipe alpine@1.0.1  complete, signature verified (key sha256:f3212501fb7a…)
     DEFECT  TBY-SIG-001 alpine@1.0.0
   ```

   Run in-process, this form refuses a store that an instance is serving:
   run it during a short stop, or on a mirror store before it leaves. With
   `--instance`, the verification runs on the instance, against the roots it
   runs with, not the proposed ones. Do not rely on `tobby sync --dry-run`
   for this check: it reads the cookbook's signatures, not the store's.

6. **Remove the old root**, top down: the instances that read from the
   cookbook first, the destinations of physical transfers last, once no
   medium in circulation still carries a recipe that only the old key
   verifies. Restart each instance.

A key can also be rotated inside one root: a `keyFile`, `keyURL` or `key`
may hold several PEM public keys, all trusted under that root's name. Keep
one key per root with a dated name instead. Reports name the fingerprint
either way, but a scope reserves a whole root, and removing one key from a
shared root is easy to get wrong.

### Removing a root too early

| Where | What happens |
| --- | --- |
| Synchronization | a recipe whose cookbook signature only the removed key verifies is refused, `TBY-SIG-001` naming the fingerprints tried. The run is incomplete, so no prune runs, and nothing it holds is deleted. |
| Push to the zone registry | a recipe whose **store** copy only the removed key verifies is refused before the push, `TBY-SIG-001`, even when the cookbook carries a newer signature. The refusal is counted in `tobby_promotion_refusals_total{code="TBY-SIG-001"}`. |
| Plan | the recipe shows `refused` in the `SIGNATURE` column; exit code `4`. |
| Store verification | `DEFECT TBY-SIG-001` on the recipe; exit code `4`. The store is not modified. |
| Destination of a medium | the recipe is `BLOCKED`, the medium is `partial`. See below. |
| Already served or pushed | nothing changes: content in a passthrough store stays served, content in the zone registry stays there. |

An `allowUnsigned` scope does not soften any of this. It admits recipes that
carry **no** signature. A recipe that carries a signature no applicable root
verifies is refused inside such a scope exactly as outside it.

To recover, put the old root back and restart. The refused recipes verify
again at the next synchronization or verification; nothing was deleted.

### The isolated zone

The destination of a physical transfer verifies every recipe against **its
own** trust roots; a key that travels on the medium is ignored
([media security](../../air-gap/media-security/)). The new public key must
therefore be configured on the destination, and the destination restarted,
**before the first medium signed with it arrives**. A medium that arrives
first is refused for that recipe. Replayed, with a destination that still
trusts only `release-2025`:

```
$ tobby media verify --config destination.yaml
verdict: partial (47 checked files, 4141453 bytes)
  pushable  alpine@1.0.0 (29 files, 4134878 bytes)
  BLOCKED   alpine@1.0.1 — TBY-SIG-001 at 127.0.0.1:5001/cookbook/alpine
```

The exit code is `4`. Two consequences follow from how a destination serves:

- **One unverifiable recipe stops all serving from the medium.** The
  destination's own registry and file server serve a medium only when its
  verification comes out whole. A `partial` medium serves nothing, for any
  recipe, with
  [`TBY-MED-032`](../../reference/errors/#tby-med-032). The recipes that are
  pushable can still be imported into the zone registry, which then serves
  them. The refusal text suggests re-copying the medium; when the cause is a
  missing trust root, adding the root is the fix.
- **The restart that adds the root closes the serving gate.** It re-closes on
  every restart ([`TBY-MED-030`](../../reference/errors/#tby-med-030)) until
  the medium is verified again: plan the restart with the verification that
  follows it, on the Media screen or with `POST /api/v1/media/verify`.

`tobby media verify` reads the configuration file at each run and writes
nothing, so a proposed destination configuration can be checked against the
medium before the instance is restarted with it. See
[an unverified medium serves nothing](../../air-gap/import-destination/#an-unverified-medium-serves-nothing).

### Roots fetched from a URL

A `keyURL` root is downloaded when the configuration loads (at startup, and
at each run of the commands above) and cached in
`state.root/trust-cache/<root name>.pub`. If the URL cannot be fetched, the
instance uses the cached copy, and **says nothing about it**: no log line
records that the cached copy was used. Removing an old key from the file
served at the URL therefore has no effect on an instance that cannot reach the
URL at its next start. For a rotation you need to see happen, use `keyFile`
or `key`, or check the key in use with a store verification.

### Where the change is visible

- **Configuration**: `tobby config dump` lists the roots by name and source,
  not by fingerprint. No screen lists the configured trust roots, and the
  policy screen (`/admin/policy`) does not either.
- **Per recipe**: `tobby store verify` and `tobby media verify` (and their
  `--output json` forms, `key_fingerprint` and `keyFingerprint`) name the key
  that verified each recipe; the Media screen shows it beside each recipe.
  A refusal names every fingerprint it tried.
- **Audit**: a trust-root change is a configuration change, and no audit
  event records it as such. Keep the configuration file under change
  control. The restart that applied it is an `instance.start` event, and each
  store verification is a `store.verify` event
  ([audit log](../audit-log/)).

## Ingredient signatures

`trust.requireIngredientSignatures` exists, and is off by default. While it
is off, an ingredient's own signature is copied, verified for information,
and recorded; it never blocks, so a rotation by the ingredient's publisher
changes only what is recorded
([content trust](../content-trust/#ingredient-signatures--recorded-required-only-on-request)).

When it is on, every ingredient must carry a signature that a trust root
applicable to the ingredient verifies (scopes matched on the ingredient's own
canonical repository), or its **recipe is blocked whole** with `TBY-SIG-001`
(FR-033). The keys involved are usually the upstream publisher's, so the
rotation is announced by the publisher, not decided by you:

1. Add the publisher's new public key as a root, beside the old one, on every
   instance that verifies, and in the same scope as the old one if a scope
   lists it. Restart.
2. Have recipes pin ingredient digests the publisher signed with the new key,
   in new recipe versions.
3. Rehearse with `tobby store verify --config proposed.yaml`, then remove the
   old root.

The store limit above applies to ingredients too: an ingredient the store
already holds keeps the signatures it was first copied with, and the
ingredient check reads that copy. A publisher that re-signs an existing
digest with its new key does not change what the store holds. An ingredient
inside an `allowUnsigned` scope is exempt from the requirement.

## Account passwords

Passwords are stored as argon2id hashes in `accounts.yaml`, in the state
directory (FR-073). The running instance keeps the accounts in memory and
writes the whole file back on every change it makes, including the
last-login time of each password sign-in.

- **Your own password**: the account screen (`/account`) or
  `POST /api/v1/account/password`, with the current password (FR-077). It
  takes effect immediately, and the account's other sessions are closed. The
  browser session that made the change stays open. The event is
  `account.password_change`.
- **Another account's password**: neither the screen nor the API sets it. On
  a running instance, an administrator can delete the account and create it
  again with a new password on `/admin/accounts`. The last administrator
  cannot be deleted ([`TBY-AUTH-011`](../../reference/errors/#tby-auth-011)).
  Tokens are not attached to accounts and are not affected.
- **On the host**: `tobby user passwd <name>` and `tobby user add` write
  `accounts.yaml` directly, and a running instance does not read it again.
  Run them **only while the instance is stopped**. Run against a running
  instance, the change is not seen, and the instance's next write puts its
  own copy back. Replayed: after `tobby user passwd admin` on a running
  instance, the new password is refused (`401`), the old one is accepted, and
  after a restart the old one is still the one that works.

## API tokens

A token carries its own name and role, is shown once at creation, and is
stored as a hash (FR-072). Rotate by overlap:

1. Create the replacement on `/admin/accounts` or with
   `POST /api/v1/tokens`. An active token's name cannot be reused, so give it
   a new name (`ci-2026`). An `expires` value, or
   `auth.tokens.defaultLifetime`, makes the next rotation happen by itself.
2. Roll the clients onto the new secret.
3. Revoke the old token (`POST /api/v1/tokens/{name}/revoke`). Revocation is
   immediate and permanent; the name becomes free again.

A client still presenting the revoked token is refused with
[`TBY-AUTH-002`](../../reference/errors/#tby-auth-002), and each refusal is an
`auth.authenticate` failure in the audit log under the token's name. An
expired token is refused with
[`TBY-AUTH-013`](../../reference/errors/#tby-auth-013) and a `token.expire`
event. Both count as failed attempts for the client's network address: after
ten, that address is throttled
([`TBY-AUTH-012`](../../reference/errors/#tby-auth-012), retry after 30
seconds), **including the valid clients behind the same address**. Find the
forgotten client in the audit log before it locks out its neighbours.

## Server TLS certificate

The listener re-reads `server.tls.certFile` and `server.tls.keyFile` whenever
their modification time or size changes, on the next handshake, without a
restart (FR-082). Replacing the files is enough. Replayed: the served
fingerprint changed from the old pair's to the new pair's on the handshake
that followed the copy. A pair caught half-written is not loaded; the
previous certificate keeps being served until the pair is consistent. A file
swap leaves no audit event; a replacement made on the Network screen or with
`PUT /api/v1/network/certificate` is a `config.server_certificate` event.
Watch `tobby_server_certificate_expiry_timestamp_seconds` to rotate before
the end of validity ([network](../../passthrough/network/#server-tls-what-the-instance-presents)).

:::note[An instance that started self-signed]
On an instance that runs the generated fallback, the Network screen writes
the new pair to `state.root/tls/server.crt` and `server.key` and adopts it
at once. The configuration still names no pair, so **at the next restart the
instance serves the generated fallback again** (replayed). After adopting a
pair from the screen, point the configuration at it so it survives the
restart:

```yaml
server:
  tls:
    certFile: /var/lib/tobby/state/tls/server.crt
    keyFile: /var/lib/tobby/state/tls/server.key
```

From then on, the files are re-read on change like any configured pair. The
API form of the replacement (`PUT /api/v1/network/certificate`) refuses an
instance running the fallback.
:::

## Registry credentials

`registries.credentialsFile` is read once, at startup, for the source
registries, the destination and the recipe publication. A running instance
keeps the credentials it loaded. To rotate one:

1. Create the new credential at the registry while the old one still works.
2. Write it into the file.
3. Restart the instance.
4. Revoke the old credential at the registry.

If the registry revokes the old credential before the restart, the
synchronizations and pushes fail until the instance is restarted. The
`tobby media import` and `tobby recipe push` commands read the file at each
run.

## Identity providers

The OIDC client secret (`auth.oidc.clientSecretFile`) and the SAML
service-provider pair (`auth.saml.certFile`, `auth.saml.keyFile`) are read
when the instance starts (FR-070, FR-071). Each rotation therefore needs a
restart, and the restart signs everyone out. Local accounts and API tokens
keep working throughout ([enterprise identity](../enterprise-identity/)).

- **OIDC client secret**: if the provider allows two secrets for one client,
  add the new one there, write it into the file, restart, then remove the old
  one at the provider. If it allows only one, federated sign-in fails with
  [`TBY-AUTH-014`](../../reference/errors/#tby-auth-014) between the change at
  the provider and the restart. The provider's own signing keys need no
  action: they are read from its published key set, which is fetched again
  when a token names an unknown key.
- **SAML service-provider pair**: Tobby holds one pair. It signs the
  authentication requests with that key and publishes the certificate in its
  descriptor (`/auth/saml/metadata`). Register the new certificate at the
  provider (beside the old one, if the provider accepts two), install the new
  pair, restart, then remove the old certificate at the provider.
- **The provider's SAML signing certificate**: `idpCertificateFile` pins
  exactly one certificate, the first in the file. When it is set, the
  provider's rotation is a cutover: sign-ins fail with
  [`TBY-AUTH-015`](../../reference/errors/#tby-auth-015) from the moment the
  provider signs with the new key until the file is replaced and the instance
  restarted. Without `idpCertificateFile`, the signing certificates of the
  pinned `idpMetadataFile` apply, so a metadata file listing the old and the
  new certificate allows an overlap.

## Proxy password

`network.proxy.password` comes from the configuration file or
`TOBBY_NETWORK_PROXY_PASSWORD`, and is read at startup. Change the value and
restart. Every outbound path uses the one transport, so there is no second
copy to update.

## Limits

- **Re-signing a stored version does not reach the stores that hold it.**
  The rotation above works around it with new versions; a store keeps the
  signatures it first copied, for recipes and ingredients alike.
- **Removing a trust root withdraws nothing already delivered.** It stops
  future synchronizations, pushes and media imports; content already served
  by a passthrough instance or pushed to a zone registry stays.
- **A `keyURL` root can silently fall back** to a cached key when its URL is
  unreachable.
- **Key custody is the organization's.** Tobby has no revocation list and no
  key expiry. A compromised signing key stops being trusted only when its root
  is removed from every instance, and only for what is verified after that.
- **Most changes need a restart**, and a restart signs every browser session
  out and closes a destination's serving gate until the medium is verified
  again.
