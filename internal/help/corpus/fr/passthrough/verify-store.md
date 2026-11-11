---
title: Vérifier l'intégrité du store
description: Vérifier un store entier à la demande, dans l'un ou l'autre mode — blobs contre leurs digests, complétude des manifests, signatures des recipes contre les clés de confiance configurées maintenant, contenu orphelin — en ligne de commande, dans l'interface et par l'API.
sidebar:
  order: 8
---

Un store ne vaut que par ses octets. Tobby les contrôle déjà par morceaux :
à l'import, avant un push, et sur un support qui arrive. Cette page décrit le
contrôle que vous lancez **quand vous voulez savoir** : une vérification du
store entier, à la demande, avec un rapport horodaté que vous pouvez
conserver. Elle fonctionne de la même façon sur le store d'une instance et sur
un store transportable — celui qu'une synchronisation miroir produit, ou un
support qui vient d'arriver.

La vérification **lit seulement**. Elle ne répare rien, ne supprime rien et
n'écrit rien dans le store, qui est identique à l'octet près après coup.

## Ce qu'elle contrôle

Quatre contrôles, chacun indépendant du verdict des autres :

| Contrôle | Ce qu'il établit | Défaut |
|---|---|---|
| **Blobs** | Chaque blob de l'arborescence adressée par contenu est recalculé et comparé au digest sous lequel il est rangé. Un fichier tronqué ou altéré est nommé par son chemin, avec le digest qu'il devrait avoir et celui qu'il a. | [`TBY-INT-001`](../../reference/errors/#tby-int-001) |
| **Manifests** | Chaque manifest atteignable depuis une recipe enregistrée — l'artefact de la recipe, ses artefacts de signature, chaque ingrédient — est complet : le manifest lui-même, sa config et ses couches, les enfants d'un index. Un index filtré par plateforme garde son digest épinglé tout en ne portant qu'une partie de ses enfants (FR-022) : un enfant que la recipe n'a *pas* retenu n'est donc pas un défaut — mais un enfant qu'elle a retenu, enregistré dans le graphe des recipes à la synchronisation, et désormais absent, est une plateforme perdue, et c'en est un. Une recipe enregistrée avant la v0.6.0 ne dit pas ce qu'elle a retenu : ses enfants absents sont lus comme non retenus jusqu'à ce qu'une synchronisation réécrive l'enregistrement. Un manifest illisible est signalé lui aussi, car ce qu'il référence n'a pas pu être contrôlé. | [`TBY-INT-002`](../../reference/errors/#tby-int-002), [`TBY-INT-005`](../../reference/errors/#tby-int-005) |
| **Signatures** | La signature de chaque recipe enregistrée est vérifiée contre les clés de confiance configurées **maintenant** sur cette instance, périmètres appliqués. Une clé sortie depuis des clés de confiance, ou un périmètre qui n'admet plus les recipes non signées, apparaît ici. Une recipe admise sans signature par un périmètre déclaré est signalée avec son [marqueur d'assurance réduite](../../security/reduced-assurance/). | [`TBY-SIG-001`](../../reference/errors/#tby-sig-001), qui nomme le périmètre |
| **Orphelins** | Le contenu qu'aucune recipe enregistrée ni aucune racine protégée n'atteint est listé. Les racines protégées sont celles que l'élagage conserve : imports unitaires, contenu amorcé par `/v2/`, base de vulnérabilités hors ligne. | [`TBY-INT-003`](../../reference/errors/#tby-int-003) |

Le verdict est **clean** (sain) ou **defects** (défauts). Le contenu orphelin
est une note, jamais une raison de rendre `defects` : il demande à être listé,
et le lister est tout ce qu'il demande. Deux sortes sont rapportées sous le
même code — un blob qu'aucun manifest atteignable depuis un tag ne référence
(ce qu'un ramasse-miettes reprendrait une fois son délai de grâce écoulé), et
un tag d'un dépôt géré par recipe qu'aucune recipe enregistrée n'atteint (ce
qu'une recipe retirée ou remplacée a laissé derrière elle). Le contenu qu'une
synchronisation est encore en train d'écrire peut apparaître comme orphelin
jusqu'à la fin du passage.

## En ligne de commande

```sh
tobby store verify --storage-root /srv/tobby/store
tobby store verify --config /etc/tobby/config.yaml --output json
```

La commande s'exécute dans son propre processus, sur le répertoire, et
fonctionne sur un hôte où aucune instance ne tourne — un poste qui vient de
recevoir un support, par exemple. Les clés de confiance viennent de la
configuration (`trust.roots`, `trust.scopes`), **jamais** du store : un support
n'en porte aucune qui compte. Elle refuse un répertoire qui n'est pas déjà un
store Tobby, au lieu de l'estampiller.

Les codes de sortie suivent la [table publiée](../../reference/cli/#exit-codes) :
`0` pour un store sain, `4` — la classe vérification — quand un défaut a été
trouvé. Un script peut s'y brancher, et lire le rapport avec `--output json` ;
le document est décrit par le schéma publié à côté du document OpenAPI.

Quand une instance **sert** le store, cette forme refuse et dit quoi faire :
elle ne voit pas le verrou de contenu de l'instance, si bien qu'une
suppression en cours se lirait comme un défaut. Confiez le travail à
l'instance :

```sh
tobby store verify --instance https://tobby.example:8443 --wait
```

La vérification s'exécute alors **sur l'instance**, comme tâche suivie, et
`--wait` la suit jusqu'au bout et sort selon son issue — `0`, `4` pour un
défaut, `1` si elle a été annulée ou a échoué. Sans `--wait`, la commande
affiche la tâche créée. Le jeton est lu dans `TOBBY_API_TOKEN`, comme pour
`tobby sync`.

## Depuis l'interface

L'écran **Intégrité du store** (`/integrity`, accessible depuis l'écran
Contenu et depuis l'écran d'administration du store) lance une vérification et
en montre le rapport. Tant qu'une vérification tourne, l'écran indique l'étape
où elle en est et sa progression, et se met à jour tout seul ; quand le verdict
tombe, il cesse d'interroger. Lire le dernier rapport est ouvert à tous les
rôles ; **lancer** une vérification demande le rôle opérateur, car elle relit
tout le disque.

L'écran ne propose jamais de réparer ni de supprimer quoi que ce soit. Le
rapport nomme ce qui ne va pas ; les outils pour y remédier sont ceux que le
produit a déjà — re-synchroniser la recipe qui atteint un blob endommagé,
élaguer selon le Retriever pour récupérer le contenu orphelin (voir
[Exploiter dans la durée](../operate/)).

## Depuis l'API

| Requête | Réponse |
|---|---|
| `POST /api/v1/store/verify` | `201` avec la tâche suivie. `409` ([`TBY-INT-004`](../../reference/errors/#tby-int-004)) nommant la vérification en cours quand une est déjà en attente ou en exécution — une à la fois. |
| `GET /api/v1/store/verification` | Le dernier rapport, en entier, avec l'identifiant et l'état de la tâche qui l'a produit ; pendant l'exécution, un instantané portant sa progression. `verification` vaut `null` tant qu'aucune n'a été demandée. |

Le même rapport figure sur la tâche elle-même (`GET /api/v1/tasks/{id}`,
membre `verification`). Un store avec des défauts est une tâche **en échec**
portant son défaut principal : la supervision qui lit les états de tâches la
voit comme n'importe quel autre échec.

La vérification s'exécute sur une **voie qui lui est propre**, à côté du
travailleur de tâches : une synchronisation — y compris la promotion continue
d'une instance passthrough — ne l'attend jamais, et elle n'en retarde aucune.
Elle tient les suppressions à l'écart pendant le parcours, si bien qu'un
ramasse-miettes ne peut rien supprimer sous elle : une suppression demandée
entre-temps l'attend, tandis que les écritures de contenu continuent.

Elle est annulable — **Annuler la vérification** sur l'écran, ou
`POST /api/v1/store/verify/cancel`. La tâche se termine alors en échec avec
[`TBY-INT-006`](../../reference/errors/#tby-int-006) et ne dit rien du store,
qu'une vérification ne fait que lire.

## Lire le rapport

```json
{
  "run_id": "20261111T200000Z-3f9a1c7e",
  "started_at": "2026-11-11T20:00:00Z",
  "finished_at": "2026-11-11T20:03:41Z",
  "verdict": "defects",
  "store": { "root": "/srv/tobby/store", "media_id": "…", "transportable": true },
  "checked": { "blobs": 412, "bytes": 8388608000, "manifests": 96, "recipes": 12 },
  "recipes": [ { "name": "wordpress", "version": "6.8.2", "complete": true,
                 "signature": "verified", "key_fingerprint": "SHA256:…" } ],
  "defects": [
    { "code": "TBY-INT-001",
      "path": "docker/registry/v2/blobs/sha256/ab/abcd…/data",
      "params": { "expected": "sha256:abcd…", "actual": "sha256:9f2e…" } }
  ],
  "findings": [ { "code": "TBY-INT-003", "path": "docker.io/library/old:1.0" } ],
  "defect_count": 1, "finding_count": 1
}
```

- **Horodaté et corrélé.** `started_at`, `finished_at` et le `run_id` de la
  tâche, pour rapprocher le rapport de ses enregistrements de journal.
- **Des codes et des paramètres, jamais des phrases.** Chaque défaut est un
  code de la taxonomie et le fichier ou l'objet concerné ; l'écran et la
  commande le rendent dans votre langue, et un script s'appuie sur le code.
- **Borné, avec des décomptes exacts.** Un store avec un million de blobs
  endommagés a un problème, pas un million de lignes : les 500 premiers de
  chaque liste sont nommés, `defects_omitted` et `findings_omitted` disent
  combien d'autres il y avait, et `defect_count` et `finding_count` sont
  toujours exacts.
- **Une ligne par recipe enregistrée**, avec sa complétude, l'état de sa
  signature selon la configuration de confiance de cette instance maintenant,
  et son marqueur d'assurance quand un périmètre l'a admise sans signature.

## Ce qui reste à votre charge

Une vérification est la réponse, pas la réparation. Un blob endommagé ou
manquant se répare en re-synchronisant les recipes qui l'atteignent (ou en
restaurant le store depuis une copie saine) ; un défaut de signature est une
question de clés de confiance — le message nomme le périmètre quand un
périmètre a décidé ; les orphelins se récupèrent en élaguant ou en retirant la
recipe qui les portait. Il n'y a pas de planification récurrente en v1 :
lancez-la avant de transporter un support, après avoir restauré une
sauvegarde, ou quand un disque commence à inquiéter.

## Observabilité et audit

- `tobby_integrity_verifications_total{verdict}` compte les vérifications
  terminées ; `tobby_integrity_last_verification_timestamp_seconds` date la
  dernière et est **absente** tant qu'aucune n'a abouti — alertez sur son âge,
  ou sur son absence.
- Une vérification terminée écrit un événement d'audit `store.verify` avec le
  demandeur, l'origine et le verdict comme issue. Une vérification qui échoue
  ou est annulée avant un verdict est enregistrée aussi, comme `failure`.
