---
title: Authentication, accounts and RBAC
description: The authentication methods and their status, the three roles, the full role-per-surface matrix, and the guardrails around disabling authentication.
sidebar:
  order: 4
---

Authentication is **on by default in both modes** (FR-075 — delivered
v0.4.0). Out of the box an instance uses locally managed basic
authentication; an instance with no account refuses to start rather than
start open.

## Methods and their status

| Method | Use case | Reference | Status |
| --- | --- | --- | --- |
| Basic auth, local accounts | default, isolated hosts | FR-073, ADR-0009 | delivered v0.4.0 |
| Static bearer tokens | API automation, CI | FR-072, ADR-0009 | delivered v0.4.0 |
| OIDC (authorization code) | enterprise IdP | FR-070, ADR-0009 | delivered v0.6.0 |
| SAML 2.0 (SP-initiated) | legacy enterprise IdP | FR-071, ADR-0009 | delivered v0.6.0 |

The four methods coexist: enabling OIDC or SAML never disables local
accounts or tokens, and they keep working when the identity provider is
down (FR-078). Configuration, group-to-role mapping, the session and token
lifetimes and the outage behaviour are on
[Enterprise identity](../enterprise-identity/). Per-origin throttling of
failed authentication attempts shipped in v0.4.2; idle session expiry and
optional token expiry (R-14) with v0.6.0.

## The three roles

A closed set, ordered `viewer < operator < admin`; each role grants
everything below it (FR-074, ADR-0009).

| Role | Grants |
| --- | --- |
| `viewer` | Read the instance: content, tasks, recipes, help. Manage its own password. Pull from the embedded registry. |
| `operator` | + the actions that move content in: unit import, synchronization triggers, recipe publication, registry pushes. |
| `admin` | + administration: accounts, tokens, content removal, and the screens that reveal instance configuration. |

## Role floors per surface

This is the published form of the enforced matrix
([`docs/rbac-matrix.md`](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/rbac-matrix.md)).
The API mirrors the UI action for action with identical floors (FR-061),
so they share one table.

| Action | UI route | API endpoint | Floor |
| --- | --- | --- | --- |
| Sign-in page, language, theme | `GET/POST /login`, `POST /lang`, `POST /theme` | — | public (deliberately minimal) |
| Federated sign-in round trips, SP descriptor | `/auth/oidc/…`, `/auth/saml/…` (only when enabled) | — | public |
| Dashboard, tasks, content, recipes, mapping, help, about, API docs | `GET /`, `/tasks…`, `/content…`, `/recipes…`, `/help`, `/about…`, `/api-docs` | `GET /api/v1/content…`, `/tasks…`, `/recipes…`, `/openapi.yaml` | viewer |
| Own password change | `POST /account/password` | `POST /api/v1/account/password` | any authenticated role |
| Unit import (and inspection) | `GET/POST /import` | `POST /api/v1/import`, `GET /api/v1/import/inspect` | operator |
| Trigger synchronization | `POST /recipes/sync` | `POST /api/v1/sync` | operator |
| Publish a recipe | `GET/POST /recipes/publish` | `POST /api/v1/recipes/publish` | operator |
| Delete unit-imported content | `POST /content/{repo}/-/delete` | `DELETE /api/v1/content/{repo}` | admin |
| Accounts, roles, tokens | `/admin/accounts` + `POST` routes | `/api/v1/accounts…`, `/api/v1/tokens…` | admin |
| Retriever source and interval | `/admin/retriever…` | `/api/v1/retriever…` | admin |
| Network screen, certificate replacement | `/admin/network…` | `/api/v1/network…` | admin |

The embedded registry (`/v2/`) is gated **by HTTP method**, not by path,
so standard clients work unmodified (FR-076): `GET`/`HEAD` (pull,
catalog, tag list) require `viewer`; everything else (push, blob upload,
delete) requires `operator`. An unauthenticated request gets a `401` with
a `Basic` challenge. `/files/` is read-only with a `viewer` floor, except
FileSets explicitly opted into anonymous access for bare-host bootstrap —
surfaced by a permanent banner (FR-047). `/healthz`, `/readyz`, and
`/metrics` are unauthenticated: the orchestrator's contract, no instance
content.

**Cross-site request forgery (NFR-012).** The session cookie is ambient: the
browser attaches it on its own. Every request it authenticates whose method
is not `GET`, `HEAD` or `OPTIONS` must therefore carry the session's
anti-forgery token: the `csrf` field of the interface's forms, or the
`X-CSRF-Token` header on an API call made from a signed-in page. With the
token, an API call is served as the session's identity, and the
per-endpoint floor still applies. Without it, the cookie alone authorizes
no API mutation. As defense in depth, such a request is also refused when
the browser says it comes from another origin. That verdict comes from
`Sec-Fetch-Site` when the browser sends it, and otherwise from `Origin`.
`Origin` is compared with the request's `Host`, or with `X-Forwarded-Host`
behind a reverse proxy that rewrites `Host`; the scheme is not compared,
so TLS can be terminated in front. Basic and Bearer credentials carry no
ambient authority and are exempt. So are the federated callbacks under
`/auth/`, which carry their own `state` and `InResponseTo` checks. The
sign-in form and, under the FR-075 override, every state-changing request
without a credential are refused on a cross-origin verdict alone.

`Origin: null` counts as another origin. A browser sends it for a
sandboxed frame, and for every form post of a page served with
`Referrer-Policy: no-referrer`, so a forging page can choose it. The
instance's own pages are served with `Referrer-Policy: same-origin`: no
referrer ever leaves for another site, and the browser sends their real
`Origin` on their own posts. A request with no `Origin` and no
`Sec-Fetch-Site` passes the check (curl, the CLI). A browser cannot use
that path against the API: every API endpoint that reads a document
requires `Content-Type: application/json` (`TBY-SRV-005` otherwise), a
type a page of another origin cannot send without a CORS preflight, and
the instance grants none.

Refusals are coded, not just numbered: no credential → `TBY-AUTH-002`;
role below the floor → `TBY-AUTH-003` naming the required role; missing
or forged anti-forgery token, or cross-origin state change →
`TBY-AUTH-004` (NFR-012); expired session →
`TBY-AUTH-005`; expired token → `TBY-AUTH-013`; federated sign-in refused
→ `TBY-AUTH-014` (OIDC), `TBY-AUTH-015` (SAML), provider unreachable →
`TBY-AUTH-016`, no mapped group → `TBY-AUTH-017`. See [errors](../../reference/errors/).

**The matrix cannot drift.** `internal/ui/rbac_matrix_test.go` walks the
route table the server actually mounts and fails if any route is missing
from the documented matrix; `TestRBACMatrixMirrorsUIFloors` pins every
UI/API pair. A route added later cannot ship with an unreviewed floor.
This is an executable proof, not a review habit — see
[tests and proofs](../../project/tests-and-proofs/).

## Accounts and tokens in practice

Accounts are managed with `tobby user add | passwd | list` on the host
(the first account is forced `admin`; `--password-stdin` for automation)
and from `/admin/accounts` or the API. The argon2id hash is always
computed by the tool — no surface anywhere accepts a pre-computed hash.
Every authenticated account can change its own password, providing the
current one; success and failure are both audit-logged (FR-077 —
delivered v0.3.0).

API tokens carry their own role, are shown once at creation, stored only
as hashes, and revocable immediately (FR-072). They may carry an expiry
(R-14), shown in every listing; an expired token is refused with its own
code and the refusal is audited. Rotate them by overlap:
create the replacement token, roll the clients, revoke the old one —
no window without a valid credential.

`docker login`, `podman login`, `helm registry login`, and `oras login`
use these same accounts and tokens against `/v2/` (FR-076): one identity
system for every surface.

Two invariants are enforced inside the account store itself, under lock,
so no surface can bypass them: the last `admin` account can be neither
deleted (`TBY-AUTH-011`) nor demoted — including by itself.

## Disabling authentication

`auth.disabled` exists for deliberately isolated setups. It is settable
only in the configuration file or `TOBBY_AUTH_DISABLED` — never by flag —
and it is never silent (FR-075): every request then carries the explicit
`anonymous` identity with the `admin` role, the UI shows a permanent
banner, and the startup emits an `auth.override_active`
[audit record](../../security/audit-log/). The default posture, and the only
supported one, is authentication on.

Under the override, the origin check is the only protection against
forged requests. It does not stop DNS rebinding: a hostile site that
re-points its own name at the instance's address is, for the browser,
the same origin as itself. The instance does not validate the `Host`
header, since it has no configured public name to compare it with. Keep
an instance with authentication off on a network that the browsers of
people who visit untrusted sites cannot reach, or put a reverse proxy in
front that only answers its own host name.
