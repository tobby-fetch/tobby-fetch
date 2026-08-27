---
title: Exploiter dans la durée
description: Sondes, suivi et reprise des tâches, ce sur quoi alerter, sauvegarde, croissance du store, montées de version et arrêt propre.
sidebar:
  order: 7
  badge:
    text: Partiel
    variant: caution
---

Une instance passthrough est conçue pour tourner sans surveillance
pendant des mois. Cette page dit ce que cela vous coûte : ce qu'il faut
surveiller, ce qu'il faut sauvegarder, ce qui grossit, et comment monter
de version. Une partie de l'outillage qui rendra tout cela plus simple
est encore devant — ces parties portent un badge ci-dessous plutôt que
d'être passées sous silence.

## Sondes et métriques

| Chemin | Rôle |
| --- | --- |
| `/healthz` | Vivacité. Répond dès que le listener est en place : le processus est vivant, pas forcément utile. |
| `/readyz` | Disponibilité. 503 tant que le store et la configuration ne sont pas exploitables, et 503 de nouveau pendant le drainage d'arrêt. |
| `/metrics` | OpenMetrics (FR-091). Derrière la même authentification que toutes les autres surfaces — donnez un compte ou un jeton viewer à votre collecteur ; le ServiceMonitor du chart gère le basic auth. |

Ouvrir un gros store prend du temps : le déploiement de référence emploie
une startup probe (30 × 5s) plutôt qu'un seuil de liveness relâché, de
sorte que l'instance a le temps de démarrer sans qu'un processus bloqué
survive plusieurs minutes ensuite.

## La tâche est l'unité d'observation

Chaque synchronisation et chaque import unitaire s'exécute comme une
tâche suivie (FR-062) : état, progression par ingrédient, et un journal
téléchargeable brut. La liste des tâches est `/tasks` dans l'interface et
`GET /api/v1/tasks` dans l'API (les deux paginées) ; le détail d'une
tâche et son journal sont `GET /api/v1/tasks/{id}` et
`GET /api/v1/tasks/{id}/logs`. Les journaux sont en JSON structuré, avec
des clés de corrélation stables — run ID, task ID, recipe, ingrédient,
digest (FR-090) — si bien qu'une synchronisation se reconstitue
entièrement en filtrant sur son task ID. Le schéma est décrit dans
[métriques et journaux](../../reference/metrics-logs/).

Les tâches terminées sont bornées : la file conserve les
`tasks.keepFinished` plus récentes (500 par défaut ; `0` garde tout) et
purge les plus anciennes avec leurs fichiers de journal. Les tâches en
attente et en cours ne sont jamais purgées.

### Les transferts interrompus reprennent

Un processus tué ou une connexion coupée ne fait pas repartir un run de
zéro. Une synchronisation reprend depuis un état persisté, et les blobs
déjà stockés ne sont jamais re-téléchargés (FR-029). Depuis la v0.4.0, la
reprise est aussi fine à l'intérieur des gros blobs (R-29) : au-delà de
`transfer.resumeThreshold` (64MiB par défaut), les octets sont écrits
dans le répertoire d'état avec leur offset, et la tentative suivante
demande le reste par une requête HTTP `Range` — un téléchargement
interrompu à 90 % repart à 90 %, y compris après un processus tué, pas
seulement après une connexion perdue. L'intégrité reste bloquante : le
digest est calculé sur l'ensemble du fichier de reprise, préfixe repris
compris, avant qu'un seul octet n'atteigne le store, et une source qui
ignore `Range` est détectée et redémarrée plutôt que concaténée. Le
détail de la tâche montre la progression par blob, et notamment si un
transfert a repris.

La conséquence opérationnelle : le volume d'état détient temporairement
une copie de chaque blob reprenable en cours de transfert — c'est pour
cette raison que le déploiement de référence le dimensionne à 20Gi.
`transfer.resumeThreshold: 0` désactive ce tampon et rétablit un
streaming pur.

## Ce sur quoi alerter

- `/readyz` non-200 hors des fenêtres de déploiement.
- Les tâches en échec — interrogez `GET /api/v1/tasks` ou alertez sur les
  métriques d'échec de tâche.
- Les refus de politique : les rejets par la liste blanche (FR-030) et
  les échecs de vérification de signature (FR-033) sont journalisés,
  audités et comptés dans les métriques. Dans une zone saine ils sont à
  zéro ; toute marche d'escalier est soit une attaque, soit une dérive de
  configuration, et les deux méritent d'être remontées.
- Un âge de dernière synchronisation réussie qui dépasse quelques
  multiples de `sync.interval` — un proxy ou un credential cassé en
  silence se voit d'abord ici.
- L'occupation du volume d'état, puisque les fichiers de reprise y
  vivent.

Les familles de métriques sont listées dans
[métriques et journaux](../../reference/metrics-logs/) — avec cette
réserve honnête : leurs noms ne sont pas encore contractuels.

## Sauvegarde : le répertoire d'état

La racine d'état est **la** cible de sauvegarde. Elle détient ce que rien
ne recrée : les comptes, les jetons, la paire TLS servie, la dérogation
d'intervalle. Elle est petite : sauvegardez-la comme tout répertoire
précieux — snapshots ou copies de fichiers de `state.root`, pris pendant
que l'instance est arrêtée ou depuis un snapshot du système de fichiers.
Le store n'a besoin d'aucune sauvegarde : tout ce qu'il contient peut
être récupéré de nouveau, et le perdre coûte de la bande passante, pas de
l'identité. Ne placez jamais l'état sur le volume du store ; Tobby refuse
l'imbrication sans discuter.

:::note[À venir — jalon 7]
Une procédure documentée de restauration et de reconstruction
(reconstituer une instance complète à partir d'une sauvegarde d'état plus
une re-synchronisation, R-27) est prévue pour le jalon 7. À suivre sur la
page [État du projet](../../discover/status/).
:::

## La croissance du store, dite sans détour

**Par défaut, rien ne nettoie automatiquement le store en mode
passthrough.** Chaque version synchronisée et chaque import unitaire reste
jusqu'à ce qu'un administrateur retire à la main les dépôts importés
unitairement (FR-044) — le contenu géré par des recipes n'est pas
supprimable individuellement. Une zone dont les recipes suivent des
contraintes mouvantes accumule toutes les versions qu'elles ont un jour
résolues.

Ce défaut est délibéré. Un store de transit passthrough n'est pas une unité
de livraison, et un exploitant qui demande du contenu plus frais n'a pas
demandé que le contenu ancien soit supprimé : une boucle de réconciliation
qui rétrécit en silence le store dont une zone tire est exactement l'échec
que ce défaut évite.

### Le prune jusqu'au Retriever

Posez `sync.prune: true` (ou `TOBBY_SYNC_PRUNE=true`) et chaque cycle de
réconciliation retire le contenu géré par recipe que le Retriever résolu ne
référence plus. Trois sortes de contenu ne sont **jamais** éligibles, parce
qu'aucune n'est gérée par une recipe :

- les imports unitaires (FR-023),
- la base de vulnérabilités hors-ligne (FR-032),
- tout ce qui est poussé par `/v2/` hors des espaces gérés (amorçage, UC3).

Deux garde-fous méritent d'être connus. Un cycle où **une** recipe n'a pas
pu être résolue ne prune rien et le dit dans le journal de la course : le
contenu d'une recipe non résolue est indiscernable du contenu que le
Retriever a retiré, et supprimer sur la foi d'une panne réseau n'est pas un
compromis que ce produit accepte. Et chaque élément retiré est nommé —
dépôt, tag, digest, et la recipe qui l'avait apporté — dans le journal du
cycle, pas seulement compté.

### Surveiller le volume

Posez `storage.occupancyThreshold` (par exemple `500GiB`) et l'instance dit
quand elle le dépasse : un avertissement permanent sur chaque page de
l'interface, le même fait sur `GET /api/v1/content` et
`GET /api/v1/retriever`, et la métrique `tobby_store_occupancy_exceeded`.
Repasser en dessous rétracte les trois — un avertissement qui apparaît et ne
s'efface jamais est un avertissement que les exploitants apprennent à
ignorer. Non posé signifie non surveillé, ce qui est rapporté comme tel et
jamais comme « dans les clous ».

### Le voir venir

`tobby sync --dry-run` rapporte ce que la prochaine synchronisation ferait
sans en faire quoi que ce soit : versions résolues, statuts par digest,
volume dédupliqué à transférer, taille projetée du store contre l'espace
libre du volume, **et le contenu qu'un prune retirerait**. Rien n'est écrit
et la cadence de réconciliation n'est pas touchée. Le même rapport est sur
`POST /api/v1/plan` et sur l'écran `/recipes/plan`, où un Retriever candidat
peut être planifié à la place de celui qui est configuré — c'est ainsi qu'on
relit un changement de Retriever avant de l'adopter.

Le code de sortie `5` signifie « des changements sont prévus », distinct de
`0` (« rien à faire ») : une barrière d'intégration peut s'y brancher sans
voir un plan chargé comme une compilation cassée. Voir la
[référence CLI](../../reference/cli/).

:::note[À venir — jalon 6]
La vérification d'intégrité du store à la demande, depuis l'interface et
l'API (R-31), arrive avec le jalon 6.
:::

## Monter de version

Lisez d'abord les [notes de release](../../discover/status/) ; la
politique de compatibilité — ce qui est déjà stable, ce qui se fige en
1.0, et comment les formats de store sont portés d'une version à l'autre
— vit dans
[processus de release et compatibilité](../../project/release-compatibility/).

- **Paquets :** vérifiez le nouveau paquet, puis installez-le par-dessus
  l'ancien (`dpkg -i` / `rpm -U` / `apk add --allow-untrusted`) et
  redémarrez le service. Les paquets sont sans script ; rien ne s'exécute
  à l'installation.
- **Kubernetes :** `helm upgrade tobby ./deploy/charts/tobby --namespace
  tobby --reuse-values --set image.tag=v0.4.2` — épinglez `image.digest`
  en production. La stratégie est `Recreate` avec un seul replica : l'ancien
  pod relâche les volumes avant que le nouveau ne démarre, il n'y a donc
  jamais deux écrivains sur un même store. Attendez-vous à une courte
  interruption à chaque montée de version ; les deux PVC portent
  `helm.sh/resource-policy: keep`, si bien que même un `helm uninstall`
  laisse les données en place.

:::note[À venir — jalon 6]
La mise à jour de Tobby par son propre canal OCI (R-25) — la nouvelle
release voyageant comme du contenu vérifié, entre zones et à travers
l'air gap comme tout ce qu'il transporte — arrive avec le jalon 6.
:::

## Arrêt propre

Sur SIGTERM ou SIGINT, l'instance cesse d'accepter du travail nouveau,
passe `/readyz` à 503, et laisse aux transferts en cours
`shutdown.gracePeriod` (30s par défaut, `--shutdown-grace-period`) pour
se terminer ou se checkpointer avant de sortir en 0 (FR-093). Les
transferts checkpointés reprennent au démarrage suivant. Ce qui supervise
le processus doit attendre plus longtemps que ce délai de grâce —
`terminationGracePeriodSeconds: 60` dans le déploiement de référence,
`TimeoutStopSec=60` dans une unit systemd — sinon le kill final tombe en
plein checkpoint.

Cela clôt le parcours passthrough. À partir d'ici :
[écrivez des recipes](../../recipes/write-and-publish/) pour étoffer ce
que la zone détient, ou lisez comment la même instance prépare le travail
des [zones isolées](../../air-gap/media-workflow/).
