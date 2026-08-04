# ADR-0009 — Authentication: OIDC, SAML, static tokens, basic auth; minimal RBAC

## Status

Accepted — 2026-07-11

## Context

Tobby exposes a web UI, a REST API, and an embedded OCI registry. Depending on the
mode, it runs as a long-lived service in a connected or restricted zone
(passthrough) or as a short-lived instance on an isolated, possibly transportable
workstation (mirror). Those contexts have irreconcilable identity situations:

- Connected and restricted zones have corporate identity providers — some modern
  (**OIDC**), some legacy (**SAML 2.0** only, with no OIDC bridge available or
  allowed). Requiring the IdP team to deploy an OIDC federation layer in front of
  a legacy SAML IdP was explicitly ruled out during scoping: Tobby must speak
  SAML natively.
- Automation (CI jobs, GitOps agents, scripts driving the REST API) needs
  non-interactive credentials that do not depend on a browser flow.
- Bootstrap scenarios and air-gapped mirror workstations may have **no IdP at
  all**, sometimes not even a network. Authentication must be able to fall back
  to local credentials — or be switched off entirely where it is meaningless
  (single operator, physically controlled machine).
- The embedded registry must accept standard clients: `docker login`, `helm
  registry login`, `oras`, `crane`. That dictates the registry token protocol,
  independent of how humans log into the UI.

## Decision

### Four authentication methods

| Method | Primary audience | Notes |
|---|---|---|
| **OIDC** | Human users via modern IdPs | Authorization Code flow; groups/claims mapped to roles |
| **SAML 2.0** | Human users via legacy IdPs | Native SP implementation using the `crewjam/saml` Go library; SP metadata endpoint for IdP onboarding |
| **Static tokens** | API clients, automation | Long-lived bearer tokens, declared in configuration or issued by an admin; stored hashed |
| **Basic auth** | Bootstrap, air-gapped mirror | Local username/password pairs (hashed in configuration); no IdP or network required |

Methods are enabled independently in configuration; several can coexist (e.g.,
OIDC for the UI plus static tokens for automation).

### Minimal RBAC

Three fixed roles, sufficient for v1 and deliberately not a policy engine:

| Role | Permissions |
|---|---|
| `viewer` | Read-only: browse Recipes, Ingredients, transfer statuses, scan results |
| `operator` | `viewer` + trigger synchronizations, imports/exports, retry failed items |
| `admin` | `operator` + configuration, credentials/trust roots, user & token management |

IdP group/attribute claims map to roles via configuration; static tokens and
basic-auth users carry an explicit role.

### Mandatory by default, explicitly disengageable

- In **passthrough** mode, authentication is **mandatory**. Tobby refuses to
  start as an unauthenticated network service unless the operator opts out
  explicitly and loudly:

  ```yaml
  # tobby config excerpt — the opt-out is deliberate, named, and logged
  auth:
    disabled: true
    disabledAcknowledgement: >-
      This instance runs on an isolated workstation with physical access
      control; network exposure is limited to localhost.
  ```

  Starting with `auth.disabled: true` emits a prominent startup warning in the
  logs and a persistent banner in the UI.
- In **mirror** mode on an isolated workstation, disabling authentication is a
  legitimate, expected configuration — the control is physical, not logical.

### Embedded registry authentication

The embedded registry implements the standard Docker/OCI **token authentication**
scheme (`WWW-Authenticate: Bearer` challenge against a token endpoint) with
**basic auth** as the initial credential — i.e., exactly what `docker login`,
`helm`, `oras`, and `crane` expect. Static tokens are also accepted directly as
bearer credentials. Registry permissions derive from the same three roles
(`viewer` → pull, `operator` → pull/push, `admin` → full).

## Consequences

### Positive

- Every deployment context in scope — modern IdP, legacy SAML-only IdP,
  headless automation, offline workstation — has a first-class path; none
  requires infrastructure Tobby's users cannot deploy.
- The default posture is safe (auth on in service mode); the opt-out is explicit,
  auditable, and impossible to enable by accident.
- Standard registry token auth means zero custom client tooling; anything that
  can log into Docker Hub can log into Tobby.
- Three fixed roles keep authorization decisions legible and testable; there is
  no policy language to audit.

### Negative

- Supporting four methods is real surface: two federation protocols, session
  management, token storage, and the registry token service all need
  hardening and tests. This is the price of the native-SAML requirement and the
  air-gap reality, accepted knowingly.
- SAML is a notoriously sharp protocol (XML signature wrapping, clock skew,
  metadata rot). Mitigation: use `crewjam/saml` (the established Go
  implementation) rather than hand-rolling, keep the SP feature set minimal
  (SP-initiated SSO only for v1), and pin/verify IdP metadata.
- Fixed roles will eventually pinch (per-Cookbook scoping, per-registry rights).
  Extending RBAC granularity post-1.0 is anticipated and the role model is
  designed to be forward-compatible (roles become role *bindings* with scopes).

### Neutral

- Password and token hashing, session cookie settings, and CSRF protection are
  implementation requirements tracked in the SRS, not decided here.

## Alternatives considered

### OIDC only, with SAML federated at the IdP level

The architecturally "clean" option: Tobby speaks one modern protocol, and
organizations bridge SAML→OIDC in their identity layer (Keycloak, Dex, ADFS…).
**Rejected during scoping**: the target environments include identity
infrastructures where deploying or modifying a federation broker is impossible in
the project's lifetime — organizationally more than technically. Native SAML in
Tobby costs one library; a federation prerequisite costs each user organization an
infrastructure project.

### Mutual TLS (client certificates)

Fits hardened environments conceptually and works offline. Rejected as a primary
mechanism because:

- the requirements explicitly state certificates may live **outside** any internal
  PKI, so no enrollment/issuance chain can be assumed for per-user client certs;
- browser UX for client certificates is poor and per-workstation, which fights
  the web UI adoption goal;
- registry clients handle mTLS unevenly compared to the universal token flow.

mTLS remains available as a transport-level hardening option in front of Tobby
(reverse proxy), orthogonal to application authentication.

### No built-in auth — delegate everything to a reverse proxy (oauth2-proxy pattern)

Minimizes Tobby's code but was rejected: it presumes a proxy deployment in every
zone including transportable mirror workstations, breaks the single-binary
self-contained model, makes the registry token flow someone else's problem, and
merely relocates the SAML requirement instead of satisfying it.
