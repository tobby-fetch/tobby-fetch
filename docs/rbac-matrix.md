# Role × route permission matrix

FR-074 requires "a documented and enforced matrix, with negative tests
confirming each role's refusals". This page is the documented half. The
enforced half is `internal/ui/rbac_matrix_test.go`, which walks the route
table that `ui.UI.Mount` and `api.API.Handle` actually register and fails
if any route is missing from the matrix — so a route added later cannot
ship with an unreviewed role floor, and this page cannot silently go stale
without the test noticing the route it does not know about.

Keep the two in step: a change here without a change there (or the
reverse) is a defect.

## The role ladder

Three roles, closed set (ADR-0009), ordered `viewer < operator < admin`.
A role grants everything the roles below it grant.

| Role | Grants |
| --- | --- |
| `viewer` | Read the instance: content, tasks, recipes, help. Manage its own account (password). Pull from the embedded registry. |
| `operator` | Everything a viewer can, plus the actions that move content in: unit import, synchronization triggers, pushes to the embedded registry. |
| `admin` | Everything an operator can, plus administration: accounts, API tokens, content removal, and the screens that reveal instance configuration. |

Under the FR-075 authentication override (`auth.disabled`) every request
carries the explicit `anonymous` identity with the `admin` role: the
barrier is removed entirely, and the permanent banner plus the startup
audit record carry the visibility. The matrix below describes an instance
with authentication on, which is the default and the only supported
posture.

## Web UI (`/`)

The UI owns the root of the listener (ADR-0015). "Public" means reachable
without a session; the set is deliberately minimal (R-01: the instance is
never exposed open), and every member exists so that signing in is
possible at all.

| Route | Floor | Notes |
| --- | --- | --- |
| `GET /static/` | public | Stylesheet and scripts the login page itself needs. |
| `GET /login` | public | The sign-in form. |
| `POST /login` | public | Credential submission. Failures answer TBY-AUTH-002 without revealing whether the account exists. |
| `POST /lang` | public | The login page offers the language switcher (FR-063). |
| `POST /theme` | public | Presentation preference, offered pre-login like the language. |
| `POST /logout` | viewer | |
| `GET /` | viewer | Dashboard (FR-062). |
| `GET /tasks`, `GET /tasks/{id}`, `GET /tasks/badge` | viewer | |
| `GET /content`, `GET /content/{repo…}` | viewer | |
| `POST /content/{repo…}/-/delete` | admin | Removal of unit-imported content (FR-044 amendment). |
| `GET /import` | operator | Importing is an operator action (FR-023). |
| `POST /import` | operator | |
| `GET /recipes`, `GET /recipes/{recipe}/mapping` | viewer | |
| `POST /recipes/sync` | operator | Triggering a synchronization is an operator action (FR-014). |
| `GET /recipes/publish` | operator | The recipe publication form (R-40): only a role that can publish is offered one. |
| `POST /recipes/publish` | operator | Publishing writes into another zone's cookbook (R-40). Audited as outbound writing (FR-094). |
| `GET /recipes/plan` | operator | The plan form (FR-055 amendment R-04): only a role that can trigger a synchronization is offered a simulation of one. |
| `POST /recipes/plan` | operator | A plan mutates nothing, but it makes this instance reach out to every registry the submitted Retriever names. |
| `GET /account` | viewer | Self-service: every authenticated role manages its own account (R-34). |
| `POST /account/password` | viewer | Own password only. Administrators manage others on `/admin/accounts`. |
| `GET /admin/accounts` | admin | |
| `POST /admin/accounts` | admin | Account creation (FR-073). |
| `POST /admin/accounts/role` | admin | Role change (FR-074). |
| `POST /admin/accounts/delete` | admin | Account removal (FR-073). |
| `POST /admin/accounts/tokens` | admin | Token creation (FR-072). |
| `POST /admin/accounts/tokens/revoke` | admin | Token revocation (FR-072). |
| `GET /admin/retriever` | admin | Reveals the configured desired-state source (FR-010). |
| `POST /admin/retriever/interval` | admin | Changes how often this instance promotes, unattended (FR-013). The change is audited as sensitive configuration (FR-094). |
| `GET /admin/network` | admin | Reveals this instance's own TLS identity and its outbound path (FR-082, FR-080, FR-081). |
| `POST /admin/network/certificate` | admin | Replaces the listener's certificate — what every client of this instance authenticates against (FR-082). Audited as sensitive configuration (FR-094). |
| `GET /admin/oci-layout` | admin | The OCI image layout export/import screen (FR-051). |
| `POST /admin/oci-layout/plan` | admin | The side-effect-free estimate of the export (FR-055): same selection surface, same floor. |
| `POST /admin/oci-layout/export` | admin | Writes the store's content to a path on the host filesystem — an administrative capability, not an operator one (FR-051). Audited (FR-094). |
| `POST /admin/oci-layout/import` | admin | Brings outside bytes into the store (FR-051). Audited (FR-094). |
| `GET /admin/store` | admin | What the store holds, and the reset that empties it (FR-046). |
| `POST /admin/store/reset` | admin | Full store reset, restricted to the admin role by FR-046, behind a typed confirmation and audited (FR-094). |
| `GET /help`, `GET /about`, `GET /about/third-party`, `GET /api-docs` | viewer | |
| `GET /help/{page...}`, `GET /help/-/assets/{name}` | viewer | The operations guides embedded in the binary and their screenshots (NFR-003, amendment 2026-08-11). Readable by whoever operates the instance — and by nobody who has not signed in (R-01). |
| anything else | viewer | The taxonomized 404 renders inside the authenticated shell (UI-SPEC §5.13). |

Every mutating UI route additionally requires the session's anti-forgery
token (NFR-012); a missing or stale one answers TBY-AUTH-004, which is a
different refusal from the role refusal below.

## REST API (`/api/v1`)

The API is the strict mirror of the UI (FR-061): each action carries the
same floor on both surfaces, and `TestRBACMatrixMirrorsUIFloors` pins the
pairs. Authentication is Basic (account password or token secret) or
Bearer (token secret); a live UI session additionally authenticates `GET`
and `HEAD`, so a signed-in page can link to API documents.

| Endpoint | Floor | Mirrors |
| --- | --- | --- |
| `GET /api/v1/openapi.yaml` | viewer | `/api-docs` |
| `GET /api/v1/content` | viewer | `GET /content` |
| `GET /api/v1/content/{repo…}` | viewer | `GET /content/{repo…}` |
| `DELETE /api/v1/content/{repo…}` | admin | `POST /content/{repo…}/-/delete` |
| `POST /api/v1/import` | operator | `POST /import` |
| `GET /api/v1/import/inspect` | operator | The import screen's inspection step |
| `GET /api/v1/tasks`, `/{id}`, `/{id}/logs` | viewer | `GET /tasks…` |
| `GET /api/v1/recipes`, `/{recipe}/mapping` | viewer | `GET /recipes…` |
| `POST /api/v1/sync` | operator | `POST /recipes/sync` |
| `POST /api/v1/plan` | operator | `POST /recipes/plan` |
| `POST /api/v1/recipes/publish` | operator | `POST /recipes/publish` |
| `GET /api/v1/network` | admin | `GET /admin/network` |
| `PUT /api/v1/network/certificate` | admin | `POST /admin/network/certificate` |
| `GET /api/v1/retriever` | admin | `GET /admin/retriever` |
| `PUT /api/v1/retriever/interval` | admin | `POST /admin/retriever/interval` |
| `DELETE /api/v1/retriever/interval` | admin | `POST /admin/retriever/interval` |
| `POST /api/v1/account/password` | any authenticated role | `POST /account/password` |
| `GET /api/v1/accounts` | admin | `GET /admin/accounts` |
| `POST /api/v1/accounts` | admin | `POST /admin/accounts` |
| `PATCH /api/v1/accounts/{name}` | admin | `POST /admin/accounts/role` |
| `DELETE /api/v1/accounts/{name}` | admin | `POST /admin/accounts/delete` |
| `GET /api/v1/tokens` | admin | the token table of `/admin/accounts` |
| `POST /api/v1/tokens` | admin | `POST /admin/accounts/tokens` |
| `POST /api/v1/tokens/{name}/revoke` | admin | `POST /admin/accounts/tokens/revoke` |
| `POST /api/v1/oci-layout/plan` | admin | `POST /admin/oci-layout/plan` |
| `POST /api/v1/oci-layout/export` | admin | `POST /admin/oci-layout/export` |
| `POST /api/v1/oci-layout/import` | admin | `POST /admin/oci-layout/import` |
| `POST /api/v1/store/reset` | admin | `POST /admin/store/reset` |

`POST /api/v1/account/password` has no floor beyond authentication itself,
exactly like its screen: it changes the caller's own password and nothing
else. An API token carries no password, so a token caller fails the
current-password check like any wrong credential.

## Embedded registry (`/v2/`)

The OCI Distribution API is gated by method, not by path, so that
`docker`, `helm`, and `oras` work unmodified against it (FR-076) with the
same accounts and tokens as the UI and the API.

| Method | Floor |
| --- | --- |
| `GET`, `HEAD` (pull, catalog, tag list) | viewer |
| everything else (push, blob upload, delete) | operator |

An unauthenticated request answers `401` with a `Basic` challenge, which
is what makes standard clients prompt for credentials.

## FileSet surface (`/files/`)

Read-only by construction; the floor is `viewer`, except for FileSets
explicitly opted into anonymous access (`files.filesets[].anonymous`,
FR-047) for bare-host bootstrap. Those opt-ins are surfaced by a permanent
banner, like the FR-075 override.

## Probes and health

`/healthz`, `/readyz`, and `/metrics` are unauthenticated: they are the
orchestrator's contract (FR-091, FR-092) and carry no instance content.

## Refusals

| Condition | Code | Status |
| --- | --- | --- |
| No session / no credential | TBY-AUTH-002 (API), redirect to `/login` (UI) | 401 / 303 |
| Role below the route's floor | TBY-AUTH-003, naming the required role | 403 |
| Missing or stale anti-forgery token | TBY-AUTH-004 | 403 |
| Expired session | TBY-AUTH-005 | 401 |

A `403` is not by itself a role refusal: policy barriers (`TBY-POL-*`) and
the anti-forgery check share the status. The code is what distinguishes
them, which is why the enforcement test reads both.

## Safety invariants on the account lifecycle

Independent of the matrix, and enforced in `auth.Store` under its own lock
so no surface can bypass them:

- The last `admin` account cannot be deleted (TBY-AUTH-011).
- The last `admin` account cannot be demoted, including by itself.

Both would leave the instance unmanageable, and FR-005 makes an instance
with no account refuse to start at all. Creating a second administrator
first is the way through — from `/admin/accounts`, from
`POST /api/v1/accounts`, or on the host with `tobby user add --role admin`.

## Secret exposure on the network screens

`GET /api/v1/network` and `/admin/network` report the served certificate —
fingerprint, subject, issuer, subject alternative names, validity — because
a certificate is public by construction: the listener hands those exact
bytes to every client that completes a handshake. The private key is
returned by nothing, in no form: not its bytes, not its length, not a
digest of it (a digest would be a stable oracle against a candidate key).
`internal/tlsadmin` has no accessor for key material at all, so neither
surface could return it by mistake. The replacement submits the key — as a
file upload on the screen, as a body member on the API — and the only value
that comes back is the new certificate's fingerprint. Proxy credentials are
reported as a boolean and never as a value (FR-080, NFR-015).

Password hashes are always computed by the tool (FR-066). No field on any
surface accepts a hash: the creation form and the API body carry a clear
password over the authenticated connection, and `auth.Store` derives the
argon2id hash.
