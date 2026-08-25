---
title: Votre première promotion
description: Deux instances locales, une recipe signée, une promotion complète et un docker pull depuis le registre de zone — étape 2 sur 2.
sidebar:
  order: 2
---

**Étape 2 sur 2** — vous avez l'instance de
l'[étape 1](../install-and-start/). Cette page met en scène une seconde
instance jouant la zone amont, publie une recipe signée, laisse votre
instance la promouvoir, et se termine par un `docker pull` depuis le
registre de zone. Tout ce qui suit n'utilise que de l'outillage
utilisateur ordinaire — rien qui vienne de l'arborescence des sources.

Rien ne sort de votre machine : les deux instances se parlent en boucle
locale. Le seul accès externe est un `docker pull` depuis Docker Hub au
départ.

**Il vous faut :** le binaire `tobby` et le `tobby.yaml` de l'étape 1,
plus `docker`, `cosign` et `curl`.

## La distribution

| Élément | Rôle |
|---|---|
| Instance « amont » (`:8092`) | Joue la zone amont : un registre et un cookbook. Authentification explicitement désactivée — c'est un décor, pas une installation de production. |
| Votre instance (`:8080`) | Celle de l'étape 1 : sécurisée, avec sa clé de confiance et son Retriever. C'est elle qui promeut. |
| Une paire de clés `cosign` | Signe la recipe. La clé publique devient la racine de confiance de votre instance. |

## 1. Monter la zone amont

Dans un second terminal, depuis le même répertoire `tobby-demo` :

```sh
TOBBY_MODE=passthrough TOBBY_AUTH_DISABLED=true \
TOBBY_SERVER_ADDR=127.0.0.1:8092 TOBBY_STORAGE_ROOT=./upstream \
  tobby serve
```

Désactiver l'authentification est une exception délibérée et bruyante :
l'instance l'inscrit dans son journal d'audit et l'affiche en bannière
dans l'interface. Acceptable pour un décor en boucle locale ; jamais pour
une instance qui compte.

## 2. Lui donner du contenu

Tobby embarque un registre OCI standard : les outils ordinaires y
poussent. Tirez une petite image publique et poussez-la dans l'instance
amont, sous le chemin qui préserve son origine :

```sh
docker pull docker.io/library/alpine:3.22.1
docker tag docker.io/library/alpine:3.22.1 127.0.0.1:8092/docker.io/library/alpine:3.22.1
docker push 127.0.0.1:8092/docker.io/library/alpine:3.22.1
```

`docker push` affiche une ligne se terminant par `digest: sha256:…` —
**gardez ce digest**, la recipe l'épingle.

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
      digest: sha256:<le digest affiché par docker push>
```

Les concepts — recipes, cookbooks, retrievers — sont couverts dans
[Comprendre les recipes](../../recipes/understand/).

## 4. La publier et la signer

`tobby recipe push` publie la recipe comme artefact OCI après l'avoir
contrôlée : un document qui n'est pas une recipe valide, pas entièrement
épinglé, ou déjà publié avec un contenu différent est refusé. L'instance
amont parle HTTP simple, ce qui exige un accord explicite par hôte :

```sh
export TOBBY_REGISTRIES_INSECURE=127.0.0.1:8092
tobby recipe push alpine.yaml 127.0.0.1:8092/cookbook/alpine:3.22.1
```

Le digest publié sort sur stdout, prêt pour la signature. Signer reste
hors de Tobby — il ne détient jamais de clé privée :

```sh
cosign generate-key-pair
cosign sign --key cosign.key --yes --allow-insecure-registry \
  --use-signing-config=false --tlog-upload=false \
  "127.0.0.1:8092/cookbook/alpine@<le digest affiché par recipe push>"
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
  cookbook: 127.0.0.1:8092/cookbook
  recipes:
    - name: alpine
      version: "3.22.1"
```

Une version exacte est prise au mot ; une contrainte (`6.x`, `~0.16.1`)
se résout contre le cookbook à chaque synchronisation — une version
corrective arrive en la publiant, sans fichier à modifier.

## 6. Pointer votre instance dessus

Arrêtez l'instance de l'étape 1 avec `Ctrl-C` — l'arrêt est gracieux —
et ajoutez quatre choses à son `tobby.yaml` : le Retriever, la racine de
confiance, l'accord HTTP simple, et une substitution de source :

```yaml
retriever:
  source: ./retriever.yaml   # un fichier, une URL https://, ou une référence OCI

trust:
  roots:
    - name: cle-signature-demo
      keyFile: ./cosign.pub

registries:
  insecure: ["127.0.0.1:8092"]
  substitutions:
    "docker.io": "127.0.0.1:8092"
```

La substitution est ce qui permet aux zones de se chaîner : la recipe
continue de dire `docker.io/library/alpine`, et seule l'adresse
réellement contactée change — le contenu est tiré depuis l'instance
amont, au chemin qui préserve l'origine. Les recipes ne sont jamais
réécrites pour s'adapter à une zone. Puis redémarrez :

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
récupère la recipe, vérifie sa signature cosign contre votre racine de
confiance, contrôle le digest épinglé, et ne transfère que ce qui manque
à la zone. L'écran **Recipes** montre alors la recipe, sa version résolue
et son verdict de vérification.

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
Ce scénario ne quitte pas la boucle locale : il se rejoue tel quel sur un
poste verrouillé — seul le `docker pull` initial a besoin d'une sortie.
Quand vos vraies sources sont derrière un proxy authentifié ou une PKI
privée, le chemin sortant unique de Tobby se configure une fois pour
toute l'instance : voir
[Réseau d'entreprise](../../passthrough/network/).
:::

## Ce que vous venez de faire

La scène était petite, mais rien n'était simulé : une recipe signée dans
un cookbook amont, une zone qui déclare son état désiré, une promotion
qui re-vérifie tout avant de servir. La production remplace les décors —
l'amont devient un vrai registre ou une autre instance Tobby, le
Retriever se publie comme artefact OCI, les clés viennent de votre
processus de qualification.

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
