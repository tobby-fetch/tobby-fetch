---
title: Installer et démarrer
description: Un binaire à télécharger, un quickstart guidé, un premier tour de l'interface — étape 1 sur 2.
sidebar:
  order: 1
---

**Étape 1 sur 2** — installer Tobby, démarrer une première instance, faire
le tour. L'[étape 2](../first-promotion/) promeut du contenu signé de bout
en bout. Les deux prennent une dizaine de minutes une fois Docker et
cosign installés ; comptez une demi-heure si vous les installez en route.

:::note[Avant de commencer]
- **Cette page** demande un terminal, un navigateur, et le port TCP
  `8080` libre sur votre machine.
- Les commandes supposent un shell POSIX (bash, zsh) ; sous Windows,
  utilisez WSL ou Git Bash.
- **L'étape 2** demande en plus Docker avec le plugin `buildx`, `cosign`
  3.x et `curl`. Vérifiez-les maintenant avec `docker buildx version` et
  `cosign version` ; la
  [page d'installation de cosign](https://docs.sigstore.dev/cosign/system_config/installation/)
  couvre toutes les plateformes.
- L'instance que vous démarrez ici occupe son terminal. L'étape 2 se
  déroule dans un **second terminal**, ouvert dans le même répertoire
  `tobby-demo`.
- Les mots que vous ne connaissez pas encore — recipe, cookbook,
  Retriever, promotion — sont dans le [glossaire](../../discover/glossary/).
:::

## Installer

Chaque release publie des binaires statiques pour Linux, macOS et Windows
(amd64/arm64). Téléchargez celui de votre machine et placez-le dans votre
`PATH` :

```sh
curl -LO https://github.com/tobby-fetch/tobby-fetch/releases/latest/download/tobby-linux-amd64
chmod +x tobby-linux-amd64
sudo mv tobby-linux-amd64 /usr/local/bin/tobby
```

Sur une machine ARM, l'artefact est `tobby-linux-arm64`.

Sur macOS, Homebrew tient en une ligne :

```sh
brew install tobby-fetch/tap/tobby
```

Sous Windows le binaire est portable — un unique `.exe`, sans installeur
et sans dépendance d'exécution. Installez-le avec winget ou Scoop :

```powershell
winget install tobby-fetch.tobby
# ou
scoop bucket add tobby https://github.com/tobby-fetch/scoop-bucket
scoop install tobby
```

ou téléchargez `tobby-windows-amd64.exe` (ou `-arm64`) depuis la release
et placez-le dans votre `PATH`. winget peut avoir quelques jours de
retard sur une release — la raison est sur la
[page des limites](../../discover/limits/) ; Scoop et le téléchargement
direct sont à jour le jour de la sortie. Ce que Tobby supporte sous
Windows, et la poignée de choses qui s'y comportent différemment, sont dans
[Plateformes supportées](../../reference/platforms/).

Vérifiez que le binaire répond :

```sh
tobby version
```

Il affiche une ligne — la version, le commit, la date de build, la
version de Go et votre plateforme :

```
tobby v0.7.0 (commit …, built …, go1.27.4, linux/amd64)
```

Chaque artefact de release porte une provenance SLSA Build L3, un SBOM
signé et une construction reproductible. Un essai peut s'en passer ;
avant tout usage qui compte,
[vérifiez une release](../../project/verify-a-release/).

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
réponse compte vraiment. Le terminal pose les questions en anglais ; elles
sont citées ici telles qu'il les affiche :

1. `Store directory (storage root) [./storage]` — le répertoire du store :
   tout ce que Tobby détient.
2. `State directory (state root) [./state]` — le répertoire d'état :
   comptes et jetons, volontairement hors du store : les secrets ne
   voyagent jamais avec le contenu.
3. `Operating mode ("passthrough" or "mirror")` — le mode d'exécution :
   répondez `passthrough` pour ce parcours. C'est le service permanent de
   promotion entre zones connectées, celui que l'étape 2 utilise. L'autre réponse, `mirror`, est
   le mode poste de travail qui fait traverser l'air gap au contenu — voir
   [le parcours média](../../air-gap/media-workflow/).
4. `Admin account name [admin]` — le nom du compte admin, puis son mot de
   passe (`Password`, puis `Confirm password`, sans écho). Seul son hash
   est stocké, jamais le mot de passe lui-même.
5. `Configuration file to write [./tobby.yaml]` — le fichier de
   configuration à écrire.
6. `Start the instance now` — démarrer l'instance maintenant : répondez
   `y`.

Le quickstart se termine par un récapitulatif, puis passe la main à
l'instance :

```
Setup complete:
  mode:          passthrough
  storage root:  ./storage
  state root:    ./state
  configuration: ./tobby.yaml
starting: tobby serve --config ./tobby.yaml
```

À partir de là, le terminal affiche le journal JSON de l'instance et
reste occupé : laissez-le ouvert. `Ctrl-C` arrête l'instance, proprement ;
`tobby serve --config ./tobby.yaml` la relance.

:::note[Si elle ne démarre pas]
`listen tcp :8080: bind: address already in use` signifie qu'un autre
programme tient le port 8080. Déplacez l'instance sur un autre port :
ajoutez `server:` puis, en retrait dessous, `addr: 127.0.0.1:8090` à
`tobby.yaml`, relancez `tobby serve --config ./tobby.yaml`, et remplacez
`8080` par `8090` dans toute la suite. Le réglage vit dans le fichier
parce qu'une variable d'environnement ne survivrait pas au redémarrage de
l'[étape 2](../first-promotion/). Tout autre refus
nomme un code `TBY-…` :
[Dépannage — l'instance ne démarre pas](../../reference/troubleshooting/#linstance-ne-démarre-pas).
:::

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

:::tip[Pressé ?]
Le store est vide : la plupart de ces écrans n'ont encore rien à montrer.
Passez directement à l'[étape 2](../first-promotion/) et revenez ici une
fois le contenu arrivé.
:::

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
combinent, et un résultat vide sous filtre est annoncé par « Aucun dépôt ne
correspond à ces filtres. » (en anglais : « No repository matches these
filters. ») — distinctement d'un store vide.

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
