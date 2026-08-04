# ADR-0007 — Signing & verification: Sigstore/cosign, key-based for air-gap

## Status

Accepted — 2026-07-11

## Context

Tobby moves OCI artifacts across trust boundaries: from a connected zone through
restricted zones, down to fully air-gapped zones, potentially on removable media
(see ADR-0006). Every hand-off in that chain is an opportunity for tampering, and
the environments Tobby targets (defense, naval, energy, other regulated industries)
mandate cryptographic proof of artifact integrity and origin.

Two categories of objects need signatures:

- **Recipes** — a Recipe published to a Cookbook is the contract describing a
  qualified software perimeter. It is stored as an OCI artifact
  (`application/vnd.tobby.recipe.v1+yaml`, see ADR-0002) and must be signed by the
  qualification pipeline that "cooked" it.
- **Ingredients** — the `ContainerImage`, `HelmChart`, `OCIArtifact` and `FileSet`
  items a Recipe references, pinned by digest once cooked.

The dominant signing ecosystem for OCI artifacts is Sigstore. Its flagship
*keyless* mode, however, relies on two online services: **Fulcio** (short-lived
certificates bound to an OIDC identity) and **Rekor** (transparency log). Neither
is reachable from a restricted or air-gapped zone, and standing up private
Fulcio/Rekor instances in every zone is operationally unrealistic for the
deployments Tobby targets. Verification must work with **zero network access**,
using only what travels with the artifacts plus locally provisioned trust material.

A further constraint comes from physical transport: signatures must move *with*
the artifacts. A signature that lives in an external database, or that must be
fetched from a separate endpoint, is useless once the storage directory is carried
across an air gap.

## Decision

Tobby adopts **Sigstore/cosign in key-based mode** for signing and verification of
both Recipes and Ingredients.

1. **Key-based signing only (for the supported offline path).** Signatures are
   produced with long-lived key pairs (`cosign sign --key ...`) by the publishing
   side (typically the qualification pipeline in the connected zone). Keyless
   signing is not required, not assumed, and never needed for verification.
2. **Trust roots distributed by configuration.** Verification public keys are
   provisioned in Tobby's configuration — inline PEM material, a local file or
   mounted secret, or an HTTPS URL fetched and cached at configuration time
   (connected instances only; air-gapped instances use inline or file forms).
   Trust roots never travel on the transport medium and are never fetched from
   the registry being verified (RECIPE-SPEC §14.4): keys found alongside the
   store are ignored. Multiple trust roots may be configured, each optionally
   scoped to registries or Cookbook repositories:

   ```yaml
   # tobby config excerpt — trust roots for signature verification
   verification:
     enabled: true
     trustRoots:
       - name: cookbook-release
         key: /etc/tobby/keys/cookbook-release.pub   # cosign public key (PEM)
         scope:
           repositories: ["registry.example.internal/cookbook/**"]
       - name: vendor-upstream
         key: /etc/tobby/keys/vendor.pub
   ```

3. **Attached signatures.** Signatures are stored in the registry next to their
   subject, following the cosign convention (signature manifest tagged
   `sha256-<digest>.sig` in the same repository). Because Tobby's storage
   directory is the transport unit (ADR-0006), attached signatures are copied
   along with the artifacts automatically — the removable medium is
   self-verifying on arrival.
4. **Verification at two enforcement points:**
   - **at import** — when Tobby pulls a Recipe or Ingredient from a source
     registry into its own store, before the artifact is accepted;
   - **before push to destination** — when Tobby promotes artifacts to a zone
     registry (passthrough mode) or when the mirror-side instance pushes into the
     destination zone registry after physical transport.

   Verifying twice is deliberate: the second check protects against tampering
   *while the artifacts were at rest or in transit on the medium*, which the
   import-time check cannot see.
5. **Failure policy.** A missing or invalid signature on a cooked Recipe or any of
   its Ingredients blocks the operation and is reported per item (API, UI, and
   structured logs — see ADR-0012). Enforcement can be relaxed per trust-root
   scope for explicitly declared low-assurance sources, but is on by default.

## Consequences

### Positive

- Verification works fully offline: only public keys (configuration) and attached
  signatures (in the store) are needed. This is the only Sigstore mode compatible
  with air-gapped operation.
- Signatures survive physical transport unchanged; a destination-zone Tobby
  instance can re-verify everything before pushing to the zone registry.
- cosign is the de-facto standard of the cloud-native ecosystem; keys, tooling and
  operator knowledge are widely available, and Tobby's own release artifacts use
  the same tooling (ADR-0011), keeping one signature ecosystem end to end.
- Trust-root scoping lets a single Tobby instance handle several publishers with
  different keys.

### Negative

- Key-based signing shifts the burden of key lifecycle management (generation,
  storage, rotation, revocation) onto the operating organization. Tobby must
  document a rotation procedure (overlapping trust roots during rollover) and the
  configuration supports multiple simultaneous keys for that reason.
- No transparency log: there is no public, append-only record of what was signed.
  This is an accepted trade-off — the environments in scope forbid the outbound
  connectivity a transparency log requires, and the qualification pipeline's own
  audit trail plays that role internally.
- Compromise of a signing key compromises all artifacts it scoped; mitigations
  (distinct keys per pipeline stage, HSM/KMS storage on the signing side) are
  outside Tobby but must be called out in operator documentation.

### Neutral

- Nothing prevents an organization operating only connected zones from layering
  keyless signatures on top; Tobby simply does not depend on them. Native keyless
  verification support may be revisited post-1.0 if demand appears.

## Alternatives considered

### Notation (Notary Project v2)

CNCF's Notation also signs OCI artifacts with keys and attaches signatures in the
registry, so it is functionally close. Rejected because:

- the target user base and the surrounding ecosystem (Kubernetes admission
  controllers, policy engines, registry tooling) have converged on cosign;
  interoperability expectations for Tobby explicitly name cosign signatures on
  Recipes (ADR-0002);
- Tobby's own supply chain (ADR-0011: slsa-github-generator, melange/apko) is
  Sigstore-native; adding a second signature ecosystem doubles trust-material
  formats and operator concepts for no functional gain;
- registry compatibility for the Referrers API is still uneven, whereas the cosign
  tag-based fallback works on any OCI registry, including Tobby's embedded one.

### GPG / detached signatures (skopeo-style)

Signing image manifests with GPG (as in `containers/image` policy.json) is proven
and fully offline. Rejected because:

- signatures are not OCI artifacts: they require a separate lookaside store or
  sigstore-attachment extensions, which breaks the "everything travels in the OCI
  store" transport model;
- coverage is image-centric; HelmChart/OCIArtifact/FileSet ingredients and Recipe
  artifacts would need bespoke conventions;
- GPG key handling and web-of-trust semantics are a well-documented source of
  operator error, and ecosystem momentum is elsewhere.

### No signing (rely on digests only)

Digest pinning already guarantees *integrity* of what was fetched. Rejected as the
sole mechanism because digests provide no *authenticity*: nothing proves that a
digest was produced and approved by the qualification pipeline rather than by
whoever wrote to the source registry or handled the removable medium. For
artifacts crossing an air gap on physical media, origin authentication is a hard
requirement, not an option.
