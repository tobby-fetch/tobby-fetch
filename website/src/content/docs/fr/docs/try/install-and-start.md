---
title: Installer et démarrer
description: Un binaire à télécharger, un quickstart guidé, un premier tour de l'interface — étape 1 sur 2.
sidebar:
  order: 1
---

**Étape 1 sur 2** — installer Tobby, démarrer une première instance, faire
le tour. L'[étape 2](../first-promotion/) promeut du contenu signé de bout
en bout. Les deux tiennent en dix minutes.

## Installer

Chaque release publie des binaires statiques pour Linux, macOS et Windows
(amd64/arm64). Téléchargez celui de votre machine et placez-le dans votre
`PATH` :

```sh
curl -LO https://github.com/tobby-fetch/tobby-fetch/releases/latest/download/tobby-linux-amd64
chmod +x tobby-linux-amd64
sudo mv tobby-linux-amd64 /usr/local/bin/tobby
```

Sur macOS, Homebrew tient en une ligne :

```sh
brew install tobby-fetch/tap/tobby
```

Vérifiez que le binaire répond :

```sh
tobby version
```

Chaque artefact de release porte une provenance SLSA Build L3, un SBOM
signé et une construction reproductible. La vérification prend une minute
et mérite d'être faite au moins une fois, même pour un essai :
[vérifier une release](../../project/verify-a-release/).

:::note[Installer pour la production ?]
Les paquets `.deb`/`.rpm`/`.apk` hors-ligne, l'image de conteneur signée,
le chart Helm, l'unit systemd de référence et la matrice OS vivent dans
[Déployer](../../passthrough/deploy/). C'est du contenu de production —
cette page reste sur le chemin le plus court vers une instance qui tourne.
:::

## Démarrer

`tobby quickstart` accompagne le premier démarrage : quelques questions,
le fichier de configuration écrit, le premier compte admin créé, et la
main passée à `serve` si vous le souhaitez.

```sh
mkdir tobby-demo && cd tobby-demo
tobby quickstart
```

Répondez aux questions — les défauts entre crochets conviennent, une seule
réponse compte vraiment :

1. **Répertoire du store** `[./storage]` — tout ce que Tobby détient.
2. **Répertoire d'état** `[./state]` — comptes et jetons, volontairement
   hors du store : les secrets ne voyagent jamais avec le contenu.
3. **Mode d'exécution** — répondez `passthrough` pour ce parcours. C'est
   le service permanent de promotion entre zones connectées, celui que
   l'étape 2 utilise. L'autre réponse, `mirror`, est le mode poste de
   travail qui fait traverser l'air gap au contenu — voir
   [le parcours média](../../air-gap/media-workflow/).
4. **Nom du compte admin** `[admin]`, puis son mot de passe (demandé deux
   fois, sans écho). L'outil calcule le hash ; le mot de passe n'est
   stocké nulle part.
5. **Fichier de configuration** `[./tobby.yaml]`.
6. **Démarrer l'instance maintenant** — répondez `y`.

Le quickstart est une aide interactive, jamais une obligation. Dans un
script ou un conteneur il refuse de deviner et affiche l'équivalent par
drapeaux ; la même mise en place, non interactive :

```sh
echo 'choisissez-un-mot-de-passe' | tobby quickstart --mode passthrough --password-stdin
tobby serve --config ./tobby.yaml
```

L'instance sert l'interface web et l'API sur `http://localhost:8080` et
refuse l'accès anonyme par défaut — connectez-vous avec le compte que le
quickstart vient de créer.

![L'écran de connexion d'une instance neuve — jamais d'interface ouverte](../../../../../assets/docs/fr-try-signin.png)

## Un premier tour

L'interface est rendue côté serveur, bilingue (français/anglais), et ne
demande aucune connectivité au-delà de l'instance elle-même.

### Naviguer dans le dépôt

L'écran **Contenu** liste ce que le store détient, sous forme
d'arborescence de dépôts. Les segments de chemin sont un fil d'Ariane :
cliquez-en un pour restreindre la liste à ce préfixe. Votre store est vide
pour l'instant — l'écran le dit sans détour, et l'étape 2 le remplit.

![L'écran Contenu dans docker.io/library/alpine, le fil d'Ariane montre le chemin relocalisé](../../../../../assets/docs/fr-try-content-store.png)

### Recherche et filtres

Le champ de recherche filtre les dépôts par sous-chaîne sur leur chemin
complet, et le filtre par type restreint au genre de contenu. Les deux se
combinent, et un résultat vide sous filtre est annoncé comme « aucun
résultat pour ces filtres » — distinctement d'un store vide.

### Les types de contenu au premier coup d'œil

Tout ce que le store contient est un artefact OCI, mais l'interface
distingue visuellement les genres : **image de conteneur**, **chart
Helm**, **ensemble de fichiers**, **recipe**, et **artefact OCI**
générique. On sait ce qu'un dépôt contient sans l'ouvrir.

![La liste de contenu : une image de conteneur, un chart Helm et un artefact de recipe côte à côte, groupés par hôte source](../../../../../assets/docs/fr-try-content-kinds.png)

### Copiez l'URL — c'est l'appel API

La navigation et l'API sont la même surface. L'écran `/content` et
`GET /api/v1/content` analysent exactement les mêmes paramètres — `q`,
`kind`, `prefix`, `page` — avec le même code : la parité tient par
construction. Prenez n'importe quelle vue filtrée dans la barre d'adresse
du navigateur, insérez `/api/v1` devant le chemin, et vous avez l'appel
JSON :

```
http://localhost:8080/content?q=alpine&kind=ContainerImage
http://localhost:8080/api/v1/content?q=alpine&kind=ContainerImage
```

Aucun langage de requête à apprendre, rien que l'interface sache faire
qu'un script ne puisse pas. L'instance sert aussi son propre contrat
d'API : un visualiseur sur `/api-docs`, le document OpenAPI brut sur
`/api/v1/openapi.yaml`. Détails dans la
[référence API](../../reference/api/).

---

**Suite : [votre première promotion](../first-promotion/)** — étape 2
sur 2. Une recipe signée, une promotion complète, un `docker pull` à
l'arrivée.
