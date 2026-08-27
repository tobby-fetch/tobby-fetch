---
title: Imports unitaires
description: Importer une image, un chart ou un artefact OCI par référence — inspecter d'abord, choisir les plateformes, ne transférer que ce qui manque.
sidebar:
  order: 6
---

Tout ne mérite pas une recipe. Une image de base à essayer, un chart en
cours d'évaluation, l'artefact qu'un développeur demande aujourd'hui —
Tobby importe un artefact OCI unique, de n'importe quel media type, par
référence, hors de tout run de recipe et sans runtime de conteneurs sur
l'instance (FR-023). C'est arrivé au jalon 2 et cela fait partie de
chaque release depuis.

## Depuis l'interface

L'écran **Import** (`/import`, rôle operator) est un parcours en deux
étapes, l'inspection avant l'import :

1. **Inspecter.** Collez une référence — `docker.io/library/nginx:1.29`,
   `ghcr.io/org/chart:2.0.1` — et l'instance contacte la registry par le
   même transport partagé que tout le reste (proxy, CA privées, fichier
   de credentials, liste blanche). Une inspection est bornée par
   `import.inspectTimeout` (20s par défaut) ; un dépassement répond avec
   le code dédié `TBY-REG-004`, distinct de « injoignable ».
2. **Sélectionner et importer.** Pour une image multi-plateforme, chaque
   plateforme est une ligne avec son digest et sa taille, et un statut
   par ligne qui dit si le store la détient déjà. Le bouton annonce ce
   qu'il va faire — « Importer (2 plateformes, ~180 MB) » — avant que
   vous ne le pressiez.

![Import unitaire, étape 2 : le résultat de l'inspection avec une ligne par plateforme, digests, tailles, statut vis-à-vis du store, et le bouton d'import chiffré](../../../../../assets/docs/import-step2.png)

<!-- TODO: screenshot: capture française de l'import unitaire, étape 2 -->

L'écran est adressable : `/import?ref=…` pré-remplit le formulaire et
déclenche l'inspection au chargement, ce qui fait aussi fonctionner le
« réessayer » d'une tâche d'import en échec et les liens profonds depuis
le détail d'un manifeste. Le POST ne fait jamais confiance à ce que le
navigateur a affiché : le serveur ré-inspecte et épingle les digests que
la registry sert *au moment de l'import* (FR-026).

## Depuis l'API

L'interface est un miroir strict de l'API (FR-061) :

```sh
# Inspect: platforms, digests, sizes, local status
curl -su "$USER:$PASS" \
  "https://tobby.zone.example/api/v1/import/inspect?ref=docker.io/library/nginx:1.29"

# Import (operator role): returns the tracked task. Platforms are named
# with the digest the inspection reported — the pin travels with the request.
curl -su "$USER:$PASS" -X POST \
  -H 'Content-Type: application/json' \
  -d '{"reference":"docker.io/library/nginx:1.29",
       "platforms":[{"name":"linux/amd64","digest":"sha256:…"}]}' \
  "https://tobby.zone.example/api/v1/import"
```

L'import s'exécute comme une tâche suivie — progression, résultat par
item et journal téléchargeable, comme toute synchronisation (voir
[exploiter](../../passthrough/operate/#la-tâche-est-lunité-dobservation)).
Le résultat est tirable depuis la registry embarquée par tag et par
digest, sous le même chemin relocalisé qu'une recipe aurait employé.

## Différentiel par digest

Les imports sont différentiels, à deux grains. Les blobs et manifestes
déjà présents dans le store ne sont jamais re-téléchargés (FR-028) —
réimporter un tag qui a bougé ne transfère que ce qui a réellement
changé. Et l'inspection rapporte le statut de chaque plateforme
vis-à-vis du store : importer le `linux/arm64` d'une image dont vous
détenez déjà le `linux/amd64` ne déplace que les couches arm64. Quand
tout est déjà à jour, l'écran le dit et vous donne la commande de pull
plutôt qu'un bouton qui ne ferait rien.

## Quand préférer une recipe

Un import unitaire répond à « donne-moi ceci, maintenant ». Une recipe
répond à « maintiens la zone à ce niveau » — et tout ce qui compose la
[boucle de promotion continue](../../passthrough/overview/#la-promotion-continue)
s'appuie sur les recipes, pas sur les imports :

| | Import unitaire | Recipe |
| --- | --- | --- |
| Réconcilié à chaque cycle | non — importé une fois | oui |
| Contraintes de version (`~`, `^`, `12.x`) | non — une seule référence | oui |
| Poussé vers la registry de destination | non — store seulement | oui |
| Listé dans le cookbook de la zone | non | oui (FR-034) |
| Signature exigée par défaut | non — voir ci-dessous | oui (FR-033) |

Si la même référence revient sans cesse par l'écran d'import, c'est une
recipe qui demande à être écrite —
[écrivez-la et publiez-la](../../recipes/write-and-publish/).

## Ce qu'un import unitaire signifie pour la provenance

Soyez lucide sur ce que vous attestez. Une recipe arrive signée, et sa
signature — vérifiée contre vos trust roots — couvre les digests exacts
de chaque ingrédient : quelqu'un de responsable a déclaré que *ce
contenu, à ces digests, a sa place dans cette zone*. Un import unitaire
ne porte aucune déclaration de ce genre. Ce que vous obtenez, c'est de
l'intégrité, pas un aval : les digests sont épinglés à l'inspection et
vérifiés de bout en bout, la liste blanche de registries s'applique
toujours (FR-030), et l'action est attribuée à l'opérateur qui l'a
déclenchée dans la piste d'audit (FR-094). Personne n'a signé le *choix*.

Tobby garde les deux provenances distinctes dans ses enregistrements. Le
contenu importé unitairement ne participe pas à la promotion, et c'est le
seul contenu qu'un administrateur peut retirer dépôt par dépôt — depuis
la page du dépôt ou avec `DELETE /api/v1/content/{repo}` — la suppression
étant auditée (FR-044) ; sur le contenu géré par une recipe, l'action est
désactivée et nomme la recipe qui le gère. En mode miroir, les imports
unitaires sont exclus du prune par défaut, qui ne touche que le contenu
géré par des recipes (FR-045). Le modèle complet est décrit dans
[confiance dans le contenu](../../security/content-trust/).

Dernière étape de la section :
[exploiter dans la durée](../../passthrough/operate/).
