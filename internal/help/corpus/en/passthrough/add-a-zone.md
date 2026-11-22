---
title: Add a zone to a cascade
description: Chain one more zone under an existing one — its instance, its Retriever (published as an OCI artifact if you wish), its trust roots, the first synchronization, and what changes — nothing — in the zone above.
sidebar:
  order: 14
---

A cascade grows one zone at a time, and the new zone is always the same
shape: one instance, one destination registry, one Retriever, and a
single network flow towards the zone above it. This page is the
procedure. The reasoning behind substitution and relocated paths is on
[Retriever and cascade](../retriever-cascade/); read it first if
`registries.substitutions` is new to you.

:::tip[In short]
- The new zone only **reads** from the zone above, through that zone's
  registry. Nothing is configured on the zone above — it does not know
  its reader exists.
- The Retriever of the new zone names the cookbook of the zone above
  (`<registry>/cookbook`), and `registries.substitutions` points the
  origin hosts at the same registry.
- A Retriever can be published as an OCI artifact: one YAML layer, any
  tag. `tobby recipe push` publishes recipes only; use `oras`.
- Trust roots are the new instance's own. Give it the public key that
  signs the recipes — the signatures travel with the recipes.
- Run `tobby sync --dry-run` before the first synchronization, then
  check the result from the API and from the zone registry.
- The Retriever document is **not signed**: protect where it lives like
  the configuration file.
:::

:::note[About the outputs on this page]
Every output below comes from a replay: two instances chained on one
machine, with throwaway registries on loopback. `127.0.0.1:5012` plays
the registry of the zone above (`registry.zone-a.example` in the
configuration snippets) and `127.0.0.1:5013` the registry of the new zone.
The new zone is called `zone-b`. Plain HTTP and the `registries.insecure`
opt-in belong to that replay only; a real zone serves TLS.
:::

## What the new zone needs

| Need | Detail |
| --- | --- |
| An instance | Its own Tobby in passthrough mode — [deploy it](../deploy/) like any other, with its own store and state directories. |
| A destination registry | The registry the zone's clients will pull from: `destination.registry`. Tobby promotes into it. |
| One network flow | From the instance to the registry of the zone above, and to its own destination registry. Nothing else: no flow to the zone above's instance, none to the origin registries. Proxy and private authorities are [Enterprise network](../network/). |
| Read access | An account on the registry of the zone above, with read as its only need, handed over through `registries.credentialsFile`. |
| A trust root | The public key (or keys) that signed the recipes, in the new instance's `trust.roots`. |
| A Retriever | The document listing what this zone holds, published where the instance can read it. |

## 1. How the zone above is addressed

The zone above promotes into its destination registry. What it leaves
there is what the new zone reads, and it is laid out the same way
whatever the content:

| Content | Where it sits in the registry of the zone above |
| --- | --- |
| A recipe | `<registry>[/<destination.basePath>]/<destination.cookbook>/<name>:<version>`; the cookbook is `cookbook` unless the zone above changed it. |
| An ingredient | `<registry>[/<destination.basePath>]/<origin host>/<repository path>`: the origin host stays in the path (`docker.io/library/alpine`). |

The cookbook path is the zone's **own** namespace, not a relocation of
the cookbook it read from: a recipe promoted through three zones sits at
`<registry>/cookbook/<name>` in each of them. Ingredients, on the other
hand, keep the origin host, which is why a recipe never changes between
zones.

You can see exactly this on the registry of the zone above after its
first synchronization:

```text
$ curl -s -u zone-b-reader http://127.0.0.1:5012/v2/_catalog
{"repositories":["cookbook/alpine","docker.io/library/alpine"]}

$ curl -s -u zone-b-reader http://127.0.0.1:5012/v2/cookbook/alpine/tags/list
{"name":"cookbook/alpine","tags":["3.22.0","sha256-8fbf02f6…"]}
```

The second tag is the recipe's signature, stored beside it; it is what
lets the new zone verify the recipe on its own.

Two consequences:

- **The new zone can only list what the zone above holds.** A recipe the
  zone above has not synchronized yet, or never will, resolves to nothing
  (see [what the zone above sees](#what-the-zone-above-sees)).
- **If the zone above set `destination.basePath`**, the new zone's
  cookbook and substitution values carry it: `<registry>/<basePath>/cookbook`
  and `<registry>/<basePath>`. This was written from the code
  (`internal/engine/destination.go`), not replayed.

When the zone above is not a registry fed by Tobby but a pull-through
proxy or the embedded registry of an upstream instance, the substitution
value differs: see
[what the substitute registry must hold](../retriever-cascade/#what-the-substitute-registry-must-hold).

## 2. Write the Retriever of the new zone

```yaml
apiVersion: recipe.tobbyfetch.dev/v1alpha1
kind: Retriever

metadata:
  name: zone-b            # the zone's identity; shown on every plan and recipe

spec:
  cookbook: registry.zone-a.example/cookbook
  recipes:
    - name: alpine
      version: "3.22.x"   # constraints resolve at each synchronization
```

`spec.cookbook` is the registry of the zone above plus its cookbook path:
the instance lists the tags of `<cookbook>/<name>` to resolve each
constraint. The recipes themselves are untouched, and so are their
origin hosts — the substitution, in the next step, takes care of those.

The document format is normative on the
[recipe-spec site](https://tobby-fetch.github.io/recipe-spec/). The
linter that ships with the specification checks Retriever documents as
well as recipes, with no instance involved:

```sh
recipe lint retriever.yaml
```

```text
1 file(s), 1 document(s) checked: all valid
```

## 3. Publish the Retriever

`retriever.source` takes a file, an HTTP(S) URL or an OCI reference
(see [three sources](../retriever-cascade/#three-sources)). The first two
need nothing from this page. A Retriever published as an OCI artifact
follows these rules, all read from `internal/engine/retriever.go`:

| Rule | Detail |
| --- | --- |
| Reference | `<host>/<repository>[:<tag>]`, with or without an `oci://` prefix in `retriever.source`. Without a tag, `latest`. |
| Content | **Exactly one layer**, holding the YAML document, at most 4 MiB. Any other number of layers is refused. |
| Media types | Not checked. Neither the artifact type nor the layer type has to match anything. |
| Mutability | The tag is read at every synchronization: pushing the same tag again changes the zone's desired state at its next cycle. |
| Signature | None. Nothing verifies who pushed it — see [below](#the-retriever-is-not-signed). |

There is no Tobby command that publishes a Retriever.
`tobby recipe push` validates and publishes **recipes**, and refuses
anything else. Use a generic OCI client; with `oras`:

```sh
oras login registry.zone-a.example --username <writer> --password-stdin
oras push registry.zone-a.example/config/zone-b:v1 \
  --artifact-type application/vnd.example.retriever.v1+yaml \
  retriever.yaml:application/yaml
```

The `--artifact-type` and the layer type above are conventions of your
own; pick values your registry accepts. The pushed manifest, as the
replay produced it, has the one layer the instance needs:

```json
{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","artifactType":"application/vnd.example.retriever.v1+yaml","config":{"mediaType":"application/vnd.oci.empty.v1+json", …},"layers":[{"mediaType":"application/yaml","digest":"sha256:1a14b73e…","size":182,"annotations":{"org.opencontainers.image.title":"retriever.yaml"}}]}
```

A second file pushed in the same artifact makes two layers, and the
instance refuses it with
[`TBY-VAL-001`](../../reference/errors/#tby-val-001), naming the
reference and the constraint: exactly one document layer, and how many
this artifact has.

Where to put it is your call. Anywhere the new instance can read, under a
repository that is not a cookbook. The registry of the zone above is
convenient: the flow already exists. A registry the zone's administrators
control is better when the zone above belongs to someone else.

## 4. Configure the new instance

```yaml
mode: passthrough
# storage.root, state.root, server.addr: as for any instance (see Deploy)

retriever:
  source: oci://registry.zone-a.example/config/zone-b:v1

trust:
  roots:
    - name: signing-key
      keyFile: /etc/tobby-trust/cosign.pub

registries:
  credentialsFile: /etc/tobby-credentials/config.json
  substitutions:
    docker.io: registry.zone-a.example
    ghcr.io: registry.zone-a.example
  # allowlist: [registry.zone-a.example, registry.zone-b.example]

destination:
  registry: registry.zone-b.example
```

What each block does here:

- **`retriever.source`**: the document written and published above.
- **`trust.roots`**: the instance verifies every recipe against **its own**
  roots, whatever the zone above already did. Give it the public key that
  signs the recipes you want it to accept, the same key as the zone above
  in the usual case. The recipe's signature is propagated with the recipe,
  so the new zone verifies the very same signed bytes. Scopes and
  relaxations are [content trust](../../security/content-trust/).
- **`registries.substitutions`**: one entry per origin host the recipes
  name. The value is the registry of the zone above, **without** the
  origin host or any path of its own beyond a `basePath`: the instance
  appends `<origin host>/<path>` itself.
- **`registries.credentialsFile`**: a `dockerconfigjson` file, keyed by the
  host actually contacted — the registry of the zone above:

  ```json
  {"auths":{"registry.zone-a.example":{"auth":"<base64 of user:password>"}}}
  ```

  Keep it outside the store. On Kubernetes it is the mounted Secret of
  the chart. The same file also holds the credentials for the
  destination registry if it needs them, under its own host.
- **`registries.allowlist`** (if you use one): it is **this** instance's
  allowlist and it must name the two hosts above: the registry of the
  zone above (the source) and the destination. Leaving the source out is
  a policy refusal (`TBY-POL-001`, exit code `3`); the plan below shows it
  before anything moves.
- **`destination.registry`**: the new zone's registry, a bare host. A
  second Tobby, further down, will read from it exactly as this one reads
  from the zone above, with `destination.cookbook` (default `cookbook`)
  as its cookbook path.

Restart the instance. Create its first administrator and an operator
token for the automation as on any new instance
([the first account](../deploy/#the-first-account-non-interactively),
[automate](../automate/)).

## 5. Plan before the first synchronization

`tobby sync --dry-run` resolves the Retriever, verifies the recipes,
reads the zone above and the destination, and writes nothing:

```sh
tobby sync --dry-run --config /etc/tobby/config.yaml
```

```text
plan of oci://127.0.0.1:5012/config/zone-b:v1 (zone zone-b)
  outcome: changes-planned
…
totals
  recipes:     1
  ingredients: 1 (1 new, 0 outdated, 0 up-to-date)
  to transfer: 28.4 MiB (29761656 bytes, deduplicated by digest, net of the local store)
  store:       0 B → 28.4 MiB
  to promote:  at most 28.4 MiB towards 127.0.0.1:5013

recipes
  NAME    REQUESTED  RESOLVED  SIGNATURE  SCOPE  TRANSFER
  alpine  3.22.x     3.22.0    verified   -      28.4 MiB

registry policy
  allow-list:  not declared (no registry is refused on that ground)
  127.0.0.1:5012                           allowed (source)
  127.0.0.1:5013                           allowed (destination)
```

The command exited `5`: changes are planned, which is what a first
plan should say. Read the three things this plan proves before anything
is transferred: the Retriever was found and parsed, every recipe
resolved to a version, and its signature reads `verified`.

When it does not, the plan names the problem. These are the ones this
procedure produces. Each was replayed except the last, which is worded
from the code:

| You see | Exit | Cause and fix |
| --- | --- | --- |
| `outcome: failed`, [`TBY-REG-003`](../../reference/errors/#tby-reg-003) on the Retriever | `1` | The registry of the zone above refused the credentials, or none matched its host. Fix the entry for that host in the file named by `registries.credentialsFile`. |
| `outcome: verification-failed`, `SIGNATURE refused`, [`TBY-SIG-001`](../../reference/errors/#tby-sig-001) | `4` | No trust root of this instance validates the recipe. Add the signing public key to `trust.roots`. |
| `outcome: policy-refused`, [`TBY-POL-001`](../../reference/errors/#tby-pol-001) | `3` | A registry of the plan is outside `registries.allowlist`. Add the registry of the zone above. |
| `outcome: failed`, [`TBY-REG-005`](../../reference/errors/#tby-reg-005) on one recipe | `1` | The zone above has no such recipe. List only what it holds. |
| `outcome: failed`, [`TBY-REG-006`](../../reference/errors/#tby-reg-006) on one recipe | `1` | The zone above holds the recipe, but no version satisfies the constraint — often because it has not synchronized that version yet. |

The same plan works on a candidate Retriever before you publish it:
`tobby sync --dry-run --retriever oci://registry.zone-a.example/config/zone-b:next`
(a tag you push for the occasion). More on plans in
[operate](../operate/#seeing-it-coming), and for use in a pipeline in
[automate](../automate/#the-plan-gate).

## 6. First synchronization, and how to check it

The instance synchronizes by itself every `sync.interval` (15 minutes
by default). To run the first one now, with an operator token in
`TOBBY_API_TOKEN`:

```sh
export TOBBY_INSTANCE_URL=https://tobby.zone-b.example
tobby sync --wait
```

The first run took about five seconds on the replay's 28 MiB image and
ended `done`, with four items: the ingredient and the recipe fetched
(`alpine@3.22.0/alpine`, `alpine@3.22.0/recipe`), then the same two pushed
to the destination (`push/…`). Run it again and the zone already matches
its desired state:

```text
task tsk_920cebfab6ac created on http://127.0.0.1:8092, waiting for it to finish
task tsk_920cebfab6ac on http://127.0.0.1:8092
  type:      sync
  reference: oci://127.0.0.1:5012/config/zone-b:v1
  status:    done
  duration:  0s
  items:     4 (0 done, 4 skipped, 0 failed)
```

Then check from the outside, on the three surfaces that matter.

**The instance** knows the recipe, its resolved version and the verdict:

```sh
curl -s -H "Authorization: Bearer $TOBBY_API_TOKEN" "$TOBBY_INSTANCE_URL/api/v1/recipes" \
  | jq -c '.recipes[] | {name, version, zone, verified}'
```

```json
{"name":"alpine","version":"3.22.0","zone":"zone-b","verified":true}
```

**The destination registry** holds the same layout as the zone above, under
its own host:

```sh
curl -s http://127.0.0.1:5013/v2/_catalog
```

```json
{"repositories":["cookbook/alpine","docker.io/library/alpine"]}
```

**The signature travelled with the recipe**, so a client of the new zone
can verify it with the same public key, without Tobby:

```sh
cosign verify --key cosign.pub registry.zone-b.example/cookbook/alpine:3.22.0 \
  --private-infrastructure --insecure-ignore-tlog
```

`cosign` ends with `The signatures were verified against the specified
public key`. A `docker pull` of
`registry.zone-b.example/docker.io/library/alpine:3.22` then returns the
digest the recipe pins. How real clients consume the zone is
[connect your clients](../connect-clients/).

## What the zone above sees

Nothing changes there, and nothing has to:

- **No configuration.** The new zone is a reader of a registry. No
  allowlist of the zone above mentions it, and its instance holds no list
  of downstream zones. Its own `registries.allowlist` and
  `trust.roots` concern what *it* reads and *it* trusts.
- **A credential on its registry.** That is the only thing the zone above
  (or whoever runs its registry) provides: an account the new instance can
  read with. Give it read and nothing else. If your registry cannot
  scope an account to read-only, say so in your own runbook; Tobby only
  ever reads from that registry on the new zone's behalf.
- **Latency, by design.** The new zone sees what the zone above holds
  *now*. In the replay, a recipe `alpine 3.22.1` was published and signed
  in the origin cookbook; until the zone above synchronized, the new zone's
  plan kept resolving `3.22.x` to `3.22.0`. After the zone above ran its
  synchronization, the next plan of the new zone resolved `3.22.1`, and
  its own synchronization propagated the recipe to its cookbook. A zone
  never gets ahead of its parent.
- **Prune stays local.** What the new instance removes is decided by its
  own Retriever and its own prune setting ([operate](../operate/#pruning-to-the-retriever)).

Chaining a zone **below** the new one repeats this page with the new
zone in the role of the zone above: its registry, its cookbook path, an
account on it.

## The Retriever is not signed

This is an accepted design position, stated rather than hidden. The
Retriever carries the same trust as the instance's configuration: its
location, `retriever.source`, is set at start and cannot be changed
through the interface or the API, and whoever can write at that location
can change the zone's desired state, and its scan policy with it. The
recipes it lists are still verified against the instance's own trust
roots, one by one, so a tampered Retriever can ask for different
**signed** content; it cannot make an unsigned recipe pass. The
reasoning, and who this protects against, is in the
[threat model](../../security/threat-model/). In practice: grant write
access to the Retriever's repository, file or URL as you would to
`/etc/tobby/config.yaml`.

## Where next

- [Automate](../automate/): drive the new zone from CI, with a token and
  a plan gate.
- [Supervise an instance](../monitoring/): what to watch on a zone that
  depends on another.
- [Upgrade and roll back](../upgrade-rollback/) the instances of a
  cascade.
