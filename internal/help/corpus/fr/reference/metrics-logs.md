---
title: Métriques et journaux
description: Les métriques que l'instance expose aujourd'hui, le schéma des journaux JSON et ses clés de corrélation stables, et ce sur quoi alerter.
sidebar:
  order: 5
  badge:
    text: Partiel
    variant: caution
---

Un seul listener, deux surfaces d'observation : `/metrics` (OpenMetrics) et les
journaux JSON structurés sur la sortie standard. Cette page liste ce que la
série v0.5.x actuelle expose réellement, extrait de `internal/metrics` et
`internal/logging`.

## Métriques

:::caution[Pas encore contractuel]
La liste de métriques ci-dessous est réelle et exacte pour la série actuelle,
mais ce n'est **pas un contrat de stabilité** avant la livraison du kit de
supervision (R-11, jalon 7) — les noms et les labels peuvent encore changer
entre versions mineures. Les clés stables du schéma de journal et le schéma
d'audit sont contractuels dès aujourd'hui ; les métriques ne le sont pas
encore. Suivez-le sur la page [état du projet](../../discover/status/).
:::

`GET /metrics` sert l'exposition OpenMetrics (négociation de contenu par-dessus
le format Prometheus classique). C'est l'un des
[préfixes réservés](../../reference/api/) et il n'exige aucune authentification,
comme les sondes.

Métriques de domaine enregistrées aujourd'hui :

| Métrique | Type | Labels | Signification |
|---|---|---|---|
| `tobby_build_info` | gauge | `version`, `commit` | Métadonnées de build du binaire en cours d'exécution ; la valeur est toujours 1. |
| `tobby_sync_transfers_inflight` | gauge | — | Transferts d'ingrédients actuellement en cours, bornés par `sync.parallelism`. |
| `tobby_sync_transferred_bytes_total` | counter | — | Octets transférés par les synchronisations de recipes (le contenu déjà à jour, ignoré, ne déplace rien). |
| `tobby_policy_rejections_total` | counter | `code` | Transferts refusés par la politique, par code de taxonomie (la liste d'autorisation aujourd'hui : `TBY-POL-001`). |
| `tobby_promotion_pushes_total` | counter | `result` = `pushed` \| `skipped` | Issues des promotions. `skipped` est le signal sain : une promotion stabilisée entre deux zones n'est presque que des sauts — « la destination est au niveau que demande le Retriever » comme assertion de tableau de bord. Les deux valeurs de label existent dès le premier scrape. |
| `tobby_promotion_pushed_bytes_total` | counter | — | Octets que la promotion a réellement déplacés ; une recipe déjà synchronisée n'ajoute rien (le différentiel, observé plutôt que supposé). |
| `tobby_promotion_refusals_total` | counter | `code` | Pushes refusés avant transfert, par code de taxonomie (`TBY-POL-001` liste d'autorisation, `TBY-SIG-001` revérification de signature avant push, `TBY-DST-001` limites de la destination). |
| `tobby_store_occupancy_bytes` | gauge | — | Taille sur disque de l'arbre de blobs dédupliqués du store au dernier échantillon ; `0` tant qu'aucun `storage.occupancyThreshold` n'est configuré et que rien n'est échantillonné. |
| `tobby_store_occupancy_threshold_bytes` | gauge | — | Le seuil configuré ; `0` signifie qu'aucun seuil n'est défini et que le store n'est pas surveillé. |
| `tobby_store_occupancy_exceeded` | gauge | — | `1` tant que le store dépasse son seuil, `0` sinon. Une gauge et non un counter, volontairement : lever la condition doit être aussi visible que la poser. Les trois existent dès le premier scrape. |
| `tobby_scan_items_total` | counter | `verdict` = `pass` \| `warn` \| `block` \| `not-scanned` | Éléments analysés à la récupération, par verdict (FR-031). Les quatre séries existent dès le premier scrape. |
| `tobby_scan_findings_total` | counter | `severity` = `UNKNOWN` \| `LOW` \| `MEDIUM` \| `HIGH` \| `CRITICAL` | Constats rapportés par les analyses d'éléments. |
| `tobby_scan_policy_blocking` | gauge | — | `1` quand la politique bloque (`scan.mode: block`, ou un seuil posé par le Retriever servi), `0` quand elle est consultative — le bandeau consultatif, sous forme de métrique. |
| `tobby_scan_scanner_available` | gauge | — | `1` quand un binaire d'analyseur vérifié est utilisable, `0` en mode dégradé. |
| `tobby_scan_db_present` | gauge | — | `1` quand une base de vulnérabilités est présente, `0` dans le mode dégradé d'absence de base. |
| `tobby_scan_db_age_seconds` | gauge | — | Âge de la base depuis l'horodatage de construction qu'elle enregistre (R-13). Exposée **uniquement tant qu'une base est présente** : `tobby_scan_db_present` distingue « absente » de « fraîche ». |
| `tobby_scan_db_stale` | gauge | — | `1` tant que l'âge dépasse `scan.maxDbAge` ; exposée avec l'âge. |
| `tobby_scan_exceptions` | gauge | `state` = `active` \| `expired`, `source` = `instance` \| `retriever` \| `vex` | Exceptions de vulnérabilités en configuration (R-15) : les `scan.exceptions` de l'instance, celles issues des documents OpenVEX de `scan.vex`, et celles du dernier Retriever servi (aucune comptée avant la première exécution). Une exception de Retriever sans limite compte comme `active` — elle s'applique. Calculée à chaque collecte à partir de la configuration et de l'horloge : une échéance qui passe sur une instance en marche fait passer une série d'`active` à `expired` sans redémarrage. Les six séries existent dès la première collecte. |
| `tobby_scan_reblocked_items_total` | counter | — | Éléments refusés par une politique bloquante parce qu'une exception qui couvrait l'un de leurs constats en violation est échue (R-15) — chacun aussi un enregistrement d'audit `scan.exception_expired`. À zéro dès la première collecte. |
| `tobby_integrity_verifications_total` | counter | `verdict` = `clean` \| `defects` | Vérifications du store à la demande terminées ([`tobby store verify`](../cli/#tobby-store-verify), l'écran d'intégrité du store, `POST /api/v1/store/verify`). Du contenu orphelin seul ne rend jamais un verdict `defects`. Les deux verdicts existent dès le premier scrape, à zéro. |
| `tobby_integrity_last_verification_timestamp_seconds` | gauge | — | Heure de fin de la dernière vérification terminée, en secondes Unix. **Absente tant qu'aucune n'est terminée** : une valeur `0` se lirait comme 1970, un nombre là où la vérité est « jamais vérifié » — alertez sur l'absence, ou sur l'âge. |
| `tobby_assurance_reduced_recipes` | gauge | `scope` | Recipes actuellement détenues en [assurance réduite](../../security/reduced-assurance/), par périmètre de confiance déclaré qui les a admises. Calculée à chaque scrape à partir du graphe de recipes, jamais comptée par événements. Chaque périmètre qui autorise des recipes non signées est présent dès le premier scrape, à zéro tant qu'il ne détient rien, de sorte que retirer la dernière est aussi visible qu'admettre la première. |
| `tobby_store_unmanaged_repositories` | gauge | `class` = `unit-import` \| `seed` | Dépôts du store **qu'aucune recipe ne gère** (FR-098), par classe de provenance : `unit-import` compte les imports unitaires et les FileSets empaquetées sur cette instance, `seed` le contenu poussé par `/v2/` (et les dépôts qu'un store écrit avant la v1.0 n'a jamais classés). Le contenu géré par recipe — assurance réduite comprise, comptée par `tobby_assurance_reduced_recipes` — est le côté géré et n'est pas compté. Lu dans le store au moment du scrape (au plus toutes les 10 secondes), jamais compté par événements ; les deux classes existent dès le premier scrape, à zéro quand le store n'en détient pas, et la famille est absente d'un scrape qui n'a pas pu lire le store. |
| `tobby_hardening_enabled` | gauge | `setting` = `disableUnitImport` \| `disableRegistryPush` | `1` pour chaque [interrupteur de durcissement](../configuration/#durcissement) activé, `0` sinon. Les deux réglages, toujours : « désactivé » est une valeur qu'une alerte peut lire, jamais une absence. |
| `tobby_auth_attempts_total` | counter | `method` = `basic` \| `token` \| `oidc` \| `saml`, `outcome` = `success` \| `failure` \| `expired` | Contrôles d'identifiants sur les surfaces machine et connexions sur l'interface (R-14). `expired` est un token refusé après son expiration. Toutes les séries existent dès le premier scrape. |
| `tobby_auth_sessions_expired_total` | counter | `reason` = `idle` \| `lifetime` | Sessions de l'interface terminées par `auth.session.idleTimeout` ou `auth.sessionTTL` (R-14), chacune étant aussi un enregistrement d'audit `session.expire`. |
| `tobby_auth_idp_reachable` | gauge | `method` = `oidc` \| `saml` | `1` tant que le fournisseur d'identité répond à la sonde d'arrière-plan, `0` tant qu'il ne répond pas (R-20). Absente tant qu'il n'a pas été sondé, et absente pour SAML avec métadonnées épinglées, qui n'a besoin d'aucun appel de serveur à serveur. |
| `tobby_auth_local_logins_under_federation_total` | counter | — | Connexions par compte local alors que OIDC ou SAML est activé — usage de secours (break-glass), chacune étant aussi un enregistrement d'audit `auth.local_under_federation` (FR-078). |

Les gauges d'analyse sont calculées au moment du scrape à partir de l'état de
l'analyseur, de sorte que l'âge évolue de lui-même entre deux analyses.

Les collecteurs standard du runtime Go et du processus sont enregistrés en plus.

La discipline des labels est délibérée : les métriques de refus sont étiquetées
par **code de taxonomie**, jamais par hôte — une métrique étiquetée par un hôte
fourni par un attaquant est un trou de cardinalité non bornée. L'hôte figure
dans l'enregistrement de journal, là où il doit être.

## Journaux JSON

L'instance journalise du JSON structuré sur la **sortie standard**, un objet
par ligne (`log/slog`, sans framework tiers). Le niveau est fixé par
`logging.level` (`info` par défaut).

**En mode mirror, les mêmes enregistrements vont plutôt dans un fichier sur le
support de transport** — `_tobby/logs/operations.log` par défaut — parce que le
support est le canal d'audit du transfert et que celui qui le reçoit n'a pas
accès à la sortie standard du poste. Même schéma, mêmes clés, plus un contrat
de durabilité : fsync à chaque frontière de tâche et rotation par taille. Voir
[tracer un transfert](../../air-gap/traceability/).

Clés stables sur chaque enregistrement :

| Clé | Contenu |
|---|---|
| `ts` | Horodatage RFC 3339, UTC, précision à la nanoseconde. |
| `level` | `DEBUG`, `INFO`, `WARN`, `ERROR`. |
| `msg` | L'événement, une phrase anglaise stable. |

**Clés de corrélation** stables, ajoutées à mesure que le travail se précise —
ces noms sont fixés par le schéma de journal et on peut bâtir dessus des règles
d'extraction :

| Clé | Contenu |
|---|---|
| `run_id` | Identifie une exécution de synchronisation de bout en bout. Il traverse l'air gap dans le manifeste du média et est réutilisé par l'instance de destination (voir [Tracer et prouver un transfert](../../air-gap/traceability/)). |
| `task_id` | Une tâche suivie. Affiché comme identifiant de corrélation à côté des erreurs de la taxonomie, de sorte qu'une erreur à l'écran retrouve ses enregistrements de journal. |
| `recipe` | La recipe en cours de traitement. |
| `ingredient` | La référence de l'ingrédient. |
| `digest` | Le digest du contenu concerné. |
| `assurance` | Présent **uniquement** sur les enregistrements concernant du contenu admis sous un périmètre de confiance assoupli, avec la valeur `reduced` ([assurance réduite](../../security/reduced-assurance/)). Filtrez sur sa présence pour les retrouver. |
| `assurance_scope` | Le périmètre de confiance déclaré derrière `assurance`, sur les mêmes enregistrements et seulement là. |
| `provenance` | La [classe de provenance](../../security/model/#interrupteurs-de-durcissement) du dépôt que concerne l'enregistrement — `recipe`, `unit-import` ou `seed` — sur l'enregistrement d'import unitaire (`unit import recorded`), les enregistrements de push et de suppression de manifeste de la registry embarquée (`manifest pushed`, `manifest deleted`), celui d'empaquetage de FileSet (`fileset packed`) et celui de retrait (`repository removed`). |

Les autres champs sont contextuels à chaque message (de nombreuses lignes
notables portent un champ `requirement` qui nomme l'exigence du SRS qu'elles
mettent en œuvre) et ne font pas partie du schéma stable.

Chaque tâche conserve aussi son propre fichier de journal dans le store, à côté
de son enregistrement de tâche — c'est ce que servent les écrans de détail de
tâche et `GET /api/v1/tasks/{id}/logs`, et la rétention des tâches terminées
(`tasks.keepFinished`) purge les deux ensemble.

Les événements d'audit circulent dans le même flux, avec leur propre schéma
versionné — voir [Journal d'audit](../../security/audit-log/).

## Sur quoi alerter aujourd'hui

Avec les moyens de la série actuelle :

- **`readyz`** — la disponibilité passe à 503 pendant le démarrage et la
  vidange ; un 503 durable désigne une instance qui ne peut pas servir.
- **`tobby_store_occupancy_exceeded == 1`** — le store a dépassé le seuil fixé
  par son opérateur. Rien ne borne ce qu'une instance passthrough sans
  surveillance accumule tant que `sync.prune` n'est pas activé : c'est l'alerte
  qui se tient entre « ça grossit » et « le volume est plein et les écritures
  échouent ». Elle retombe d'elle-même à `0` dès que le store repasse sous le
  seuil.
- **`rate(tobby_promotion_refusals_total[…]) > 0`** — un refus de promotion
  n'est jamais anodin : c'est une violation de la liste d'autorisation, une
  signature qui ne se vérifie plus, ou une limite de la destination. Le label
  `code` dit lequel ; les journaux portent l'hôte et la référence.
- **`rate(tobby_policy_rejections_total[…]) > 0`** — même raisonnement côté
  récupération.
- **`tobby_scan_scanner_available == 0` ou `tobby_scan_db_present == 0`** — le
  mode dégradé de l'analyse : le contenu est signalé non analysé, et refusé sous
  une politique bloquante.
- **`tobby_scan_db_stale == 1`** — la base de vulnérabilités n'a pas été mise à
  jour assez récemment ; les constats publiés depuis sa construction sont
  inconnus de l'analyseur.
- **`increase(tobby_scan_reblocked_items_total[…]) > 0`** — une exception est
  échue et un contenu qu'elle admettait est de nouveau refusé : la renouveler
  délibérément ou traiter le constat. **`tobby_scan_exceptions{state="expired"} > 0`**
  le dit avant même la synchronisation suivante.
- **`tobby_auth_idp_reachable == 0`** — le fournisseur d'identité est
  injoignable : les personnes se connectent entre-temps avec le compte local de
  secours (FR-078), ce que montre
  **`increase(tobby_auth_local_logins_under_federation_total[…]) > 0`** — à
  alerter aussi à part entière lorsque le fournisseur est disponible.
- **`rate(tobby_auth_attempts_total{outcome="expired"}[…]) > 0`** — un client
  présente encore un token expiré : remplacez-le avant que ce soit celui qui
  compte.
- **L'absence de mouvement de `tobby_promotion_pushes_total`** (ni `pushed` ni
  `skipped` n'augmente) sur une instance passthrough avec un retriever
  configuré — la boucle de réconciliation a cessé de tourner.
- **Les enregistrements de niveau `ERROR`**, et les enregistrements `WARN` autour
  de la résolution des FileSets et des cycles ignorés (« promotion cycle
  skipped » est une coalescence informative, pas un échec).

:::note[À venir — jalon 7]
Le kit de supervision (R-11 : tableaux de bord, règles d'alerte et contrat de
stabilité des métriques) et le bundle de diagnostic expurgé (R-30) arrivent avec
le jalon 7. D'ici là, l'alerte s'assemble à partir des primitives ci-dessus.
Suivez les deux sur la page [état du projet](../../discover/status/).
:::
