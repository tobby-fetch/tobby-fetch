---
title: Automate with scripts and CI
description: Drive an instance from a script or a pipeline — API tokens, triggering a synchronization and following the task, the plan gate and its exit codes, publishing a recipe, and reading errors without parsing prose.
sidebar:
  order: 15
---

Everything an operator does in the interface has a mirror on the API, and
the commands that matter to a pipeline are thin clients of it. This page is
the working set for a script or a CI job: which credential to create,
which calls to make, which numbers to branch on, and a small job that
puts them together. The reference pages state the contract
([API](../../reference/api/), [CLI](../../reference/cli/)); this one shows
it running.

:::tip[In short]
- Create an API token with the **operator** role on `/admin/accounts` (or
  `POST /api/v1/tokens`) and send it as `Authorization: Bearer <secret>`.
  The secret is shown once.
- `tobby sync --wait` triggers a synchronization, follows the task and
  **exits on its outcome**; `--output json` leaves a report alone on
  standard output.
- `tobby sync --dry-run` is the plan gate: exit `0` nothing to do, `5`
  changes planned, `3` policy refusal, `4` verification failure.
  `POST /api/v1/plan` is its API twin, for a runner that has no store.
- `TOBBY_INSTANCE_URL` and `TOBBY_API_TOKEN` are all a runner needs to
  drive a remote instance.
- Every API error is a problem document: branch on its `code`, never on its
  text.
:::

:::note[About the outputs on this page]
They come from a replay: instances on loopback with throwaway registries,
reading signed recipes from a local cookbook. Hosts, task identifiers and
digests are those of the replay. Tokens in the examples are placeholders.
:::

## Create an API token

Tokens are an **administrator** action, and there is deliberately no
`tobby token` command: a token is created on a running instance, from
the interface or the API.

- **Interface:** the accounts screen, `/admin/accounts`. The form
  asks for a name, a role and an optional lifetime in days; the secret is
  displayed once, on the page that answers.
- **API:** `POST /api/v1/tokens`, authenticated as an administrator (Basic
  `account:password`, or an admin token):

```sh
curl -sS -u admin -X POST "$TOBBY_INSTANCE_URL/api/v1/tokens" \
  -H 'Content-Type: application/json' \
  -d '{"name":"ci-sync","role":"operator","expires":"2026-12-31T00:00:00Z"}'
```

```json
{"expires":"2026-12-31T00:00:00Z","name":"ci-sync","role":"operator","secret":"tby_<the secret, shown once>"}
```

| Member | Meaning |
| --- | --- |
| `name` | Unique on the instance. Visible in listings and in the audit trail as the actor of everything the token does. |
| `role` | `viewer`, `operator` or `admin`. Pick the lowest that works (below). |
| `expires` | Optional, RFC 3339. Absent: the instance's `auth.tokens.defaultLifetime` applies if set, otherwise the token does not expire. |
| `secret` | The credential. Returned **exactly once**; only its hash is stored. Put it straight into the CI secret store. |

A token carries one role for its life:

| The job does | Role |
| --- | --- |
| Reads tasks, recipes, content, the OpenAPI document | `viewer` |
| Triggers a synchronization, plans, publishes a recipe, verifies the store | `operator` |
| Creates tokens or accounts, reads the Retriever source and the effective policy, collects a diagnostic bundle | `admin` |

Create one token per pipeline, not one per team: the listing
(`GET /api/v1/tokens`, admin) shows `name`, `role`, `created`, `revoked`
and `expires` — never the secret — and a revocation then cuts exactly one
pipeline.

```sh
curl -sS -u admin "$TOBBY_INSTANCE_URL/api/v1/tokens"
```

```json
{"tokens":[{"name":"ci-sync","role":"operator","created":"2026-11-22T17:32:17.393144Z","revoked":false,"expires":"2026-12-31T00:00:00Z"}]}
```

Rotate by overlap: create the replacement, roll the clients, then revoke
the old one with `POST /api/v1/tokens/{name}/revoke` (immediate and
permanent). An expired token is refused with its own code,
[`TBY-AUTH-013`](../../reference/errors/#tby-auth-013). Roles and
the rest of the account model are in
[Authentication, accounts and RBAC](../../security/auth-rbac/).

## Call the API

The token goes in a `Bearer` header, over HTTPS:

```sh
export TOBBY_INSTANCE_URL=https://tobby.example:8443
export TOBBY_API_TOKEN=<the secret>

curl -sS -H "Authorization: Bearer $TOBBY_API_TOKEN" \
  "$TOBBY_INSTANCE_URL/api/v1/tasks?type=sync&status=done"
```

The instance serves its own contract: `GET /api/v1/openapi.yaml` for the
endpoints and `GET /api/v1/cli-output.schema.json` for the documents the
CLI writes, both readable by a `viewer`. Lists take the same parameters as
the screens (`/api/v1/tasks` accepts `q`, `status`, `type` and `page`) and
answer `{"tasks":[…],"page":1,"totalPages":1,"total":3}`.

## Trigger a synchronization and follow the task

A synchronization is a task. Create it, then read it until it is no longer
active.

```sh
curl -sS -X POST "$TOBBY_INSTANCE_URL/api/v1/sync" \
  -H "Authorization: Bearer $TOBBY_API_TOKEN"
```

The answer is `201` with the task in an envelope:

```json
{"task":{"id":"tsk_aa4e4ea01da6","run_id":"20261122T173230Z-29a9176f","type":"sync","reference":"./a/retriever.yaml","actor":"ci-sync","status":"running","created":"2026-11-22T17:32:30.392505Z","started":"2026-11-22T17:32:30.392756Z","items":null,"items_total":0,"items_done":0,"items_failed":0,"items_skipped":0}}
```

`reference` is the configured Retriever source, `actor` the token's name.
The body is optional: `{"prune": true}` or `{"prune": false}` decides, for
this run, whether content that no recipe reaches any more is removed;
leaving it out takes the instance's own setting. A body that is not that
object answers `422` ([`TBY-VAL-001`](../../reference/errors/#tby-val-001)).
An instance with no `retriever.source` refuses the trigger
([`TBY-CFG-001`](../../reference/errors/#tby-cfg-001)).

Follow it with `GET /api/v1/tasks/{id}`:

```sh
curl -sS -H "Authorization: Bearer $TOBBY_API_TOKEN" \
  "$TOBBY_INSTANCE_URL/api/v1/tasks/tsk_aa4e4ea01da6" | jq -c '.task | {status, items_total, items_done, items_failed, items_skipped}'
```

```json
{"status":"done","items_total":4,"items_done":4,"items_failed":0,"items_skipped":0}
```

| `status` | Meaning |
| --- | --- |
| `pending`, `running` | Active: keep polling. |
| `done` | Finished. A task can be `done` with some items `skipped` (already up to date). |
| `failed` | Finished with at least one failed item. `principal_code` carries the code of the one that sets the verdict. |

Poll every few seconds: a synchronization can take hours, and a poll
survives an instance restart where a long connection does not — a task
interrupted by a restart comes back and the next poll finds it. Each item
of `items[]` has a `name`, a `status`, a `digest` and `size_bytes` once
known, and, when it failed, an `error` with the `code` and its parameters:

```sh
curl -sS -H "Authorization: Bearer $TOBBY_API_TOKEN" "$TOBBY_INSTANCE_URL/api/v1/tasks/$ID" \
  | jq -r '.task | .status, (.items[] | select(.error) | "  \(.name): \(.error.code)")'
```

The task's log is `GET /api/v1/tasks/{id}/logs?after=<offset>`, which
answers `{"chunk":"<JSON lines>","next":<offset>}` — call again with
`after=<next>` to read on — and `?format=raw` downloads it whole.

### The CLI does the polling for you

`tobby sync --wait` is that loop, with a deadline and an exit code:

```sh
export TOBBY_INSTANCE_URL=https://tobby.example:8443
export TOBBY_API_TOKEN=<the secret>
tobby sync --wait --wait-timeout 30m --output json
```

It creates the task, polls every two seconds, prints the report once the
task has finished, and **exits on the task's own outcome**, mapped through
the [exit-code table](../../reference/cli/#exit-codes): a failed task exits
with the class of its principal error, so a refusal for policy on the
instance is `3` on the runner, a signature that no trust root validates is
`4`. A failure of another kind is `1`.

- Narration (`task … created on …, waiting for it to finish`) goes to
  standard error. With `--output json`, standard output carries exactly
  one document, with three members: `instance` (the base URL driven),
  `waited` (`true` under `--wait`) and `task`: the task as the instance
  stores it (`id`, `run_id`, `type`, `reference`, `actor`, `status`, the
  timestamps and `items[]`). The counters `items_total` and the like, and
  `principal_code`, are added by the API only: from the CLI, read
  `task.status` and `task.items[].error.code`.
- `--wait-timeout` bounds the wait, not the work. When it expires the
  command says which task is still running and where, and exits `1`; the
  synchronization continues on the instance.
- `--prune` and `--prune=false` map onto the body above.
- Without `--wait` the command returns as soon as the task is created.

A failed run, replayed against an instance that did not hold the signing
key (the text report; with `--output json` the same fields are in the
document):

```text
task tsk_97de26f68682 on http://127.0.0.1:8093
  type:      sync
  reference: oci://127.0.0.1:5012/config/zone-b:v1
  status:    failed
  duration:  0s
  items:     1 (0 done, 0 skipped, 1 failed)
  failed   alpine@3.22.0 — TBY-SIG-001
  principal: TBY-SIG-001 (verification)
```

```text
TBY-SIG-001 — The recipe signature could not be verified.
  cause:  No trust root of the default set validates the signature of alpine (keys tried: sha256:decef1e4…).
  action: Check that the zone's trust roots include the key that signed this recipe, …
  see:    /help#TBY-SIG-001    correlation: 20261122T173440Z-9fbffa86
```

The second block is on standard error, the process exit code is `4`.

## The plan gate

A plan says what a synchronization would do without doing any of it. It
is the step a pipeline puts in front of a Retriever change.

```sh
tobby sync --dry-run --retriever ./retriever.yaml --output json
```

`--retriever` accepts a file, an HTTP(S) URL or an OCI reference: the
candidate is planned instead of the configured document. The exit code is
the verdict:

| Exit | `outcome` in the report | Meaning for a gate |
| --- | --- | --- |
| `0` | `up-to-date` | Nothing to do. |
| `5` | `changes-planned` | Work is planned: new or outdated ingredients, or a prune. A success that deserves a look, not a broken build. |
| `3` | `policy-refused` | A registry is outside `registries.allowlist` ([`TBY-POL-001`](../../reference/errors/#tby-pol-001)). |
| `4` | `verification-failed` | A recipe signature no trust root validates, or content that does not match its pinned digest ([`TBY-SIG-001`](../../reference/errors/#tby-sig-001)). |
| `1` | `failed` | The plan could not complete: a registry that does not answer, a recipe or version that does not exist, an unreadable store. |
| `2` | — | Usage error: for example `--wait` or `--instance` given with `--dry-run`. |

Replayed on a zone whose signing key was not configured:

```text
plan of oci://127.0.0.1:5012/config/zone-b:v1 (zone zone-b)
  outcome: verification-failed
…
recipes
  NAME    REQUESTED  RESOLVED  SIGNATURE  SCOPE  TRANSFER
  alpine  3.22.x     3.22.0    refused    -      0 B
…
problems
  TBY-SIG-001  verification   alpine
```

The process exited `4`. The JSON form (`--output json`) has these members:
`mode`, `source`, `source_is_candidate`, `zone`, `outcome`, `totals`,
`recipes`, `policy`, `prune`, `destination`, `checks`, `generated_at`, and
the schema is in `GET /api/v1/cli-output.schema.json`. It carries the
verdict in `outcome`, and **no `exit_code` member**: read the exit status
of the process, or `outcome`.

**Where the plan runs matters.** `tobby sync --dry-run` plans on the host
that holds the instance's configuration and store directory; it contacts no
instance, which is why `--instance` and `--wait` are refused with it, and
why `TOBBY_INSTANCE_URL` has no effect on it. A CI runner that has neither
uses the API instead:

```sh
jq -Rs '{document: ., skip_destination: true}' retriever.yaml |
  curl -sS -X POST "$TOBBY_INSTANCE_URL/api/v1/plan" \
    -H "Authorization: Bearer $TOBBY_API_TOKEN" \
    -H 'Content-Type: application/json' --data-binary @-
```

`POST /api/v1/plan` (operator role) takes three optional members: `document`
(a candidate Retriever **inline**, as YAML text, so a file that exists only in
the merge request can be planned), `retriever` (a source the *instance* can
read: a file on its host, a URL, an OCI reference) and `skip_destination`.
An empty body plans the configured Retriever. It answers **`200` whatever
the outcome** — a plan is a report — with `{"plan": …, "exit_code": …}`:
branch on `exit_code`, the number the CLI would have exited with, never on
the HTTP status. Problems are in `plan.problems[]`, each with a `code`, a
`class` and the `subject` they concern. Planning a candidate that names a
recipe the cookbook does not hold, trimmed with
`jq -c '{exit_code, outcome: .plan.outcome, problems: .plan.problems}'`:

```json
{"exit_code":1,"outcome":"failed","problems":[{"code":"TBY-REG-005","class":"operational","subject":"ghost","params":{"reference":"ghost"}}]}
```

The plan runs the
instance's own egress: it opens connections to every registry the
candidate names, which is why the role is `operator` and not `viewer`.

## Publish a recipe from CI

Publishing is two steps on purpose: Tobby checks and publishes, a key you
own signs.

```sh
digest=$(tobby recipe push alpine.yaml registry.example.com/cookbook/alpine:3.22.0 \
  --output json | jq -r '.digest')
cosign sign --key env://COSIGN_PRIVATE_KEY --yes \
  --use-signing-config=false --tlog-upload=false \
  "registry.example.com/cookbook/alpine@$digest"
```

`tobby recipe push` refuses a document that is not a valid, fully pinned
recipe, or whose name and version contradict the reference, and it never
overwrites a published version. Its report, with `--output json`:

```json
{
  "reference": "127.0.0.1:5011/cookbook/alpine:3.22.0",
  "repository": "127.0.0.1:5011/cookbook/alpine",
  "digest": "sha256:8fbf02f6…",
  "unchanged": true
}
```

- `unchanged: true` means the same document is already published: a rerun
  is a no-op. Sign anyway — a previous job may have published and stopped
  before signing, and signing a digest twice is harmless.
- A different document under a published version exits `3` with
  [`TBY-POL-004`](../../reference/errors/#tby-pol-004): publish under a new
  `metadata.version`.
- Credentials for the registry come from the configuration
  (`registries.credentialsFile`), like every outbound call, with the
  usual `TOBBY_*` environment variables on top; the command needs no mode
  and no store. Without a matching entry the registry's refusal is
  [`TBY-REG-003`](../../reference/errors/#tby-reg-003), exit `1`.
- `cosign` stays outside Tobby, which never holds a private key. The two
  flags keep the signature verifiable offline and are explained in
  [Your first promotion](../../try/first-promotion/#4-publish-and-sign-it).
  `env://COSIGN_PRIVATE_KEY` reads the key from the environment, with its
  password in `COSIGN_PASSWORD`; keep both in the CI secret store.
- To publish **through an instance** instead, `POST /api/v1/recipes/publish`
  with `{"reference": "<registry>/<cookbook>/<name>:<version>", "document": "<the YAML>"}`
  (operator role) answers `200` with `{"reference", "digest", "unchanged"}`;
  the push uses the instance's registry credentials. Signing is still yours.

Writing and linting recipes is [write, publish and sign](../../recipes/write-and-publish/).

## Environment variables for a runner

| Variable | Role |
| --- | --- |
| `TOBBY_INSTANCE_URL` | Base URL of the instance to drive, e.g. `https://tobby.example:8443`. |
| `TOBBY_API_TOKEN` | The token. There is deliberately no flag for it: a flag value is visible in the process table. `--token-file <path>` reads it from a file instead (takes precedence). |
| `TOBBY_REGISTRIES_INSECURE`, `TOBBY_*` | Every configuration key has its variable ([configuration reference](../../reference/configuration/)); flags override variables, variables override the file. |

How `TOBBY_INSTANCE_URL` is read:

- **`tobby sync`** without `--dry-run` always drives an instance; the URL is
  `--instance`, else `TOBBY_INSTANCE_URL`, else the configured
  `server.addr` on localhost.
- **`tobby store verify`, `tobby diagnostics bundle` and `tobby scan
  exceptions`** have a local form and a remote one. `--instance` *or*
  `TOBBY_INSTANCE_URL` alone selects the remote form; with neither they
  work on the local directory or configuration. Setting the variable on a
  runner is therefore enough: no command needs the flag repeated. (Replayed:
  `tobby store verify --wait --output json` with only the two variables
  set ran on the instance and wrote the report.)
- **`tobby sync --dry-run`** ignores the variable and plans locally, as
  above.
- A token sent over plain `http://` leaves the machine in clear. The
  commands refuse it for any host that is not this one, with exit `2`;
  `--allow-plaintext-token` accepts the exposure and says so on every run.
  Use `https://`.
- A wrong or revoked token is [`TBY-AUTH-002`](../../reference/errors/#tby-auth-002),
  an expired one [`TBY-AUTH-013`](../../reference/errors/#tby-auth-013), a
  role that is too low [`TBY-AUTH-003`](../../reference/errors/#tby-auth-003).
  Replayed: the first exits `1` on the CLI, the last exits `3`.

## Machine-readable output

- `--output json` is accepted by every command that reports something, and
  puts **one document alone on standard output**. Narration, logs and
  progress go to standard error, so `… --output json | jq` composes. An
  unknown format is a usage error, exit `2`.
- The documents are described by `GET /api/v1/cli-output.schema.json`, one
  entry per command (`tobby sync`, `tobby sync --dry-run`,
  `tobby recipe push`, `tobby store verify`, …). Adding a member is
  additive; removing or retyping one is a breaking change, the same promise
  as the exit codes.
- API payloads carry raw values and RFC 3339 timestamps, never translated
  sentences.

## Problem documents

Every API error is an RFC 9457 problem document, `Content-Type:
application/problem+json`, with the standard members `type`, `title` and
`status` and four from the Tobby taxonomy: `code`, `probable_cause`,
`action` and, when there is one, `correlation_id`.

```sh
curl -sS -X POST "$TOBBY_INSTANCE_URL/api/v1/sync" \
  -H "Authorization: Bearer <a viewer token>"
```

```http
HTTP/1.1 403 Forbidden
Content-Type: application/problem+json
```

```json
{"type":"/help#TBY-AUTH-003","title":"This action is not allowed for your role.","status":403,"code":"TBY-AUTH-003","probable_cause":"It requires the operator role.","action":"Sign in with an account holding the operator role, or ask an administrator to grant it."}
```

A script reads the **`code`**: it is stable, documented in the
[error reference](../../reference/errors/), and the same string the UI and
the CLI show. The `title`, `probable_cause` and `action` follow the
`Accept-Language` header and are for humans. `type` is an in-instance link
(`/help#<code>`), readable offline on the instance itself.

```sh
status=$(curl -sS -o body.json -w '%{http_code}' -H "Authorization: Bearer $TOBBY_API_TOKEN" \
  "$TOBBY_INSTANCE_URL/api/v1/tasks/$ID")
case $status in
  2??) ;;                                              # a task document
  *)   echo "$(jq -r '.code + ": " + .title' body.json)" >&2; exit 1 ;;
esac
```

Codes a pipeline meets most often:

| HTTP | Code | When |
| --- | --- | --- |
| `401` | [`TBY-AUTH-002`](../../reference/errors/#tby-auth-002) | Unknown or revoked token, wrong password. |
| `401` | [`TBY-AUTH-013`](../../reference/errors/#tby-auth-013) | Expired token. |
| `403` | [`TBY-AUTH-003`](../../reference/errors/#tby-auth-003) | Role too low for the endpoint. |
| `404` | [`TBY-TSK-001`](../../reference/errors/#tby-tsk-001) | No task with that identifier. |
| `422` | [`TBY-VAL-001`](../../reference/errors/#tby-val-001) | Malformed body or invalid field; `probable_cause` names which. |
| `429` | [`TBY-AUTH-012`](../../reference/errors/#tby-auth-012) | An origin that keeps failing authentication is throttled. |

A failure of the instance itself is a `5xx` with its own code; retry only
those, never a `4xx`.

## A complete job

Three scripts cover most pipelines. They need `curl` and `jq`, plus `tobby`
(and `cosign` for the last script), and read `TOBBY_INSTANCE_URL` and `TOBBY_API_TOKEN` from
the CI secret store. All three were run in the replay; the exit status of each run is
in parentheses after its output.

### Before merging a Retriever change

```sh
#!/bin/sh
# plan-gate.sh — plan a candidate Retriever on the instance, without applying it.
set -eu

candidate=${1:-retriever.yaml}
out=$(mktemp)
trap 'rm -f "$out"' EXIT

status=$(jq -Rs '{document: ., skip_destination: true}' "$candidate" |
  curl -sS -o "$out" -w '%{http_code}' -X POST "$TOBBY_INSTANCE_URL/api/v1/plan" \
    -H "Authorization: Bearer $TOBBY_API_TOKEN" \
    -H 'Content-Type: application/json' --data-binary @-)

if [ "$status" != 200 ]; then
  # Not a plan: a problem document (bad token, wrong role, instance fault).
  echo "plan refused: $(jq -r '.code + " " + .title' "$out")" >&2
  exit 1
fi

echo "outcome: $(jq -r '.plan.outcome' "$out")"
jq -r '.plan.problems[]? | "  " + .code + "  " + .subject' "$out" >&2 || true

case $(jq -r '.exit_code' "$out") in
  0) echo "nothing to do" ;;
  5) echo "changes planned: needs a review" ;;
  3) echo "refused by policy" >&2; exit 3 ;;
  4) echo "verification failed" >&2; exit 4 ;;
  *) echo "the plan could not complete" >&2; exit 1 ;;
esac
```

On a zone with work to do, then on one that cannot verify the recipe, then
on a candidate naming a recipe the cookbook does not hold:

```text
outcome: changes-planned
changes planned: needs a review          (exit 0)

outcome: verification-failed
  TBY-SIG-001  alpine
verification failed                      (exit 4)

outcome: failed
  TBY-REG-005  ghost
the plan could not complete              (exit 1)
```

The job ends `0` for `up-to-date` and for `changes-planned`: decide in your
CI whether the latter should hold the merge for an approval.

### After merging: synchronize and wait

```sh
#!/bin/sh
# sync-wait.sh — trigger a synchronization and wait. Exits with the task's outcome.
set -u

report=$(mktemp)
trap 'rm -f "$report"' EXIT

tobby sync --wait --wait-timeout 30m --output json >"$report"
rc=$?

jq -r '"task \(.task.id): \(.task.status)"' "$report"
jq -r '.task.items[]? | select(.error) | "  \(.name): \(.error.code)"' "$report"
exit "$rc"
```

```text
task tsk_1490ad8a7e58 created on http://127.0.0.1:8094, waiting for it to finish
task tsk_1490ad8a7e58: done                                         (exit 0)

task tsk_80c840c58b04 created on http://127.0.0.1:8093, waiting for it to finish
TBY-SIG-001 — The recipe signature could not be verified.   …
task tsk_80c840c58b04: failed
  alpine@3.22.1: TBY-SIG-001                                        (exit 4)
```

The first line of each pair, and the error block, are on standard error;
the job keeps `rc` because `jq` would otherwise replace the exit status.

### Publishing a recipe

```sh
#!/bin/sh
# publish-recipe.sh <file> <registry>/<cookbook>/<name>:<version>
# COSIGN_PRIVATE_KEY and COSIGN_PASSWORD come from the CI secret store.
set -eu

file=$1
ref=$2

digest=$(tobby recipe push "$file" "$ref" --output json | jq -r '.digest')

cosign sign --key env://COSIGN_PRIVATE_KEY --yes \
  --use-signing-config=false --tlog-upload=false \
  "${ref%:*}@$digest"
echo "published and signed ${ref} (${digest})"
```

Run twice on the same document, it prints the same line twice and exits
`0` both times; `cosign verify --key cosign.pub` on the tag then succeeds.

### In GitHub Actions

The steps are the same shell. This job assumes `tobby`, `jq` and `cosign`
are already on the runner, and keeps the secrets in the repository's
secret store. Pin whatever installs them as your own policy requires.
Its syntax was not run.

```yaml
jobs:
  sync:
    runs-on: self-hosted
    env:
      TOBBY_INSTANCE_URL: ${{ vars.TOBBY_INSTANCE_URL }}
      TOBBY_API_TOKEN: ${{ secrets.TOBBY_API_TOKEN }}
    steps:
      - uses: actions/checkout@<pinned commit>
      - run: ./plan-gate.sh retriever.yaml
      - run: ./sync-wait.sh
```

A runner that must reach the instance needs a flow to it: see
[Enterprise network](../network/) for proxies and private authorities.

## Where next

- [Add a zone to a cascade](../add-a-zone/): the procedure these
  scripts usually serve.
- [API reference](../../reference/api/) and
  [CLI reference](../../reference/cli/): the full contracts.
- [Operate over time](../operate/): what to alert on once the pipeline runs
  unattended.
