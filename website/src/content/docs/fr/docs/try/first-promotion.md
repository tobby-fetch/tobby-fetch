---
title: Votre première promotion
description: Une instance Tobby entre un registre amont et votre zone — une recipe signée, une promotion complète et un docker pull depuis le registre de zone — étape 2 sur 2.
sidebar:
  order: 2
---

**Étape 2 sur 2** — vous avez l'instance de
l'[étape 1](../install-and-start/). Cette page la met à sa vraie place :
entre les registres où le contenu vit déjà et la zone qui le consomme.
Vous allez épingler une image publique dans une recipe, signer la recipe,
la publier dans un cookbook, déclarer l'état désiré de la zone, laisser
votre instance promouvoir — et finir par un `docker pull` depuis le
registre de zone. Tout ce qui suit n'utilise que de l'outillage
utilisateur ordinaire — rien qui vienne de l'arborescence des sources.

<svg viewBox="0 0 640 230" role="img" aria-label="Un registre amont et un cookbook à gauche, une instance Tobby qui promeut vers sa zone à droite, les clients de la zone tirant depuis le registre de Tobby" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
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
  <text x="382" y="124" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">store + registre de zone</text>
  <!-- clients -->
  <rect x="492" y="88" width="120" height="48" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="552" y="108" text-anchor="middle" font-size="12" fill="var(--sl-color-gray-1)">clients de la zone</text>
  <text x="552" y="124" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">docker pull</text>
  <!-- flows -->
  <line x1="204" y1="71" x2="308" y2="100" stroke="var(--sl-color-gray-3)" marker-end="url(#fp-arrow)" />
  <text x="254" y="72" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">image, par digest</text>
  <line x1="204" y1="153" x2="308" y2="124" stroke="var(--sl-color-gray-3)" marker-end="url(#fp-arrow)" />
  <text x="254" y="158" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">recipe + signature</text>
  <line x1="452" y1="112" x2="488" y2="112" stroke="var(--sl-color-gray-3)" marker-end="url(#fp-arrow)" />
  <!-- note -->
  <text x="460" y="186" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-2)">Vérifié à la frontière — jamais réécrit, jamais re-signé</text>
</svg>

**Il vous faut :** le binaire `tobby` et le `tobby.yaml` de l'étape 1,
plus `docker`, `cosign` et `curl`. L'image vient de Docker Hub ; la seule
chose que vous montez vous-même est un registre cookbook jetable en
boucle locale.

## La distribution

| Élément | Rôle |
|---|---|
| `docker.io` | Le registre amont. L'image y vit déjà ; rien n'y est poussé. |
| Un registre cookbook (`:5000`) | Un registre OCI ordinaire qui héberge votre recipe signée. N'importe quel registre où vous pouvez pousser convient — ici, un registre local jetable. |
| Votre instance (`:8080`) | Celle de l'étape 1 : sécurisée, avec sa clé de confiance et son Retriever. C'est elle qui promeut. |
| Une paire de clés `cosign` | Signe la recipe. La clé publique devient la racine de confiance de votre instance. |

## 1. Un registre cookbook

Les recipes se publient dans un *cookbook* : un dépôt OCI ordinaire, sur
n'importe quel registre où vous pouvez pousser. Si vous en avez déjà un
(ghcr.io, Harbor, …), utilisez-le et adaptez les adresses qui suivent.
Pour le parcours, un registre jetable en boucle locale suffit :

```sh
docker run -d --rm --name cookbook -p 5000:5000 registry:2
```

## 2. Épingler l'image

Une recipe désigne le contenu par digest, jamais par simple tag. Lisez le
digest que `alpine:3.22.1` résout aujourd'hui :

```sh
docker buildx imagetools inspect docker.io/library/alpine:3.22.1
```

Les premières lignes affichent `Digest: sha256:…` — **gardez ce digest**,
la recipe l'épingle.

## 3. Écrire la recipe

Une recipe décrit une livraison cohérente. Une recipe « cuite » — la
seule qu'un cookbook publie — épingle chaque ingrédient par digest : une
seule signature atteste les octets exacts de toute la livraison. Écrivez
`alpine.yaml`, avec le digest de l'étape précédente :

```yaml
apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe

metadata:
  name: alpine
  version: 3.22.1
  description: Première promotion — une image épinglée

spec:
  ingredients:
    - name: alpine
      kind: ContainerImage
      ref: docker.io/library/alpine   # référence nominale, sans tag
      version: 3.22.1
      digest: sha256:<le digest affiché par imagetools>
      # Seules ces plateformes sont transférées. L'index d'origine est
      # préservé tel quel, donc le digest épinglé reste valable.
      platforms: [linux/amd64, linux/arm64]
```

Les concepts — recipes, cookbooks, retrievers — sont couverts dans
[Comprendre les recipes](../../recipes/understand/).

## 4. La publier et la signer

`tobby recipe push` publie la recipe comme artefact OCI après l'avoir
contrôlée : un document qui n'est pas une recipe valide, pas entièrement
épinglé, ou déjà publié avec un contenu différent est refusé. Le registre
jetable parle HTTP simple, ce qui exige un accord explicite par hôte :

```sh
export TOBBY_REGISTRIES_INSECURE=127.0.0.1:5000
tobby recipe push alpine.yaml 127.0.0.1:5000/cookbook/alpine:3.22.1
```

Le digest publié sort sur stdout, prêt pour la signature. Signer reste
hors de Tobby — il ne détient jamais de clé privée :

```sh
cosign generate-key-pair
cosign sign --key cosign.key --yes --allow-insecure-registry \
  --use-signing-config=false --tlog-upload=false \
  "127.0.0.1:5000/cookbook/alpine@<le digest affiché par recipe push>"
```

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
  name: zone-demo

spec:
  cookbook: 127.0.0.1:5000/cookbook
  recipes:
    - name: alpine
      version: "3.22.1"
```

Une version exacte est prise au mot ; une contrainte (`6.x`, `~0.16.1`)
se résout contre le cookbook à chaque synchronisation — une version
corrective arrive en la publiant, sans fichier à modifier.

## 6. Pointer votre instance dessus

Arrêtez l'instance de l'étape 1 avec `Ctrl-C` — l'arrêt est gracieux —
et ajoutez trois choses à son `tobby.yaml` : le Retriever, la racine de
confiance, et l'accord HTTP simple pour le registre jetable :

```yaml
retriever:
  source: ./retriever.yaml   # un fichier, une URL https://, ou une référence OCI

trust:
  roots:
    - name: cle-signature-demo
      keyFile: ./cosign.pub

registries:
  insecure: ["127.0.0.1:5000"]
```

Puis redémarrez :

```sh
tobby serve --config ./tobby.yaml
```

## 7. Promouvoir

En mode passthrough l'instance réconcilie sur son propre rythme (toutes
les 15 minutes par défaut). Déclenchez un cycle tout de suite : l'écran
**Recipes** propose une action de synchronisation, ou en ligne de
commande :

```sh
curl -u admin -X POST http://localhost:8080/api/v1/sync
```

Observez l'écran **Tâches** : la synchronisation résout le Retriever,
récupère la recipe depuis le cookbook, vérifie sa signature cosign contre
votre racine de confiance, puis tire l'image directement depuis
`docker.io` et la contrôle contre le digest épinglé — en ne transférant
que ce qui manque à la zone. L'écran **Recipes** montre alors la recipe,
sa version résolue et son verdict de vérification.

<!-- TODO: capture : écran Tâches avec la tâche de sync et son journal en direct -->
<!-- TODO: capture : écran Recipes montrant la recipe vérifiée -->

Relancez la synchronisation : elle se termine sans rien transférer — la
zone correspond déjà à son état désiré.

## 8. Tirer depuis le registre de zone

Le registre embarqué de votre instance sert désormais le contenu promu.
Docker s'authentifie avec le même compte que l'interface :

```sh
docker login 127.0.0.1:8080    # le compte créé par le quickstart
docker pull 127.0.0.1:8080/docker.io/library/alpine:3.22.1
```

Le pull réussit, digest intact : l'image a été transportée, vérifiée et
servie — jamais réécrite, jamais re-signée. Le chemin dit d'où vient le
contenu ; pourquoi c'est important, et comment brancher de vrais clients
(mirrors containerd, GitOps), c'est dans
[Brancher vos clients](../../passthrough/connect-clients/).

:::note[Derrière un proxy d'entreprise ?]
Le scénario se rejoue tel quel sur un poste verrouillé : le tirage de
l'image depuis `docker.io` est le seul appel sortant de Tobby, et le
trafic sortant de Tobby se configure une fois pour toute l'instance —
proxy authentifié et PKI privée compris. Voir
[Réseau d'entreprise](../../passthrough/network/).
:::

## Ce que vous venez de faire

La scène était petite, mais rien n'était simulé : une recipe signée dans
un cookbook, une zone qui déclare son état désiré, une promotion qui
vérifie tout avant de servir. La production ne change que les adresses —
le cookbook part sur un registre alimenté par votre processus de
qualification, le Retriever se publie comme artefact OCI, les clés
viennent de ce même processus.

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
docker pull docker.io/library/alpine:3.22.1
docker tag docker.io/library/alpine:3.22.1 127.0.0.1:8092/docker.io/library/alpine:3.22.1
docker push 127.0.0.1:8092/docker.io/library/alpine:3.22.1
```

Adaptez la recipe : épinglez le digest affiché par `docker push`, et
retirez la ligne `platforms:` — ce que vous avez poussé est un manifeste
mono-plateforme, pas un index. Publiez-la et signez-la contre
`127.0.0.1:8092/cookbook/alpine:3.22.1` exactement comme ci-dessus,
pointez le `cookbook` du Retriever dessus, et remplacez le bloc
`registries` de votre `tobby.yaml` par :

```yaml
registries:
  insecure: ["127.0.0.1:8092"]
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
  amovible : [le parcours média](../../air-gap/media-workflow/).
- **Écrire vos propres recipes** — le raisonnement derrière une liste
  d'ingrédients, et les pièges :
  [écrire, publier et signer](../../recipes/write-and-publish/).
- **Lecteur sécurité ?** — le modèle derrière ce que vous venez de voir,
  en une page : [le one-pager sécurité](../../security/one-pager/).
