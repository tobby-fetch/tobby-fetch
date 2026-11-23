---
title: "Aides à la consommation : table de correspondance et registries.yaml"
description: Générer, à partir du contenu du store, la table source-destination et le registries.yaml K3s/RKE2 qui orientent un cluster vers la registry de la zone — interface, API et ligne de commande.
sidebar:
  order: 6
---

Une fois le contenu arrivé dans la registry de la zone, les machines qui le
consomment ont besoin de deux choses : le nom exact que porte désormais chaque
ingrédient, et une configuration de runtime qui fait résoudre
`docker.io/bitnami/wordpress` à cet endroit. Tobby génère les deux à partir
des recipes que son store contient — elles correspondent donc à ce
qui a été poussé, au lieu d'être saisies à la main depuis la
[convention de relocalisation](../connect-clients/).

Les deux aides sont **en lecture seule** : Tobby les calcule, vous les
copiez. Rien n'est jamais écrit sur un nœud, et rien dans l'une ou l'autre
n'est un identifiant.

## Vers quelle registry elles pointent

Les aides pointent vers la registry où les clients de la zone tirent leurs
images :

| Situation | Registry utilisée | Préfixe de chemin utilisé |
|---|---|---|
| L'instance pousse vers une registry de zone | `destination.registry` | `destination.basePath` |
| La zone atteint la registry sous un autre nom | `consumption.registry` | `destination.basePath` |
| L'instance sert elle-même son `/v2/` à la zone | `consumption.registry` | `storage.basePrefix` |

```yaml
consumption:
  registry: registry.zone.example:5000   # un hôte nu, ce que les nœuds tirent
  caFile: /etc/ssl/certs/zone-ca.crt     # chemin SUR LES NŒUDS ; seulement écrit dans l'extrait
```

Si ni `destination.registry` ni `consumption.registry` n'est renseigné, toute
aide est refusée avec [`TBY-AID-001`](../../reference/errors/#tby-aid-001),
qui nomme les deux paramètres. `consumption.caFile` n'est jamais lu par
Tobby : c'est le chemin du certificat de l'autorité sur les nœuds du cluster,
recopié tel quel dans l'extrait.

## La table de correspondance

Une ligne par ingrédient de chaque recipe retenue, FileSets compris :

| Colonne | Signification |
|---|---|
| `recipe`, `recipeVersion` | La recipe dont relève l'ingrédient. |
| `ingredient`, `kind` | Le nom de l'ingrédient et son kind déclaré. |
| `source` | La référence que nomme la recipe, avec son tag (ou son digest à défaut), hôte canonicalisé : `docker.io/bitnami/wordpress:6.5.2`. |
| `destination` | La référence dans la registry de destination, relocalisée selon l'[ADR-0013](../../reference/srs-adr/) : `registry.zone.example:5000/docker.io/bitnami/wordpress:6.5.2`. |
| `digest` | Le digest épinglé — identique entre la source et la destination. |

Où l'obtenir, toujours avec les mêmes lignes :

- **Interface.** L'écran de correspondance de la recipe
  (`/recipes/{recipe}/mapping`) et l'écran **Consommation** (`/consumption`,
  tout le store) proposent la table en téléchargement CSV, JSON ou Markdown.
- **API.** `GET /api/v1/mapping?format=csv|json|markdown` pour tout le store ;
  `GET /api/v1/recipes/{recipe}/mapping?format=…` pour une recipe (sans
  `format`, cette route continue de répondre les versions enregistrées).
- **Ligne de commande.**

```sh
tobby consumption table --output csv > mapping.csv
tobby consumption table --recipe wordpress@6.5.2 --output markdown
tobby consumption table --recipe rke2@1.36.5 --destination 10.0.0.5:5000 --output json
```

La forme JSON est `{"destination": "…", "entries": [{"recipe": …}]}`, le
document publié sous `tobby consumption table` dans le
[schéma des sorties de la ligne de commande](../../reference/cli/#format-de-rapport).

## L'extrait registries.yaml

K3s et RKE2 lisent respectivement `/etc/rancher/k3s/registries.yaml` et
`/etc/rancher/rke2/registries.yaml`. Tobby génère une entrée `mirrors` par
hôte de registry source des images de conteneur des recipes retenues
(`docker.io` pour tous les alias Docker Hub) ; son `endpoint` est la registry
de la zone et son `rewrite` projette le dépôt sur son chemin relocalisé. Pour
une recipe qui contient des images de deux registries, la destination étant
en HTTP simple (listée dans `registries.insecure`) :

```yaml
mirrors:
  "docker.io":
    endpoint:
      - "http://10.0.0.5:5000"
    rewrite:
      "^(.*)$": "docker.io/$1"
  "registry.rancher.com":
    endpoint:
      - "http://10.0.0.5:5000"
    rewrite:
      "^(.*)$": "registry.rancher.com/$1"
```

Lecture : containerd confronte chaque clé de `rewrite`, une expression
régulière, à la partie dépôt du nom d'image (sans hôte ni tag) ;
`docker.io/library/busybox:1.36` est donc tiré depuis
`10.0.0.5:5000/docker.io/library/busybox:1.36`. Une référence épinglée par
digest garde son digest. Un port de l'hôte source s'écrit `_5000` dans le
chemin relocalisé (`forge.example.com_5000/$1`) ; un `destination.basePath`
est préfixé (`mirror/docker.io/$1`).

- Le schéma est `https`, sauf si la destination figure dans
  `registries.insecure` : il est alors `http`.
- Quand `consumption.caFile` est renseigné, une entrée `configs` pour la
  destination porte `tls.ca_file`. C'est le seul contenu `configs` que Tobby
  écrit.
- **Jamais d'identifiant.** Si la registry de la zone exige une
  authentification, un bloc `auth` en commentaire indique où la saisir ;
  remplissez-le sur le nœud.
- La sortie est déterministe — même store et même configuration, mêmes octets
  — et ne porte aucun horodatage. Un commentaire d'en-tête indique la version
  de Tobby, la destination, les recipes couvertes et les limites ci-dessous.

Trois endroits pour l'obtenir :

```sh
tobby consumption registries-yaml --recipe rke2@1.36.5 --recipe crown-app@1.0.0 \
  --destination 10.0.0.5:5000 > registries.yaml
```

`tobby consumption registries` en est un alias. Dans l'interface, l'extrait
figure sur l'écran de correspondance avec des boutons de copie et de
téléchargement ; l'API le sert sur `GET /api/v1/registries.yaml` et
`GET /api/v1/recipes/{recipe}/registries.yaml`. Installez le fichier sur
chaque nœud, puis redémarrez le service `k3s` ou `rke2`.

Les deux commandes lisent le répertoire du store (et la configuration pour la
registry et les préfixes) : elles fonctionnent donc sur un hôte côté
destination où aucune instance ne tourne, comme
[`tobby store verify`](../../reference/cli/#tobby-store-verify). `--recipe`
est répétable et accepte `nom` (toutes les versions enregistrées) ou
`nom@version` ; `--destination` remplace la registry pour l'exécution.

## Ce que les aides ne couvrent pas

Le mirror redirige **uniquement les tirages d'images faits par le runtime de
conteneurs du nœud**. Il ne s'applique pas :

- aux sources de charts Helm et aux contrôleurs GitOps (`repoURL` d'Argo CD,
  `HelmRepository` de Flux), qui doivent référencer le chemin relocalisé du
  chart ;
- aux politiques d'admission et à tout outil qui résout lui-même les
  références d'images, lesquels voient la référence telle qu'écrite dans le
  pod ;
- aux autres runtimes (CRI-O, Docker) : Tobby n'en génère aucune en v1.

Pour tous ceux-là, utilisez la colonne `destination` de la table de
correspondance. Tobby ne réécrit jamais les values d'un chart : cela
casserait digests et signatures. Le même avertissement figure dans l'en-tête
de chaque extrait généré.

## Vérifier que ça fonctionne

Sur un nœud configuré avec l'extrait, `crictl pull docker.io/library/busybox:1.36`
doit être servi par la registry de la zone — son journal d'accès montre
`GET /v2/docker.io/library/busybox/manifests/1.36` — et le digest égale la
colonne `digest` de la table.
