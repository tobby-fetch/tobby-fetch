---
title: Votre première promotion
description: Une instance Tobby entre un registre amont et le registre de votre zone — une recipe signée, une promotion complète et un docker pull depuis le registre de zone — étape 2 sur 2.
sidebar:
  order: 2
---

**Étape 2 sur 2** — vous avez l'instance de
l'[étape 1](../install-and-start/). Cette page la met à sa vraie place :
entre les registres où le contenu vit déjà et la zone qui le consomme.

Une **promotion**, c'est ce pour quoi Tobby existe : un contenu qui
existe déjà en amont est copié dans le registre d'une zone, inchangé,
après vérification de sa signature et de ses digests. Vous allez épingler
une image publique dans une *recipe* (la liste de ce qu'il faut livrer),
signer la recipe, la publier dans un *cookbook* (le dépôt qui range les
recipes), déclarer l'état désiré de la zone dans un *Retriever*, laisser
votre instance promouvoir — et finir par un `docker pull` depuis le
registre de zone. Chacun de ces mots a son entrée dans le
[glossaire](../../discover/glossary/). Tout ce qui suit n'utilise que de
l'outillage utilisateur ordinaire — rien qui vienne de l'arborescence des
sources.

<svg viewBox="0 0 640 230" role="img" aria-label="Un registre amont et un cookbook à gauche, une instance Tobby qui vérifie puis pousse vers le registre de zone à droite, où tirent les clients de la zone" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
  <defs>
    <marker id="fp-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--sl-color-gray-3)" />
    </marker>
  </defs>
  <!-- zones -->
  <rect x="8" y="30" width="216" height="170" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <rect x="288" y="30" width="344" height="170" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <text x="116" y="20" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Où vit le contenu</text>
  <text x="460" y="20" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Votre zone</text>
  <!-- upstream boxes -->
  <rect x="28" y="48" width="176" height="46" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="116" y="67" text-anchor="middle" font-size="12" fill="var(--sl-color-gray-1)">docker.io</text>
  <text x="116" y="82" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">l'image, déjà publiée</text>
  <rect x="28" y="130" width="176" height="46" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="116" y="149" text-anchor="middle" font-size="12" fill="var(--sl-color-gray-1)">registre cookbook</text>
  <text x="116" y="164" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">votre recipe signée</text>
  <!-- tobby -->
  <rect x="312" y="88" width="140" height="48" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="382" y="108" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="382" y="124" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">vérifie, puis pousse</text>
  <!-- clients -->
  <rect x="492" y="88" width="120" height="48" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="552" y="108" text-anchor="middle" font-size="12" fill="var(--sl-color-gray-1)">registre de zone</text>
  <text x="552" y="124" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">les clients tirent ici</text>
  <!-- flows -->
  <line x1="204" y1="71" x2="308" y2="100" stroke="var(--sl-color-gray-3)" marker-end="url(#fp-arrow)" />
  <text x="254" y="72" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">image, par digest</text>
  <line x1="204" y1="153" x2="308" y2="124" stroke="var(--sl-color-gray-3)" marker-end="url(#fp-arrow)" />
  <text x="254" y="158" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">recipe + signature</text>
  <line x1="452" y1="112" x2="488" y2="112" stroke="var(--sl-color-gray-3)" marker-end="url(#fp-arrow)" />
  <!-- note -->
  <text x="460" y="186" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-2)">Vérifié à la frontière — jamais réécrit, jamais re-signé</text>
</svg>

:::note[Il vous faut]
- L'instance de l'étape 1, **toujours en marche** dans son terminal, et
  un **second terminal** ouvert dans le même répertoire `tobby-demo`.
  Toutes les commandes et tous les fichiers ci-dessous vivent dans ce
  répertoire : la configuration désigne `./retriever.yaml` et
  `./cosign.pub` par chemin relatif.
- Docker avec le plugin `buildx` (`docker buildx version` répond).
- `cosign` **3.x** (`cosign version`). La commande de signature
  ci-dessous emploie des drapeaux qu'une version 2.x n'a pas tous.
- `curl`, et un accès sortant à Docker Hub pour deux petites images.
- Les ports TCP `5001` et `5002` libres, pour deux registres jetables.
:::

## Qui fait quoi

| Élément | Rôle |
|---|---|
| `docker.io` | Le registre amont. L'image y vit déjà ; rien n'y est poussé. |
| Un registre cookbook (`:5001`) | Un registre OCI ordinaire qui héberge votre recipe signée. N'importe quel registre où vous pouvez pousser convient — ici, un registre local jetable. |
| Votre instance (`:8080`) | Celle de l'étape 1. Sur cette page elle reçoit un Retriever, une racine de confiance et une destination. C'est elle qui promeut. |
| Le registre de zone (`:5002`) | Le registre d'où tirent les clients de votre zone : la destination de la promotion. En production il existe déjà (Harbor, un registre cloud, …) — ici, un second registre jetable. |
| Une paire de clés `cosign` | Signe la recipe. La clé publique devient la **racine de confiance** de votre instance : la seule clé dont elle accepte les signatures. |

## 1. Deux registres jetables

Les recipes se publient dans un *cookbook* : un dépôt OCI ordinaire, sur
n'importe quel registre où vous pouvez pousser. Si vous en avez déjà un
(ghcr.io, Harbor, …), utilisez-le et adaptez les adresses qui suivent.
Pour le parcours, un registre jetable en boucle locale suffit — et un
second joue le registre de zone :

```sh
docker run -d --rm --name cookbook -p 5001:5000 registry:2
docker run -d --rm --name zone -p 5002:5000 registry:2
```

Chaque commande affiche un identifiant de conteneur. Les deux registres
sont vides et oublient tout à l'arrêt.

Les ports hôte commencent à 5001 et non 5000 : sur macOS, le récepteur
AirPlay occupe le port 5000 et répond 403 à sa place — un grand classique des tutoriels
de registry locale.

## 2. Épingler l'image

Une recipe désigne le contenu par digest, jamais par simple tag. Lisez le
digest que `alpine:3.24.4` résout aujourd'hui :

```sh
docker buildx imagetools inspect docker.io/library/alpine:3.24.4
```

Les premières lignes ressemblent à ceci — **gardez la valeur de
`Digest`**, la recipe l'épingle :

```
Name:      docker.io/library/alpine:3.24.4
MediaType: application/vnd.oci.image.index.v1+json
Digest:    sha256:f59abe65…
```

Prenez le digest du haut, celui de l'index multi-plateforme, pas ceux
par plateforme listés sous `Manifests`. Sans `buildx`,
`crane digest docker.io/library/alpine:3.24.4` ou
`skopeo inspect --raw docker://docker.io/library/alpine:3.24.4 | sha256sum`
donnent la même valeur.

## 3. Écrire la recipe

Une recipe décrit une livraison cohérente. Une recipe « cuisinée » — la
seule qu'un cookbook publie — épingle chaque ingrédient par digest : une
seule signature atteste les octets exacts de toute la livraison. Écrivez
`alpine.yaml`, avec le digest de l'étape précédente :

```yaml
apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe

metadata:
  name: alpine
  version: 3.24.4
  description: Première promotion — une image épinglée

spec:
  ingredients:
    - name: alpine
      kind: ContainerImage
      ref: docker.io/library/alpine   # référence nominale, sans tag
      version: 3.24.4
      digest: sha256:<le digest affiché par imagetools>
```

Les concepts — recipes, cookbooks, retrievers — sont couverts dans
[Comprendre les recipes](../../recipes/understand/).

## 4. La publier et la signer

`tobby recipe push` publie la recipe comme artefact OCI après l'avoir
contrôlée : un document qui n'est pas une recipe valide, pas entièrement
épinglé, ou déjà publié avec un contenu différent est refusé. Le registre
jetable parle HTTP simple, ce qui exige un accord explicite par hôte :

```sh
export TOBBY_REGISTRIES_INSECURE=127.0.0.1:5001
tobby recipe push alpine.yaml 127.0.0.1:5001/cookbook/alpine:3.24.4
```

La commande répond par le digest de la recipe publiée, et par la commande
de signature à lancer :

```
sha256:3aa1de2f…
127.0.0.1:5001/cookbook/alpine:3.24.4 published
sign it with:
  cosign sign --key <key> --use-signing-config=false --tlog-upload=false 127.0.0.1:5001/cookbook/alpine@sha256:3aa1de2f…
```

Signer reste hors de Tobby — il ne détient jamais de clé privée. Créez
une paire de clés, puis signez le digest affiché par `recipe push`.
`--yes` saute la demande de confirmation de cosign et
`--allow-insecure-registry` est là parce que le registre jetable parle
HTTP simple :

```sh
cosign generate-key-pair
cosign sign --key cosign.key --yes --allow-insecure-registry \
  --use-signing-config=false --tlog-upload=false \
  "127.0.0.1:5001/cookbook/alpine@<le digest affiché par recipe push>"
```

`cosign generate-key-pair` demande un mot de passe pour protéger la clé
privée, deux fois, et `cosign sign` le redemande : choisissez-en un
quelconque, un mot de passe vide est accepté pour un essai. La commande
écrit `cosign.key` (privée, reste ici) et `cosign.pub` (publique).
`cosign sign` se termine par
`Pushing signature to: 127.0.0.1:5001/cookbook/alpine` ; cosign 3.x
affiche aussi deux avis de dépréciation sur les drapeaux ci-dessous, qui
sont attendus.

Les deux drapeaux `--use-signing-config=false --tlog-upload=false`
gardent la signature vérifiable hors-ligne : sans eux, cosign 3.x publie
dans le journal de transparence public — un appel réseau qu'un signataire
de zone restreinte ne devrait pas faire, et une consultation que la
destination ne pourrait de toute façon pas effectuer.

## 5. Déclarer l'état désiré de la zone

Un Retriever nomme ce que la zone doit contenir. Écrivez
`retriever.yaml` :

```yaml
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever

metadata:
  name: demo-zone

spec:
  cookbook: 127.0.0.1:5001/cookbook
  recipes:
    - name: alpine
      version: "3.24.4"
```

Une version exacte est prise au mot ; une contrainte (`6.x`, `~0.16.2`)
se résout contre le cookbook à chaque synchronisation — une version
corrective arrive en la publiant, sans fichier à modifier.

## 6. Pointer votre instance dessus

Dans le premier terminal, arrêtez l'instance de l'étape 1 avec `Ctrl-C`
— l'arrêt est gracieux — et ajoutez quatre choses à la fin de son
`tobby.yaml` : le Retriever, la racine de confiance, l'accord HTTP simple
pour les deux registres jetables, et la destination :

```yaml
retriever:
  source: ./retriever.yaml   # un fichier, une URL https://, ou une référence OCI

trust:
  roots:
    - name: demo-signing-key
      keyFile: ./cosign.pub

registries:
  insecure: ["127.0.0.1:5001", "127.0.0.1:5002"]

destination:
  registry: 127.0.0.1:5002   # le registre de zone : un hôte nu, sans schéma ni chemin
```

`destination.registry` est la clé qui fait de ceci une promotion : elle
nomme le registre de zone dans lequel le contenu vérifié est poussé. Sans
elle, l'instance récupère et vérifie toujours, mais ne pousse nulle part
— elle se contente de servir le contenu depuis son propre registre
embarqué.

Puis redémarrez, dans ce même terminal (si vous avez déplacé l'instance
sur un autre port à l'étape 1, son bloc `server:` est déjà dans le fichier
et y reste) :

```sh
tobby serve --config ./tobby.yaml
```

Le journal se termine par `http server listening`. Un refus de démarrer
nomme la clé en cause — le plus souvent un chemin : `./retriever.yaml` et
`./cosign.pub` sont résolus depuis le répertoire où vous lancez `tobby`.

## 7. Promouvoir

En mode passthrough l'instance réconcilie sur son propre rythme (toutes
les 15 minutes par défaut). Déclenchez un cycle tout de suite : l'écran
**Recipes** propose une action de synchronisation, ou en ligne de
commande :

```sh
curl -u admin -X POST http://localhost:8080/api/v1/sync
```

`curl` demande le mot de passe admin, et la réponse est la tâche qui
vient d'être créée :

```
{"task":{"id":"tsk_…","run_id":"…","type":"sync","status":"running", …}}
```

Observez l'écran **Tâches** : la synchronisation résout le Retriever,
récupère la recipe depuis le cookbook, vérifie sa signature cosign contre
votre racine de confiance, puis tire l'image directement depuis
`docker.io` et la contrôle contre le digest épinglé — en ne transférant
que ce qui manque à la zone — et pousse les deux dans le registre de
zone. Comptez une demi-minute. La tâche se termine en `done` avec quatre
items : l'image et la recipe récupérées, puis les deux mêmes poussées.
L'écran **Recipes** montre alors la recipe, sa version résolue et son
verdict de vérification.

Le verdict d'analyse de l'image indique *non analysé* : une instance
neuve n'a pas encore de base de vulnérabilités, et l'analyse est
informative par défaut. C'est attendu ici ;
[l'analyse de vulnérabilités](../../security/vulnerability-scanning/)
couvre le vrai montage.

![Le détail de tâche : les items de la synchronisation avec leur statut et le journal JSON brut, identifiant de run inclus](../../../../../assets/docs/fr-task-detail.png)
![L'écran Recipes montrant la recipe promue, signature vérifiée](../../../../../assets/docs/fr-try-recipes-verified.png)

:::note[Si la tâche échoue]
Le détail de la tâche nomme un code `TBY-…` sur l'item en échec. Les
trois causes habituelles sur cette page :

- **La recipe est refusée** — la signature ne se vérifie pas :
  `cosign.pub` n'est pas la clé qui a signé, ou la signature vise le tag
  au lieu du digest. Voir
  [une recipe est refusée](../../reference/troubleshooting/#une-recipe-est-refusée).
- **L'image est refusée** — le digest de `alpine.yaml` n'est pas celui
  affiché par `imagetools`. Corrigez le fichier, puis publiez-le sous une
  nouvelle version : une version publiée n'est jamais écrasée.
- **Un registre ne répond pas** — `docker.io` derrière un proxy, ou l'un
  des deux registres jetables arrêté. Voir
  [un registre source ne répond pas](../../reference/troubleshooting/#un-registre-source-ne-répond-pas).

[La référence des erreurs](../../reference/errors/) explique chaque code.
:::

Relancez la synchronisation : elle se termine en une seconde avec ses
quatre items *ignorés* — la zone correspond déjà à son état désiré, rien
n'est transféré.

## 8. Tirer depuis le registre de zone

Le registre de zone détient maintenant le contenu promu, sous un chemin
qui dit d'où il vient :

```sh
docker pull 127.0.0.1:5002/docker.io/library/alpine:3.24.4
```

```
Digest: sha256:f59abe65…
Status: Downloaded newer image for 127.0.0.1:5002/docker.io/library/alpine:3.24.4
```

Si votre daemon Docker tourne dans une VM (Rancher Desktop, certains
montages Colima) et que le pull n'arrive pas à se connecter, son
`127.0.0.1` est la VM, pas votre machine — utilisez l'adresse LAN de
votre hôte.

Le pull réussit et le digest est celui que vous avez épinglé à
l'étape 2 : l'image a été transportée, vérifiée et poussée — jamais
réécrite, jamais re-signée. La recipe signée a voyagé avec elle, vers
`cookbook/alpine` sur le registre de zone.

Votre instance sert aussi le même contenu depuis son propre registre
embarqué, pour une zone qui n'a pas de registre à elle : `docker login
127.0.0.1:8080` avec le compte créé par le quickstart, puis
`docker pull 127.0.0.1:8080/docker.io/library/alpine:3.24.4`.

Pourquoi le chemin garde l'origine, et comment brancher de vrais clients
(mirrors containerd, GitOps), c'est dans
[Brancher vos clients](../../passthrough/connect-clients/).

:::note[Derrière un proxy d'entreprise ?]
Deux choses joignent Internet sur cette page, et chacune a besoin de ses
réglages de proxy : Docker, pour tirer `registry:2` et résoudre le
digest, et Tobby, pour récupérer l'image sur `docker.io`. Le trafic
sortant de Tobby se configure une fois pour toute l'instance — proxy
authentifié et PKI privée compris : voir
[Réseau d'entreprise](../../passthrough/network/).
:::

## Ce que vous venez de faire

La scène était petite, mais rien n'était simulé : une recipe signée dans
un cookbook, une zone qui déclare son état désiré, une promotion qui
vérifie tout avant de pousser. La production garde cette forme et change
ce qui l'entoure : le cookbook part sur un registre alimenté par votre
processus de qualification, les clés viennent de ce même processus, le
Retriever se publie comme artefact OCI, et `destination.registry` nomme
votre vrai registre de zone. Elle ajoute aussi ce qu'un décor en boucle
locale laisse de côté — TLS, credentials de registre, liste
d'autorisation des registres et stockage durable — tout cela dans
[Déployer](../../passthrough/deploy/).

Quand vous avez fini, `Ctrl-C` arrête l'instance et
`docker rm -f cookbook zone` supprime les deux registres.

:::tip[Astuce de labo : un second Tobby comme registry amont]
Cela fonctionne parce qu'une instance Tobby expose un registre OCI
standard : une seconde instance jetable peut jouer les deux rôles amont à
la fois — source d'images et cookbook — une façon astucieuse de rejouer
tout ce scénario hors-ligne, sans toucher au moindre vrai registre. Ce
n'est **pas** la place normale de Tobby dans une architecture ; sa place,
c'est celle du schéma ci-dessus.

Démarrez la doublure avec l'authentification explicitement désactivée
(une exception bruyante et auditée — acceptable pour un décor en boucle
locale, jamais pour une instance qui compte) :

```sh
TOBBY_MODE=passthrough TOBBY_AUTH_DISABLED=true \
TOBBY_SERVER_ADDR=127.0.0.1:8092 TOBBY_STORAGE_ROOT=./upstream \
  tobby serve
```

Les outils ordinaires poussent dans son registre embarqué, sous le chemin
qui préserve l'origine — `docker push` affiche le digest que votre recipe
épingle ensuite :

```sh
docker pull docker.io/library/alpine:3.24.4
docker tag docker.io/library/alpine:3.24.4 127.0.0.1:8092/docker.io/library/alpine:3.24.4
docker push 127.0.0.1:8092/docker.io/library/alpine:3.24.4
```

Adaptez la recipe : épinglez le digest affiché par `docker push` — ce que
vous avez poussé est un manifeste mono-plateforme, son digest diffère donc
de celui de l'index d'origine. Publiez-la et signez-la contre
`127.0.0.1:8092/cookbook/alpine:3.24.4` exactement comme ci-dessus,
pointez le `cookbook` du Retriever dessus, et remplacez le bloc
`registries` de votre `tobby.yaml` par :

```yaml
registries:
  insecure: ["127.0.0.1:8092", "127.0.0.1:5002"]
  substitutions:
    "docker.io": "127.0.0.1:8092"
```

La substitution est la partie intéressante : la recipe continue de dire
`docker.io/library/alpine`, et seule l'adresse réellement contactée
change. C'est exactement ainsi que de vraies zones enchaînent un Tobby
derrière un autre, recipes inchangées — voir
[Retriever et cascade](../../passthrough/retriever-cascade/).
:::

## Et maintenant

- **Zones connectées (passthrough)** — le cas d'usage livré, en vrai :
  [architecture et promotion continue](../../passthrough/overview/), puis
  [Déployer](../../passthrough/deploy/) sur Kubernetes ou en VM.
- **Zones isolées (air-gap)** — la même promotion, portée sur un média
  amovible, rejouée sur ce poste en dix minutes de plus :
  [l'air-gap sur un seul poste](../air-gap-on-one-machine/).
- **Écrire vos propres recipes** — le raisonnement derrière une liste
  d'ingrédients, et les pièges :
  [écrire, publier et signer](../../recipes/write-and-publish/).
- **Lecteur sécurité ?** — le modèle derrière ce que vous venez de voir,
  en une page : [le modèle de sécurité en une page](../../security/one-pager/).
