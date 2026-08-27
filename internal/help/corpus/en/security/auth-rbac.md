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
| OIDC (authorization code) | enterprise IdP | FR-070, ADR-0009 | upcoming, milestone 6 |
| SAML 2.0 (SP-initiated) | legacy enterprise IdP | FR-071, ADR-0009 | upcoming, milestone 6 |

:::note[Upcoming — milestone 6]
Enterprise identity (OIDC then SAML, group-to-role mapping) ships at
milestone 6, alongside continuity guarantees when the IdP is down: local
accounts and tokens keep working, with their use logged distinctly
(R-20). Milestone 6 also hardens authentication further — session
inactivity expiry, optional token expiry, lifecycle audit (R-14).
Per-origin throttling of failed authentication attempts already shipped
in v0.4.2. Track all of it on the [project status](../../discover/status/) page.
:::

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

Refusals are coded, not just numbered: no credential → `TBY-AUTH-002`;
role below the floor → `TBY-AUTH-003` naming the required role; missing
anti-forgery token → `TBY-AUTH-004` (NFR-012); expired session →
`TBY-AUTH-005`. See [errors](../../reference/errors/).

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
as hashes, and revocable immediately (FR-072). Rotate them by overlap:
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
