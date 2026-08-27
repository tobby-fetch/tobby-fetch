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
| `GET /filesets` | viewer | The FileSet inventory: what is held and what is served (FR-047). |
| `POST /filesets/pack` | admin | Packing a directory of the host into a FileSet (FR-048): it reads the host filesystem and puts unsigned content in the store. Confined to `files.packRoots` on top of the role. |
| `GET /import` | operator | Importing is an operator action (FR-023). |
| `POST /import` | operator | |
| `GET /recipes`, `GET /recipes/{recipe}/mapping` | viewer | |
| `POST /recipes/sync` | operator | Triggering a synchronization is an operator action (FR-014). |
| `GET /recipes/prune-preview` | operator | The projection of what a prune would remove, shown before the synchronization that would remove it (FR-045). Offered to the role that can trigger one. |
| `GET /recipes/publish` | operator | The recipe publication form (R-40): only a role that can publish is offered one. |
| `POST /recipes/publish` | operator | Publishing writes into another zone's cookbook (R-40). Audited as outbound writing (FR-094). |
| `GET /recipes/plan` | operator | The plan form (FR-055 amendment R-04): only a role that can trigger a synchronization is offered a simulation of one. |
| `POST /recipes/plan` | operator | A plan mutates nothing, but it makes this instance reach out to every registry the submitted Retriever names. |
| `GET /media` | viewer | The Media screen (FR-062 amendment R-02): the medium's inventory summary, the verification verdicts, and the guided sequence. Reading it is a listing like any other. |
| `POST /media/verify` | operator | Starts the FR-054 verification: it re-reads and re-hashes the whole medium, which is work rather than a read. The two waivers below need admin. |
| `POST /media/import` | operator | Pushes the transported medium into the zone registry (FR-052). Audited (FR-094). The two waivers below need admin. |
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
| `GET /api/v1/cli-output.schema.json` | viewer | the CLI's `--output json` schemas (R-08), served beside the OpenAPI document |
| `GET /api/v1/content` | viewer | `GET /content` |
| `GET /api/v1/content/{repo…}` | viewer | `GET /content/{repo…}` |
| `DELETE /api/v1/content/{repo…}` | admin | `POST /content/{repo…}/-/delete` |
| `GET /api/v1/filesets` | viewer | `GET /filesets` |
| `POST /api/v1/filesets/pack` | admin | `POST /filesets/pack` |
| `POST /api/v1/import` | operator | `POST /import` |
| `GET /api/v1/import/inspect` | operator | The import screen's inspection step |
| `GET /api/v1/tasks`, `/{id}`, `/{id}/logs` | viewer | `GET /tasks…` |
| `GET /api/v1/recipes`, `/{recipe}/mapping` | viewer | `GET /recipes…` |
| `POST /api/v1/sync` | operator | `POST /recipes/sync` |
| `POST /api/v1/plan` | operator | `POST /recipes/plan` |
| `GET /api/v1/sync/prune-preview` | operator | `GET /recipes/prune-preview` |
| `POST /api/v1/recipes/publish` | operator | `POST /recipes/publish` |
| `GET /api/v1/media` | viewer | the Media screen's summary (`GET /media`) |
| `GET /api/v1/media/verification` | viewer | the serving gate and the verification in progress — what `GET /media` polls |
| `POST /api/v1/media/verify` | operator | `POST /media/verify` |
| `POST /api/v1/media/import` | operator | `POST /media/import` |
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

The three media endpoints (FR-052) carry a floor **and** an in-handler
check the floor cannot express. Verifying a medium and importing it are
operator actions; **waiving** one of the two FR-054 guards — a medium
addressed to another zone, or older than the last one imported here — is
an administrator's, and the handler refuses `allowZoneMismatch` or
`allowStale` from anyone below admin with the same `TBY-AUTH-003` the
middleware would have produced. Both the attempt and the applied waiver
are audited (FR-094). No role, admin included, can waive an integrity or
signature verdict: those have no override at all (R-19). The same rule is
enforced a second time on the UI side, in `POST /media/verify` and
`POST /media/import`: two doors, one rule, and the waiver checkboxes are
rendered only for an administrator.

`GET /api/v1/media/verification` carries no floor beyond the viewer's
because it hashes nothing — it reports the state of the serving gate
(FR-054) and of the verification currently walking the medium, which is
what makes it safe to poll every two seconds. **A closed gate is not an
authorization refusal and appears nowhere in this matrix**: an instance
holding an unverified medium answers `TBY-MED-030` (or `TBY-MED-032` for
a medium that did not clear) with `403` on `/v2/` and `/files/` to every
role, administrator included, until verification opens it. The role
matrix decides who may ask; the gate decides whether this instance has
anything it is willing to hand out yet.

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

On a **destination instance holding a transported medium**, the whole
surface sits behind the FR-054 serving gate: until a verification has
cleared the medium, every method answers `403` with the OCI error envelope
carrying `TBY-MED-030` — the message, the cause and the way out, so a
`docker pull` prints an instruction rather than a mystery. This is not a
role decision and no role bypasses it.

## FileSet surface (`/files/`)

Read-only by construction; the floor is `viewer`, except for FileSets
explicitly opted into anonymous access (`files.filesets[].anonymous`,
FR-047) for bare-host bootstrap. Those opt-ins are surfaced by a permanent
banner, like the FR-075 override.

The FR-054 serving gate applies here exactly as it does to `/v2/`, and
before authentication: an unverified medium answers `403` with the
taxonomy entry rendered as plain text, because the clients of this surface
are `apt`, `dnf` and `curl`, which show a body and know nothing of problem
documents. An anonymous FileSet is no exception — anonymity decides who
may ask, not whether the medium has been verified.

## Probes and health

`/healthz`, `/readyz`, and `/metrics` are unauthenticated: they are the
orchestrator's contract (FR-091, FR-092) and carry no instance content.

An instance withholding an unverified medium stays **live and ready**: it
is running, its storage is writable, its configuration is valid, and its
interface is serving — every one of which an operator needs in order to
press Verify. A `503` would take it out of rotation and remove the screen
that fixes the condition. `/readyz` therefore keeps its `200` and says in
its body which surfaces are closed and where to open them.

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
