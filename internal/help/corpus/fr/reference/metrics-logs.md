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

:::note[Un contrat depuis la version 1.0]
Les familles `tobby_*` ci-dessous sont versionnées, comme l'API REST : en
renommer ou en retirer une est un changement cassant, en ajouter une ne l'est
pas. Le référentiel `metrics-reference.json` — chaque famille, son type, ses
labels et leurs ensembles de valeurs fermés, son unité, l'exigence qu'elle sert
et quand elle est présente — est **généré à partir du registre en marche** et
publié, signé, avec chaque release ; un test échoue quand lui et l'endpoint
divergent. Voir [Superviser une instance](../../passthrough/monitoring/).
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
| `tobby_sync_runs_total` | counter | `outcome` = `success` \| `failure` | Exécutions de synchronisation (déclenchement miroir ou cycle passthrough). Une exécution échoue quand elle a produit une erreur ou laissé un item en échec. Les deux séries existent dès la première collecte. |
| `tobby_sync_duration_seconds` | histogram | `outcome` = `success` \| `failure` | Durée des exécutions de synchronisation, en secondes. |
| `tobby_sync_last_success_timestamp_seconds` | gauge | — | Fin de la dernière exécution réussie, en secondes Unix. **Absente tant qu'aucune n'a réussi** depuis le démarrage du processus. |
| `tobby_sync_ingredients` | gauge | `status` = `new` \| `outdated` \| `up_to_date` | Ingrédients de la dernière exécution par statut (FR-026). Absente tant qu'aucune exécution n'est terminée. |
| `tobby_sync_parallelism` | gauge | — | Le `sync.parallelism` configuré, à comparer à `tobby_sync_transfers_inflight`. |
| `tobby_server_certificate_expiry_timestamp_seconds` | gauge | — | `NotAfter` du certificat que le listener présente (FR-082), repli auto-signé compris. Lu à chaque collecte, donc un remplacement est suivi ; absente tant que le listener sert du HTTP clair. |
| `tobby_clock_implausible` | gauge | `anchor` = `build` \| `medium` | `1` tant que l'horloge locale est trouvée antérieure à l'ancre au-delà de `clock.skewTolerance`, `0` sinon (FR-096). `build` est contrôlée au démarrage, puis toutes les cinq minutes ; `medium` est jugée contre l'heure de résolution la plus récente des supports ouverts depuis le démarrage (un support bloqué exclu), et est absente tant qu'aucun support n'a été ouvert. |
| `tobby_diagnostics_bundles_total` | counter | `outcome` = `written` \| `refused` \| `failed` | [Bundles de diagnostic](../../passthrough/diagnostics/) : écrits, refusés par le contrôle d'expurgation (`TBY-DIA-002`), ou en échec (`TBY-DIA-001`). Les trois existent dès la première collecte. |
| `tobby_integrity_verifications_total` | counter | `verdict` = `clean` \| `defects` | Vérifications du store à la demande terminées ([`tobby store verify`](../cli/#tobby-store-verify), l'écran d'intégrité du store, `POST /api/v1/store/verify`). Du contenu orphelin seul ne rend jamais un verdict `defects`. Les deux verdicts existent dès le premier scrape, à zéro. |
| `tobby_integrity_last_verification_timestamp_seconds` | gauge | — | Heure de fin de la dernière vérification terminée, en secondes Unix. **Absente tant qu'aucune n'est terminée** : une valeur `0` se lirait comme 1970, un nombre là où la vérité est « jamais vérifié » — alertez sur l'absence, ou sur l'âge. |
| `tobby_assurance_reduced_recipes` | gauge | `scope` | Recipes actuellement détenues en [assurance réduite](../../security/reduced-assurance/), par périmètre de confiance déclaré qui les a admises. Calculée à chaque scrape à partir du graphe de recipes, jamais comptée par événements. Chaque périmètre qui autorise des recipes non signées est présent dès le premier scrape, à zéro tant qu'il ne détient rien, de sorte que retirer la dernière est aussi visible qu'admettre la première. |
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

Les autres champs sont contextuels à chaque message (de nombreuses lignes
notables portent un champ `requirement` qui nomme l'exigence du SRS qu'elles
mettent en œuvre) et ne font pas partie du schéma stable.

Chaque tâche conserve aussi son propre fichier de journal dans le store, à côté
de son enregistrement de tâche — c'est ce que servent les écrans de détail de
tâche et `GET /api/v1/tasks/{id}/logs`, et la rétention des tâches terminées
(`tasks.keepFinished`) purge les deux ensemble.

Les événements d'audit circulent dans le même flux, avec leur propre schéma
versionné — voir [Journal d'audit](../../security/audit-log/).

## Sur quoi alerter

Le chart de référence livre les règles des conditions qui comptent le plus —
échecs répétés, absence de succès récent, saturation, occupation du store, base
de vulnérabilités et scanner, certificat, horloge — et un tableau de bord
d'exemple : voir [Superviser une instance](../../passthrough/monitoring/). Les
signaux qui les fondent, et quelques autres :

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
- **`tobby_auth_idp_reachable == 0`** — le fournisseur d'identité est
  injoignable : les personnes se connectent entre-temps avec le compte local de
  secours (FR-078), ce que montre
  **`increase(tobby_auth_local_logins_under_federation_total[…]) > 0`** — à
  alerter aussi à part entière lorsque le fournisseur est disponible.
- **`rate(tobby_auth_attempts_total{outcome="expired"}[…]) > 0`** — un client
  présente encore un token expiré : remplacez-le avant que ce soit celui qui
  compte.
- **`increase(tobby_sync_runs_total{outcome="failure"}[…])`** et
  **`time() - tobby_sync_last_success_timestamp_seconds`** — des échecs répétés,
  et un dernier succès trop ancien. L'horodatage est absent tant qu'une
  exécution n'a pas réussi : cette absence veut dire « jamais depuis le
  démarrage ».
- **`tobby_server_certificate_expiry_timestamp_seconds - time()`** — le temps
  qui reste au certificat du listener.
- **`tobby_clock_implausible == 1`** — l'horloge est antérieure à une date que
  cette instance prouve révolue ; voir [Détecter une horloge fausse](../../air-gap/clock-check/).
- **L'absence de mouvement de `tobby_promotion_pushes_total`** (ni `pushed` ni
  `skipped` n'augmente) sur une instance passthrough avec un retriever
  configuré — la boucle de réconciliation a cessé de tourner.
- **Les enregistrements de niveau `ERROR`**, et les enregistrements `WARN` autour
  de la résolution des FileSets et des cycles ignorés (« promotion cycle
  skipped » est une coalescence informative, pas un échec).
