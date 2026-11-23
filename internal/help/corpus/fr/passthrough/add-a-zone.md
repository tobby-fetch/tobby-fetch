---
title: Ajouter une zone à une cascade
description: Enchaîner une zone de plus sous une zone existante — son instance, son Retriever (publié au besoin comme artefact OCI), ses racines de confiance, la première synchronisation, et ce qui change — rien — dans la zone amont.
sidebar:
  order: 14
---

Une cascade grandit une zone à la fois, et la nouvelle zone a toujours la
même forme : une instance, un registre de destination, un Retriever, et un
seul flux réseau vers la zone au-dessus. Cette page est la procédure. Le
raisonnement sur la substitution et les chemins relocalisés est sur
[Retriever et cascade](../retriever-cascade/) ; lisez-la d'abord si
`registries.substitutions` est nouveau pour vous.

:::tip[En bref]
- La nouvelle zone ne fait que **lire** la zone amont, à travers le
  registre de celle-ci. Rien n'est configuré dans la zone amont : elle
  ignore qu'elle a un lecteur.
- Le Retriever de la nouvelle zone nomme le cookbook de la zone amont
  (`<registre>/cookbook`), et `registries.substitutions` pointe les hôtes
  d'origine vers ce même registre.
- Un Retriever peut être publié comme artefact OCI : une seule couche
  YAML, n'importe quel tag. `tobby recipe push` ne publie que des
  recipes ; utilisez `oras`.
- Les racines de confiance sont celles de la nouvelle instance. Donnez-lui
  la clé publique qui signe les recipes — les signatures voyagent avec les
  recipes.
- Lancez `tobby sync --dry-run` avant la première synchronisation, puis
  vérifiez le résultat depuis l'API et depuis le registre de zone.
- Le document Retriever n'est **pas signé** : protégez l'endroit où il
  vit comme le fichier de configuration.
:::

:::note[À propos des sorties de cette page]
Toutes les sorties ci-dessous viennent d'un rejeu : deux instances
enchaînées sur une même machine, avec des registres jetables sur la boucle
locale. `127.0.0.1:5012` joue le registre de la zone amont
(`registry.zone-a.example` dans les extraits de configuration) et
`127.0.0.1:5013` celui de la nouvelle zone. La nouvelle zone s'appelle
`zone-b`. Le HTTP en clair et l'exception `registries.insecure`
n'appartiennent qu'à ce rejeu ; une vraie zone sert du TLS.
:::

## Ce dont la nouvelle zone a besoin

| Besoin | Détail |
| --- | --- |
| Une instance | Son propre Tobby en mode passthrough — [déployez-la](../deploy/) comme les autres, avec ses propres répertoires de store et d'état. |
| Un registre de destination | Le registre où tireront les clients de la zone : `destination.registry`. Tobby y promeut. |
| Un flux réseau | De l'instance vers le registre de la zone amont, et vers son propre registre de destination. Rien d'autre : aucun flux vers l'instance de la zone amont, aucun vers les registres d'origine. Proxy et autorités privées : [Réseau d'entreprise](../network/). |
| Un accès en lecture | Un compte sur le registre de la zone amont, avec la lecture pour seul besoin, remis par `registries.credentialsFile`. |
| Une racine de confiance | La clé publique (ou les clés) qui ont signé les recipes, dans `trust.roots` de la nouvelle instance. |
| Un Retriever | Le document qui liste ce que détient cette zone, publié là où l'instance peut le lire. |

## 1. Comment la zone amont est adressée

La zone amont promeut dans son registre de destination. Ce qu'elle y laisse
est ce que lit la nouvelle zone, et c'est rangé de la même façon quel que
soit le contenu :

| Contenu | Où il se trouve dans le registre de la zone amont |
| --- | --- |
| Une recipe | `<registre>[/<destination.basePath>]/<destination.cookbook>/<nom>:<version>` ; le cookbook est `cookbook` sauf si la zone amont l'a changé. |
| Un ingrédient | `<registre>[/<destination.basePath>]/<hôte d'origine>/<chemin du dépôt>` : l'hôte d'origine reste dans le chemin (`docker.io/library/alpine`). |

Le chemin du cookbook est l'espace de noms **propre** de la zone, pas une
relocalisation du cookbook qu'elle a lu : une recipe promue à travers trois
zones se trouve à `<registre>/cookbook/<nom>` dans chacune. Les
ingrédients, eux, gardent l'hôte d'origine, et c'est pourquoi une recipe ne
change jamais d'une zone à l'autre.

Vous pouvez voir exactement cela sur le registre de la zone amont après sa
première synchronisation :

```text
$ curl -s -u zone-b-reader http://127.0.0.1:5012/v2/_catalog
{"repositories":["cookbook/alpine","docker.io/library/alpine"]}

$ curl -s -u zone-b-reader http://127.0.0.1:5012/v2/cookbook/alpine/tags/list
{"name":"cookbook/alpine","tags":["3.22.0","sha256-8fbf02f6…"]}
```

Le second tag est la signature de la recipe, rangée à côté ; c'est ce qui
permet à la nouvelle zone de la vérifier d'elle-même.

Deux conséquences :

- **La nouvelle zone ne peut lister que ce que détient la zone amont.**
  Une recipe que la zone amont n'a pas encore synchronisée, ou ne
  synchronisera jamais, ne se résout en rien (voir
  [ce que voit la zone amont](#ce-que-voit-la-zone-amont)).
- **Si la zone amont a défini `destination.basePath`**, le cookbook et les
  valeurs de substitution de la nouvelle zone le portent :
  `<registre>/<basePath>/cookbook` et `<registre>/<basePath>`. Ceci est
  écrit d'après le code (`internal/engine/destination.go`), pas rejoué.

Quand la zone amont n'est pas un registre alimenté par Tobby mais un proxy
de tirage ou le registre embarqué d'une instance amont, la valeur de
substitution diffère : voir
[ce que doit contenir le registre substitué](../retriever-cascade/#ce-que-doit-contenir-le-registre-substitué).

## 2. Écrire le Retriever de la nouvelle zone

```yaml
apiVersion: recipe.tobbyfetch.dev/v1alpha1
kind: Retriever

metadata:
  name: zone-b            # l'identité de la zone ; affichée sur chaque plan et chaque recipe

spec:
  cookbook: registry.zone-a.example/cookbook
  recipes:
    - name: alpine
      version: "3.22.x"   # les contraintes se résolvent à chaque synchronisation
```

`spec.cookbook` est le registre de la zone amont plus son chemin de
cookbook : l'instance liste les tags de `<cookbook>/<nom>` pour résoudre
chaque contrainte. Les recipes elles-mêmes ne changent pas, ni leurs hôtes
d'origine — la substitution, à l'étape suivante, s'en charge.

Le format du document est normatif sur le
[site recipe-spec](https://tobby-fetch.github.io/recipe-spec/). Le linter
livré avec la spécification vérifie les documents Retriever comme les
recipes, sans aucune instance :

```sh
recipe lint retriever.yaml
```

```text
1 file(s), 1 document(s) checked: all valid
```

## 3. Publier le Retriever

`retriever.source` accepte un fichier, une URL HTTP(S) ou une référence OCI
(voir [trois sources](../retriever-cascade/#trois-sources)). Les deux
premières n'ont besoin de rien de cette page. Un Retriever publié comme
artefact OCI suit ces règles, toutes lues dans
`internal/engine/retriever.go` :

| Règle | Détail |
| --- | --- |
| Référence | `<hôte>/<dépôt>[:<tag>]`, avec ou sans préfixe `oci://` dans `retriever.source`. Sans tag, `latest`. |
| Contenu | **Exactement une couche**, qui porte le document YAML, de 4 Mio au plus. Tout autre nombre de couches est refusé. |
| Types de média | Non vérifiés. Ni l'artifact type ni le type de la couche n'ont à correspondre à quoi que ce soit. |
| Mutabilité | Le tag est lu à chaque synchronisation : repousser le même tag change l'état désiré de la zone à son prochain cycle. |
| Signature | Aucune. Rien ne vérifie qui a poussé — voir [plus bas](#le-retriever-nest-pas-signé). |

Aucune commande Tobby ne publie un Retriever. `tobby recipe push` valide
et publie des **recipes**, et refuse tout le reste. Utilisez un client OCI
générique ; avec `oras` :

```sh
oras login registry.zone-a.example --username <écrivain> --password-stdin
oras push registry.zone-a.example/config/zone-b:v1 \
  --artifact-type application/vnd.example.retriever.v1+yaml \
  retriever.yaml:application/yaml
```

L'`--artifact-type` et le type de couche ci-dessus sont des conventions à
vous ; choisissez des valeurs que votre registre accepte. Le manifeste
poussé, tel que le rejeu l'a produit, porte la couche unique dont
l'instance a besoin :

```json
{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","artifactType":"application/vnd.example.retriever.v1+yaml","config":{"mediaType":"application/vnd.oci.empty.v1+json", …},"layers":[{"mediaType":"application/yaml","digest":"sha256:1a14b73e…","size":182,"annotations":{"org.opencontainers.image.title":"retriever.yaml"}}]}
```

Un second fichier poussé dans le même artefact fait deux couches, et
l'instance le refuse avec [`TBY-VAL-001`](../../reference/errors/#tby-val-001),
en nommant la référence et la contrainte : exactement une couche de document,
et combien en a cet artefact.

Où le mettre, c'est votre choix : n'importe où l'instance peut lire, sous
un dépôt qui n'est pas un cookbook. Le registre de la zone amont est
commode : le flux existe déjà. Un registre que contrôlent les
administrateurs de la zone vaut mieux quand la zone amont appartient à
quelqu'un d'autre.

## 4. Configurer la nouvelle instance

```yaml
mode: passthrough
# storage.root, state.root, server.addr : comme pour toute instance (voir Déployer)

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

Ce que fait chaque bloc ici :

- **`retriever.source`** : le document écrit et publié ci-dessus.
- **`trust.roots`** : l'instance vérifie chaque recipe contre **ses**
  racines à elle, quoi qu'ait déjà fait la zone amont. Donnez-lui la clé
  publique qui signe les recipes que vous voulez lui voir accepter, en
  général la même que la zone amont. La signature de la recipe est propagée
  avec la recipe : la nouvelle zone vérifie donc exactement les mêmes octets
  signés. Les périmètres et les assouplissements sont dans
  [la confiance du contenu](../../security/content-trust/).
- **`registries.substitutions`** : une entrée par hôte d'origine que
  nomment les recipes. La valeur est le registre de la zone amont, **sans**
  l'hôte d'origine ni chemin propre autre qu'un `basePath` : l'instance
  ajoute elle-même `<hôte d'origine>/<chemin>`.
- **`registries.credentialsFile`** : un fichier `dockerconfigjson`, indexé
  par l'hôte réellement contacté — le registre de la zone amont :

  ```json
  {"auths":{"registry.zone-a.example":{"auth":"<base64 de utilisateur:mot de passe>"}}}
  ```

  Gardez-le hors du store. Sur Kubernetes, c'est le Secret monté du chart.
  Le même fichier porte aussi les identifiants du registre de destination
  s'il en demande, sous son propre hôte.
- **`registries.allowlist`** (si vous en utilisez une) : c'est la liste
  d'autorisation de **cette** instance et elle doit nommer les deux hôtes
  ci-dessus : le registre de la zone amont (la source) et la destination.
  Oublier la source, c'est un refus de politique (`TBY-POL-001`, code de
  sortie `3`) ; le plan ci-dessous le montre avant que rien ne bouge.
- **`destination.registry`** : le registre de la nouvelle zone, un hôte nu.
  Un second Tobby, plus bas, lira depuis lui exactement comme celui-ci
  lit depuis la zone amont, avec `destination.cookbook` (par défaut
  `cookbook`) comme chemin de cookbook.

Redémarrez l'instance. Créez son premier administrateur et un jeton
operator pour l'automatisation comme sur n'importe quelle instance neuve
([le premier compte](../deploy/#le-premier-compte-sans-interaction),
[automatiser](../automate/)).

## 5. Planifier avant la première synchronisation

`tobby sync --dry-run` résout le Retriever, vérifie les recipes, lit la zone
amont et la destination, et n'écrit rien :

```sh
tobby sync --dry-run --config /etc/tobby/config.yaml
```

```text
plan of oci://127.0.0.1:5012/config/zone-b:v1 (zone zone-b)
  outcome: changes-planned
…
totals
  recipes:     1 (1 new, 0 outdated)
  ingredients: 1 (1 new, 0 outdated, 0 up-to-date)
  to transfer: 28.4 MiB (29761656 bytes, deduplicated by digest, net of the local store)
  store:       0 B → 28.4 MiB
  to promote:  at most 28.4 MiB towards 127.0.0.1:5013

recipes
  NAME    REQUESTED  RESOLVED  STATUS  SIGNATURE  SCOPE  TRANSFER
  alpine  3.22.x     3.22.0    new     verified   -      28.4 MiB

registry policy
  allow-list:  not declared (no registry is refused on that ground)
  127.0.0.1:5012                           allowed (source)
  127.0.0.1:5013                           allowed (destination)
```

La commande a rendu `5` : des changements sont planifiés, ce que doit dire
un premier plan. Lisez les trois choses que ce plan prouve avant tout
transfert : le Retriever a été trouvé et lu, chaque recipe s'est résolue en
une version, et sa signature indique `verified`.

Quand ce n'est pas le cas, le plan nomme le problème. Voici ceux que cette
procédure produit. Chacun a été rejoué sauf le dernier, rédigé d'après le
code :

| Vous voyez | Sortie | Cause et correction |
| --- | --- | --- |
| `outcome: failed`, [`TBY-REG-003`](../../reference/errors/#tby-reg-003) sur le Retriever | `1` | Le registre de la zone amont a refusé les identifiants, ou aucun ne correspondait à son hôte. Corrigez l'entrée de cet hôte dans le fichier désigné par `registries.credentialsFile`. |
| `outcome: verification-failed`, `SIGNATURE refused`, [`TBY-SIG-001`](../../reference/errors/#tby-sig-001) | `4` | Aucune racine de cette instance ne valide la recipe. Ajoutez la clé publique de signature à `trust.roots`. |
| `outcome: policy-refused`, [`TBY-POL-001`](../../reference/errors/#tby-pol-001) | `3` | Un registre du plan est hors de `registries.allowlist`. Ajoutez le registre de la zone amont. |
| `outcome: failed`, [`TBY-REG-005`](../../reference/errors/#tby-reg-005) sur une recipe | `1` | La zone amont n'a pas cette recipe. Ne listez que ce qu'elle détient. |
| `outcome: failed`, [`TBY-REG-006`](../../reference/errors/#tby-reg-006) sur une recipe | `1` | La zone amont détient la recipe, mais aucune version ne satisfait la contrainte — souvent parce qu'elle n'a pas encore synchronisé cette version. |

Le même plan fonctionne sur un Retriever candidat avant de le publier :
`tobby sync --dry-run --retriever oci://registry.zone-a.example/config/zone-b:next`
(un tag poussé pour l'occasion). Plus de détails sur les plans dans
[exploiter](../operate/#le-voir-venir), et pour un usage en pipeline dans
[automatiser](../automate/#la-barrière-du-plan).

## 6. Première synchronisation, et comment la vérifier

L'instance se synchronise seule toutes les `sync.interval` (15 minutes par
défaut). Pour lancer la première maintenant, avec un jeton operator dans
`TOBBY_API_TOKEN` :

```sh
export TOBBY_INSTANCE_URL=https://tobby.zone-b.example
tobby sync --wait
```

La première exécution a pris environ cinq secondes sur l'image de 28 Mio du
rejeu et s'est terminée `done`, avec quatre éléments : l'ingrédient et la
recipe récupérés (`alpine@3.22.0/alpine`, `alpine@3.22.0/recipe`), puis les
deux mêmes poussés vers la destination (`push/…`). Relancez-la et la zone
correspond déjà à son état désiré :

```text
task tsk_920cebfab6ac created on http://127.0.0.1:8092, waiting for it to finish
task tsk_920cebfab6ac on http://127.0.0.1:8092
  type:      sync
  reference: oci://127.0.0.1:5012/config/zone-b:v1
  status:    done
  duration:  0s
  items:     4 (0 done, 4 skipped, 0 failed)
```

Vérifiez ensuite de l'extérieur, sur les trois surfaces qui comptent.

**L'instance** connaît la recipe, sa version résolue et le verdict :

```sh
curl -s -H "Authorization: Bearer $TOBBY_API_TOKEN" "$TOBBY_INSTANCE_URL/api/v1/recipes" \
  | jq -c '.recipes[] | {name, version, zone, verified}'
```

```json
{"name":"alpine","version":"3.22.0","zone":"zone-b","verified":true}
```

**Le registre de destination** détient la même disposition que la zone
amont, sous son propre hôte :

```sh
curl -s http://127.0.0.1:5013/v2/_catalog
```

```json
{"repositories":["cookbook/alpine","docker.io/library/alpine"]}
```

**La signature a voyagé avec la recipe**, de sorte qu'un client de la
nouvelle zone peut la vérifier avec la même clé publique, sans Tobby :

```sh
cosign verify --key cosign.pub registry.zone-b.example/cookbook/alpine:3.22.0 \
  --private-infrastructure --insecure-ignore-tlog
```

`cosign` se termine par `The signatures were verified against the specified
public key`. Un `docker pull` de
`registry.zone-b.example/docker.io/library/alpine:3.22` rend ensuite le
digest que la recipe épingle. La consommation par de vrais clients est dans
[connecter vos clients](../connect-clients/).

## Ce que voit la zone amont

Rien ne change là-bas, et rien n'a à changer :

- **Aucune configuration.** La nouvelle zone est un lecteur d'un registre.
  Aucune liste d'autorisation de la zone amont ne la mentionne, et rien
  dans la configuration de son instance ne la désigne. Les
  `registries.allowlist` et `trust.roots` de la nouvelle zone concernent ce
  que *elle* lit et ce en quoi *elle* a confiance.
- **Un identifiant sur son registre.** C'est la seule chose que fournit la
  zone amont (ou qui exploite son registre) : un compte avec lequel la
  nouvelle instance peut lire. Donnez-lui la lecture et rien de plus. Si
  votre registre ne sait pas borner un compte à la lecture seule, dites-le
  dans votre propre runbook ; Tobby ne fait que lire ce registre pour le
  compte de la nouvelle zone.
- **De la latence, par construction.** La nouvelle zone voit ce que détient
  la zone amont *maintenant*. Dans le rejeu, une recipe `alpine 3.22.1` a
  été publiée et signée dans le cookbook d'origine ; tant que la zone amont
  n'avait pas synchronisé, le plan de la nouvelle zone continuait de
  résoudre `3.22.x` en `3.22.0`. Une fois la synchronisation de la zone
  amont passée, le plan suivant de la nouvelle zone a résolu `3.22.1`, et sa
  propre synchronisation a propagé la recipe dans son cookbook. Une zone ne
  devance jamais son parent.
- **Le prune reste local.** Ce que la nouvelle instance supprime est décidé
  par son propre Retriever et son propre réglage de prune
  ([exploiter](../operate/#le-prune-jusquau-retriever)).

Enchaîner une zone **sous** la nouvelle répète cette page avec la nouvelle
zone dans le rôle de la zone amont : son registre, son chemin de cookbook,
un compte dessus.

## Le Retriever n'est pas signé

C'est une position de conception assumée, énoncée plutôt que cachée. Le
Retriever porte la même confiance que la configuration de l'instance : son
emplacement, `retriever.source`, est fixé au démarrage et ne peut pas être
changé par l'interface ni par l'API, et qui peut écrire à cet emplacement
peut changer l'état désiré de la zone, et sa politique de scan avec. Les
recipes qu'il liste restent vérifiées une à une contre les racines de
confiance propres de l'instance : un Retriever altéré peut donc demander un
contenu **signé** différent ; il ne peut pas faire passer une recipe non
signée. Le raisonnement, et ce que cela protège, est dans le
[modèle de menaces](../../security/threat-model/). En pratique : accordez
l'écriture sur le dépôt, le fichier ou l'URL du Retriever comme vous le
feriez pour `/etc/tobby/config.yaml`.

## Et maintenant

- [Automatiser](../automate/) : piloter la nouvelle zone depuis la CI, avec
  un jeton et une barrière de plan.
- [Superviser une instance](../monitoring/) : quoi surveiller sur une zone
  qui dépend d'une autre.
- [Mettre à jour et revenir en arrière](../upgrade-rollback/) sur les
  instances d'une cascade.
