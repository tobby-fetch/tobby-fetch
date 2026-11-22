---
title: Automatiser avec des scripts et la CI
description: Piloter une instance depuis un script ou un pipeline — jetons d'API, déclencher une synchronisation et suivre la tâche, la barrière du plan et ses codes de sortie, publier une recipe, et lire les erreurs sans parser de prose.
sidebar:
  order: 15
---

Tout ce qu'un opérateur fait dans l'interface a son miroir dans l'API, et
les commandes qui comptent pour un pipeline en sont de minces clientes.
Cette page est le jeu de travail d'un script ou d'un job de CI : quel
identifiant créer, quels appels faire, sur quels nombres brancher, et un
petit job qui assemble le tout. Les pages de référence énoncent le contrat
([API](../../reference/api/), [CLI](../../reference/cli/)) ; celle-ci le
montre en fonctionnement.

:::tip[En bref]
- Créez un jeton d'API de rôle **operator** sur `/admin/accounts` (ou par
  `POST /api/v1/tokens`) et envoyez-le en `Authorization: Bearer <secret>`.
  Le secret n'est montré qu'une fois.
- `tobby sync --wait` déclenche une synchronisation, suit la tâche et
  **sort sur son résultat** ; `--output json` laisse un rapport seul sur la
  sortie standard.
- `tobby sync --dry-run` est la barrière du plan : code `0` rien à faire,
  `5` des changements planifiés, `3` refus de politique, `4` échec de
  vérification. `POST /api/v1/plan` en est le jumeau API, pour un runner qui
  n'a pas de store.
- `TOBBY_INSTANCE_URL` et `TOBBY_API_TOKEN` suffisent à un runner pour
  piloter une instance distante.
- Toute erreur d'API est un document de problème : branchez sur son `code`,
  jamais sur son texte.
:::

:::note[À propos des sorties de cette page]
Elles viennent d'un rejeu : des instances sur la boucle locale avec des
registres jetables, lisant des recipes signées dans un cookbook local.
Hôtes, identifiants de tâche et digests sont ceux du rejeu. Les jetons des
exemples sont des paramètres fictifs.
:::

## Créer un jeton d'API

Les jetons relèvent d'une action d'**administrateur**, et il n'y a
volontairement pas de commande `tobby token` : un jeton se crée sur une
instance en marche, depuis l'interface ou l'API.

- **Interface :** l'écran des comptes, `/admin/accounts`. Le formulaire
  demande un nom, un rôle et une durée de vie optionnelle en jours ; le
  secret s'affiche une seule fois, sur la page qui répond.
- **API :** `POST /api/v1/tokens`, authentifié en administrateur (Basic
  `compte:mot de passe`, ou un jeton admin) :

```sh
curl -sS -u admin -X POST "$TOBBY_INSTANCE_URL/api/v1/tokens" \
  -H 'Content-Type: application/json' \
  -d '{"name":"ci-sync","role":"operator","expires":"2026-12-31T00:00:00Z"}'
```

```json
{"expires":"2026-12-31T00:00:00Z","name":"ci-sync","role":"operator","secret":"tby_<le secret, montré une seule fois>"}
```

| Membre | Sens |
| --- | --- |
| `name` | Unique sur l'instance. Visible dans les listes et dans la piste d'audit comme acteur de tout ce que fait le jeton. |
| `role` | `viewer`, `operator` ou `admin`. Prenez le plus bas qui convient (ci-dessous). |
| `expires` | Optionnel, RFC 3339. Absent : `auth.tokens.defaultLifetime` de l'instance s'applique s'il est défini, sinon le jeton n'expire pas. |
| `secret` | L'identifiant. Rendu **une seule fois** ; seul son hachage est stocké. Mettez-le tout de suite dans le coffre de secrets de la CI. |

Un jeton porte un rôle pour toute sa vie :

| Le job fait | Rôle |
| --- | --- |
| Lire les tâches, les recipes, le contenu, le document OpenAPI | `viewer` |
| Déclencher une synchronisation, planifier, publier une recipe, vérifier le store | `operator` |
| Créer des jetons ou des comptes, lire la source du Retriever et la politique effective, collecter un bundle de diagnostic | `admin` |

Créez un jeton par pipeline, pas un par équipe : la liste
(`GET /api/v1/tokens`, admin) montre `name`, `role`, `created`, `revoked`
et `expires` — jamais le secret — et une révocation coupe alors exactement
un pipeline.

```sh
curl -sS -u admin "$TOBBY_INSTANCE_URL/api/v1/tokens"
```

```json
{"tokens":[{"name":"ci-sync","role":"operator","created":"2026-11-22T17:32:17.393144Z","revoked":false,"expires":"2026-12-31T00:00:00Z"}]}
```

Renouvelez par recouvrement : créez le remplaçant, basculez les clients,
puis révoquez l'ancien avec `POST /api/v1/tokens/{name}/revoke` (immédiat et
définitif). Un jeton expiré est refusé avec son propre code,
[`TBY-AUTH-013`](../../reference/errors/#tby-auth-013). Les rôles et le
reste du modèle de comptes sont dans
[Authentification, comptes et RBAC](../../security/auth-rbac/).

## Appeler l'API

Le jeton va dans un en-tête `Bearer`, en HTTPS :

```sh
export TOBBY_INSTANCE_URL=https://tobby.example:8443
export TOBBY_API_TOKEN=<le secret>

curl -sS -H "Authorization: Bearer $TOBBY_API_TOKEN" \
  "$TOBBY_INSTANCE_URL/api/v1/tasks?type=sync&status=done"
```

L'instance sert son propre contrat : `GET /api/v1/openapi.yaml` pour les
endpoints et `GET /api/v1/cli-output.schema.json` pour les documents que
la CLI écrit, tous deux lisibles par un `viewer`. Les listes prennent les
mêmes paramètres que les écrans (`/api/v1/tasks` accepte `q`, `status`,
`type` et `page`) et répondent
`{"tasks":[…],"page":1,"totalPages":1,"total":3}`.

## Déclencher une synchronisation et suivre la tâche

Une synchronisation est une tâche. Créez-la, puis lisez-la jusqu'à ce
qu'elle ne soit plus active.

```sh
curl -sS -X POST "$TOBBY_INSTANCE_URL/api/v1/sync" \
  -H "Authorization: Bearer $TOBBY_API_TOKEN"
```

La réponse est `201` avec la tâche dans une enveloppe :

```json
{"task":{"id":"tsk_aa4e4ea01da6","run_id":"20261122T173230Z-29a9176f","type":"sync","reference":"./a/retriever.yaml","actor":"ci-sync","status":"running","created":"2026-11-22T17:32:30.392505Z","started":"2026-11-22T17:32:30.392756Z","items":null,"items_total":0,"items_done":0,"items_failed":0,"items_skipped":0}}
```

`reference` est la source du Retriever configurée, `actor` le nom du jeton.
Le corps est optionnel : `{"prune": true}` ou `{"prune": false}` décide,
pour ce passage, si le contenu qu'aucune recipe n'atteint plus est supprimé ;
l'omettre prend le réglage propre de l'instance. Un corps qui n'est pas cet
objet répond `422` ([`TBY-VAL-001`](../../reference/errors/#tby-val-001)).
Une instance sans `retriever.source` refuse le déclenchement
([`TBY-CFG-001`](../../reference/errors/#tby-cfg-001)).

Suivez-la avec `GET /api/v1/tasks/{id}` :

```sh
curl -sS -H "Authorization: Bearer $TOBBY_API_TOKEN" \
  "$TOBBY_INSTANCE_URL/api/v1/tasks/tsk_aa4e4ea01da6" | jq -c '.task | {status, items_total, items_done, items_failed, items_skipped}'
```

```json
{"status":"done","items_total":4,"items_done":4,"items_failed":0,"items_skipped":0}
```

| `status` | Sens |
| --- | --- |
| `pending`, `running` | Active : continuez d'interroger. |
| `done` | Terminée. Une tâche peut être `done` avec des éléments `skipped` (déjà à jour). |
| `failed` | Terminée avec au moins un élément en échec. `principal_code` porte le code de celui qui fait le verdict. |

Interrogez toutes les quelques secondes : une synchronisation peut durer des
heures, et une interrogation survit à un redémarrage de l'instance là où une
connexion longue ne le fait pas — une tâche interrompue par un redémarrage
revient et l'interrogation suivante la retrouve. Chaque élément de `items[]`
a un `name`, un `status`, un `digest` et `size_bytes` une fois connus, et,
en cas d'échec, une `error` avec le `code` et ses paramètres :

```sh
curl -sS -H "Authorization: Bearer $TOBBY_API_TOKEN" "$TOBBY_INSTANCE_URL/api/v1/tasks/$ID" \
  | jq -r '.task | .status, (.items[] | select(.error) | "  \(.name): \(.error.code)")'
```

Le journal de la tâche est `GET /api/v1/tasks/{id}/logs?after=<décalage>`,
qui répond `{"chunk":"<lignes JSON>","next":<décalage>}` — rappelez avec
`after=<next>` pour lire la suite — et `?format=raw` le télécharge en entier.

### La CLI fait l'interrogation à votre place

`tobby sync --wait` est cette boucle, avec une échéance et un code de
sortie :

```sh
export TOBBY_INSTANCE_URL=https://tobby.example:8443
export TOBBY_API_TOKEN=<le secret>
tobby sync --wait --wait-timeout 30m --output json
```

Elle crée la tâche, interroge toutes les deux secondes, affiche le rapport
une fois la tâche terminée, et **sort sur le résultat propre de la tâche**,
traduit par la [table des codes de sortie](../../reference/cli/#codes-de-sortie) :
une tâche en échec sort avec la classe de son erreur principale, de sorte
qu'un refus de politique sur l'instance est `3` sur le runner, une
signature qu'aucune racine de confiance ne valide est `4`. Un échec d'une
autre nature est `1`.

- Le récit (`task … created on …, waiting for it to finish`) va sur la
  sortie d'erreur. Avec `--output json`, la sortie standard porte exactement
  un document, à trois membres : `instance` (l'URL de base pilotée),
  `waited` (`true` sous `--wait`) et `task` : la tâche telle que l'instance
  la stocke (`id`, `run_id`, `type`, `reference`, `actor`, `status`, les
  horodatages et `items[]`). Les compteurs `items_total` et consorts, et
  `principal_code`, sont ajoutés par l'API seule : depuis la CLI, lisez
  `task.status` et `task.items[].error.code`.
- `--wait-timeout` borne l'attente, pas le travail. À l'expiration, la
  commande dit quelle tâche tourne encore et où, et sort `1` ; la
  synchronisation continue sur l'instance.
- `--prune` et `--prune=false` correspondent au corps ci-dessus.
- Sans `--wait`, la commande rend la main dès que la tâche est créée.

Un passage en échec, rejoué contre une instance qui n'avait pas la clé de
signature (le rapport texte ; avec `--output json` les mêmes champs sont
dans le document) :

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

Le second bloc est sur la sortie d'erreur, le code de sortie du processus
est `4`.

## La barrière du plan

Un plan dit ce qu'une synchronisation ferait sans rien faire. C'est l'étape
qu'un pipeline place devant un changement de Retriever.

```sh
tobby sync --dry-run --retriever ./retriever.yaml --output json
```

`--retriever` accepte un fichier, une URL HTTP(S) ou une référence OCI : le
candidat est planifié à la place du document configuré. Le code de sortie
est le verdict :

| Sortie | `outcome` dans le rapport | Sens pour une barrière |
| --- | --- | --- |
| `0` | `up-to-date` | Rien à faire. |
| `5` | `changes-planned` | Du travail est planifié : ingrédients nouveaux ou périmés, ou un prune. Un succès qui mérite un coup d'œil, pas un build cassé. |
| `3` | `policy-refused` | Un registre est hors de `registries.allowlist` ([`TBY-POL-001`](../../reference/errors/#tby-pol-001)). |
| `4` | `verification-failed` | Une signature de recipe qu'aucune racine de confiance ne valide, ou un contenu qui ne correspond pas à son digest épinglé ([`TBY-SIG-001`](../../reference/errors/#tby-sig-001)). |
| `1` | `failed` | Le plan n'a pas pu se terminer : un registre qui ne répond pas, une recipe ou une version inexistante, un store illisible. |
| `2` | — | Erreur d'usage : par exemple `--wait` ou `--instance` donné avec `--dry-run`. |

Rejoué sur une zone dont la clé de signature n'était pas configurée :

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

Le processus est sorti `4`. La forme JSON (`--output json`) a ces membres :
`mode`, `source`, `source_is_candidate`, `zone`, `outcome`, `totals`,
`recipes`, `policy`, `prune`, `destination`, `checks`, `generated_at`, et le
schéma est dans `GET /api/v1/cli-output.schema.json`. Elle porte le verdict
dans `outcome`, et **aucun membre `exit_code`** : lisez le statut de sortie
du processus, ou `outcome`.

**L'endroit où le plan tourne compte.** `tobby sync --dry-run` planifie sur
l'hôte qui détient la configuration et le répertoire de store de
l'instance ; il ne contacte aucune instance, c'est pourquoi `--instance` et
`--wait` sont refusés avec lui, et pourquoi `TOBBY_INSTANCE_URL` n'a aucun
effet sur lui. Un runner de CI qui n'a ni l'un ni l'autre utilise l'API :

```sh
jq -Rs '{document: ., skip_destination: true}' retriever.yaml |
  curl -sS -X POST "$TOBBY_INSTANCE_URL/api/v1/plan" \
    -H "Authorization: Bearer $TOBBY_API_TOKEN" \
    -H 'Content-Type: application/json' --data-binary @-
```

`POST /api/v1/plan` (rôle operator) prend trois membres optionnels :
`document` (un Retriever candidat **en ligne**, en texte YAML, de sorte
qu'un fichier qui n'existe que dans la merge request peut être planifié),
`retriever` (une source que *l'instance* peut lire : un fichier sur son
hôte, une URL, une référence OCI) et `skip_destination`. Un corps vide
planifie le Retriever configuré. Il répond **`200` quel que soit le
résultat** — un plan est un rapport — avec `{"plan": …, "exit_code": …}` :
branchez sur `exit_code`, le nombre avec lequel la CLI serait sortie, jamais
sur le statut HTTP. Les problèmes sont dans `plan.problems[]`, chacun avec
un `code`, une `class` et le `subject` qu'il concerne. Planification d'un
candidat qui nomme une recipe que le cookbook ne détient pas, réduite avec
`jq -c '{exit_code, outcome: .plan.outcome, problems: .plan.problems}'` :

```json
{"exit_code":1,"outcome":"failed","problems":[{"code":"TBY-REG-005","class":"operational","subject":"ghost","params":{"reference":"ghost"}}]}
```

Le plan utilise la sortie réseau propre de l'instance : il ouvre des
connexions vers chaque registre que nomme le candidat, c'est pourquoi le
rôle est `operator` et non `viewer`.

## Publier une recipe depuis la CI

La publication se fait en deux temps, volontairement : Tobby vérifie et
publie, une clé qui est à vous signe.

```sh
digest=$(tobby recipe push alpine.yaml registry.example.com/cookbook/alpine:3.22.0 \
  --output json | jq -r '.digest')
cosign sign --key env://COSIGN_PRIVATE_KEY --yes \
  --use-signing-config=false --tlog-upload=false \
  "registry.example.com/cookbook/alpine@$digest"
```

`tobby recipe push` refuse un document qui n'est pas une recipe valide et
entièrement épinglée, ou dont le nom et la version contredisent la
référence, et n'écrase jamais une version publiée. Son rapport, avec
`--output json` :

```json
{
  "reference": "127.0.0.1:5011/cookbook/alpine:3.22.0",
  "repository": "127.0.0.1:5011/cookbook/alpine",
  "digest": "sha256:8fbf02f6…",
  "unchanged": true
}
```

- `unchanged: true` signifie que le même document est déjà publié : un
  rejeu est sans effet. Signez quand même — un job précédent a pu publier
  puis s'arrêter avant de signer, et signer deux fois un digest est
  inoffensif.
- Un document différent sous une version publiée sort `3` avec
  [`TBY-POL-004`](../../reference/errors/#tby-pol-004) : publiez sous une
  nouvelle `metadata.version`.
- Les identifiants du registre viennent de la configuration
  (`registries.credentialsFile`), comme tout appel sortant, avec les
  variables d'environnement `TOBBY_*` habituelles par-dessus ; la commande
  n'a besoin ni de mode ni de store. Sans entrée correspondante, le refus
  du registre est [`TBY-REG-003`](../../reference/errors/#tby-reg-003),
  sortie `1`.
- `cosign` reste en dehors de Tobby, qui ne détient jamais de clé privée.
  Les deux options gardent la signature vérifiable hors ligne et sont
  expliquées dans
  [Votre première promotion](../../try/first-promotion/#4-la-publier-et-la-signer).
  `env://COSIGN_PRIVATE_KEY` lit la clé dans l'environnement, avec son mot
  de passe dans `COSIGN_PASSWORD` ; gardez les deux dans le coffre de
  secrets de la CI.
- Pour publier **à travers une instance**, `POST /api/v1/recipes/publish`
  avec `{"reference": "<registre>/<cookbook>/<nom>:<version>", "document": "<le YAML>"}`
  (rôle operator) répond `200` avec `{"reference", "digest", "unchanged"}` ;
  le push utilise les identifiants de registre de l'instance. La signature
  reste de votre ressort.

Écrire et linter des recipes, c'est
[écrire, publier et signer](../../recipes/write-and-publish/).

## Variables d'environnement d'un runner

| Variable | Rôle |
| --- | --- |
| `TOBBY_INSTANCE_URL` | URL de base de l'instance à piloter, p. ex. `https://tobby.example:8443`. |
| `TOBBY_API_TOKEN` | Le jeton. Il n'y a volontairement pas d'option pour lui : la valeur d'une option est visible dans la table des processus. `--token-file <chemin>` le lit depuis un fichier à la place (prioritaire). |
| `TOBBY_REGISTRIES_INSECURE`, `TOBBY_*` | Chaque clé de configuration a sa variable ([référence de configuration](../../reference/configuration/)) ; les options l'emportent sur les variables, qui l'emportent sur le fichier. |

Comment `TOBBY_INSTANCE_URL` est lue :

- **`tobby sync`** sans `--dry-run` pilote toujours une instance ; l'URL est
  `--instance`, sinon `TOBBY_INSTANCE_URL`, sinon le `server.addr`
  configuré sur localhost.
- **`tobby store verify`, `tobby diagnostics bundle` et `tobby scan
  exceptions`** ont une forme locale et une forme distante. `--instance`
  *ou* `TOBBY_INSTANCE_URL` seule sélectionne la forme distante ; sans
  aucune des deux, elles travaillent sur le répertoire ou la configuration
  locale. Poser la variable sur un runner suffit donc : aucune commande
  n'a besoin que l'option soit répétée. (Rejoué : `tobby store verify --wait
  --output json` avec les deux seules variables posées s'est exécutée sur
  l'instance et a écrit le rapport.)
- **`tobby sync --dry-run`** ignore la variable et planifie en local,
  comme ci-dessus.
- Un jeton envoyé en `http://` clair quitte la machine en clair. Les
  commandes le refusent pour tout hôte qui n'est pas celui-ci, avec la
  sortie `2` ; `--allow-plaintext-token` accepte l'exposition et le dit à
  chaque exécution. Utilisez `https://`.
- Un jeton erroné ou révoqué est
  [`TBY-AUTH-002`](../../reference/errors/#tby-auth-002), un jeton expiré
  [`TBY-AUTH-013`](../../reference/errors/#tby-auth-013), un rôle trop bas
  [`TBY-AUTH-003`](../../reference/errors/#tby-auth-003). Rejoué : le
  premier sort `1` côté CLI, le dernier sort `3`.

## Sortie lisible par machine

- `--output json` est accepté par toute commande qui rapporte quelque
  chose, et met **un seul document sur la sortie standard**. Récit,
  journaux et progression vont sur la sortie d'erreur, si bien que
  `… --output json | jq` se compose. Un format inconnu est une erreur
  d'usage, sortie `2`.
- Les documents sont décrits par `GET /api/v1/cli-output.schema.json`, une
  entrée par commande (`tobby sync`, `tobby sync --dry-run`,
  `tobby recipe push`, `tobby store verify`, …). Ajouter un membre est
  additif ; en retirer un ou en changer le type est un changement
  incompatible, avec la même promesse que les codes de sortie.
- Les charges utiles de l'API portent des valeurs brutes et des horodatages
  RFC 3339, jamais de phrases traduites.

## Documents de problème

Toute erreur d'API est un document de problème RFC 9457,
`Content-Type: application/problem+json`, avec les membres standard `type`,
`title` et `status` et quatre de la taxonomie Tobby : `code`,
`probable_cause`, `action` et, quand il y en a un, `correlation_id`.

```sh
curl -sS -X POST "$TOBBY_INSTANCE_URL/api/v1/sync" \
  -H "Authorization: Bearer <un jeton viewer>"
```

```http
HTTP/1.1 403 Forbidden
Content-Type: application/problem+json
```

```json
{"type":"/help#TBY-AUTH-003","title":"This action is not allowed for your role.","status":403,"code":"TBY-AUTH-003","probable_cause":"It requires the operator role.","action":"Sign in with an account holding the operator role, or ask an administrator to grant it."}
```

Un script lit le **`code`** : il est stable, documenté dans la
[référence des erreurs](../../reference/errors/), et c'est la même chaîne
que montrent l'interface et la CLI. `title`, `probable_cause` et `action`
suivent l'en-tête `Accept-Language` et s'adressent aux humains. `type` est
un lien interne à l'instance (`/help#<code>`), lisible hors ligne sur
l'instance elle-même.

```sh
status=$(curl -sS -o body.json -w '%{http_code}' -H "Authorization: Bearer $TOBBY_API_TOKEN" \
  "$TOBBY_INSTANCE_URL/api/v1/tasks/$ID")
case $status in
  2??) ;;                                              # un document de tâche
  *)   echo "$(jq -r '.code + ": " + .title' body.json)" >&2; exit 1 ;;
esac
```

Les codes qu'un pipeline rencontre le plus souvent :

| HTTP | Code | Quand |
| --- | --- | --- |
| `401` | [`TBY-AUTH-002`](../../reference/errors/#tby-auth-002) | Jeton inconnu ou révoqué, mauvais mot de passe. |
| `401` | [`TBY-AUTH-013`](../../reference/errors/#tby-auth-013) | Jeton expiré. |
| `403` | [`TBY-AUTH-003`](../../reference/errors/#tby-auth-003) | Rôle trop bas pour l'endpoint. |
| `404` | [`TBY-TSK-001`](../../reference/errors/#tby-tsk-001) | Aucune tâche avec cet identifiant. |
| `422` | [`TBY-VAL-001`](../../reference/errors/#tby-val-001) | Corps mal formé ou champ invalide ; `probable_cause` dit lequel. |
| `429` | [`TBY-AUTH-012`](../../reference/errors/#tby-auth-012) | Une origine qui échoue sans cesse à s'authentifier est freinée. |

Une défaillance de l'instance elle-même est un `5xx` avec son propre code ;
ne rejouez que ceux-là, jamais un `4xx`.

## Un job complet

Trois scripts couvrent la plupart des pipelines. Ils demandent `curl` et
`jq`, plus `tobby` (et `cosign` pour le dernier script), et lisent
`TOBBY_INSTANCE_URL` et `TOBBY_API_TOKEN` dans le coffre de secrets de la
CI. Les trois ont été exécutés dans le rejeu ; le statut de sortie de
chaque exécution est entre parenthèses après sa sortie.

### Avant de fusionner un changement de Retriever

```sh
#!/bin/sh
# plan-gate.sh — planifie un Retriever candidat sur l'instance, sans l'appliquer.
set -eu

candidate=${1:-retriever.yaml}
out=$(mktemp)
trap 'rm -f "$out"' EXIT

status=$(jq -Rs '{document: ., skip_destination: true}' "$candidate" |
  curl -sS -o "$out" -w '%{http_code}' -X POST "$TOBBY_INSTANCE_URL/api/v1/plan" \
    -H "Authorization: Bearer $TOBBY_API_TOKEN" \
    -H 'Content-Type: application/json' --data-binary @-)

if [ "$status" != 200 ]; then
  # Pas un plan : un document de problème (mauvais jeton, mauvais rôle, panne).
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

Sur une zone qui a du travail à faire, puis sur une qui ne peut pas
vérifier la recipe, puis sur un candidat qui nomme une recipe que le
cookbook ne détient pas :

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

Le job se termine `0` pour `up-to-date` et pour `changes-planned` : à vous
de décider, dans votre CI, si ce dernier doit retenir la fusion pour une
approbation.

### Après la fusion : synchroniser et attendre

```sh
#!/bin/sh
# sync-wait.sh — déclenche une synchronisation et attend. Sort avec le résultat de la tâche.
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

La première ligne de chaque paire, et le bloc d'erreur, sont sur la sortie
d'erreur ; le job garde `rc` parce que `jq` remplacerait sinon le statut de
sortie.

### Publier une recipe

```sh
#!/bin/sh
# publish-recipe.sh <fichier> <registre>/<cookbook>/<nom>:<version>
# COSIGN_PRIVATE_KEY et COSIGN_PASSWORD viennent du coffre de secrets de la CI.
set -eu

file=$1
ref=$2

digest=$(tobby recipe push "$file" "$ref" --output json | jq -r '.digest')

cosign sign --key env://COSIGN_PRIVATE_KEY --yes \
  --use-signing-config=false --tlog-upload=false \
  "${ref%:*}@$digest"
echo "published and signed ${ref} (${digest})"
```

Lancé deux fois sur le même document, il affiche deux fois la même ligne et
sort `0` les deux fois ; `cosign verify --key cosign.pub` sur le tag réussit
ensuite.

### Dans GitHub Actions

Les étapes sont le même shell. Ce job suppose que `tobby`, `jq` et `cosign`
sont déjà sur le runner, et garde les secrets dans le coffre du dépôt.
Épinglez ce qui les installe comme votre politique l'exige. Sa syntaxe n'a
pas été exécutée.

```yaml
jobs:
  sync:
    runs-on: self-hosted
    env:
      TOBBY_INSTANCE_URL: ${{ vars.TOBBY_INSTANCE_URL }}
      TOBBY_API_TOKEN: ${{ secrets.TOBBY_API_TOKEN }}
    steps:
      - uses: actions/checkout@<commit épinglé>
      - run: ./plan-gate.sh retriever.yaml
      - run: ./sync-wait.sh
```

Un runner qui doit joindre l'instance a besoin d'un flux vers elle : voir
[Réseau d'entreprise](../network/) pour les proxys et les autorités
privées.

## Et maintenant

- [Ajouter une zone à une cascade](../add-a-zone/) : la procédure que ces
  scripts servent d'ordinaire.
- [Référence de l'API](../../reference/api/) et
  [référence de la CLI](../../reference/cli/) : les contrats complets.
- [Exploiter dans la durée](../operate/) : sur quoi alerter une fois le
  pipeline lancé sans surveillance.
