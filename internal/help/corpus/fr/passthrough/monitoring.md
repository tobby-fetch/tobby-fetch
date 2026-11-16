---
title: Superviser une instance
description: Le référentiel des métriques, les règles d'alerte et le tableau de bord livrés avec le chart, et le document OpenAPI signé — sur quoi alerter, et ce que la release publie pour cela.
sidebar:
  order: 11
---

Une instance qui se réconcilie pendant des mois doit être surveillée par autre
chose que la personne qui l'a installée. Cette page décrit le **kit de
supervision** : le référentiel des métriques, des règles d'alerte et un tableau
de bord d'exemple livrés avec le chart de référence, et les deux documents
qu'une release publie, signés, pour qui s'intègre à Tobby.

## Ce que la release publie

Chaque release joint deux fichiers, chacun avec un bundle cosign à côté, signés
sans clé comme le sont les SBOM :

| Fichier | Ce que c'est |
|---|---|
| `openapi.yaml` | Le document OpenAPI 3.1 de l'API REST — identique à l'octet près à ce qu'une instance en marche sert à `/api/v1/openapi.yaml`. |
| `metrics-reference.json` | Chaque famille `tobby_*` : type, aide, labels avec leurs ensembles de valeurs fermés, unité, l'exigence servie, et quand elle est présente. |

```sh
cosign verify-blob --new-bundle-format \
  --bundle metrics-reference.json.bundle \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/tobby-fetch/tobby-fetch/' \
  metrics-reference.json
```

Le référentiel est **généré à partir du registre en marche** (`go run
./tools/metricsref`), et un test échoue si une famille existe que le référentiel
ne liste pas, ou l'inverse — il ne peut pas diverger de l'endpoint. Renommer ou
retirer une famille est un changement cassant sous la promesse 1.x ; en ajouter
une ne l'est pas. Les familles standard `go_*` et `process_*` de la bibliothèque
cliente Prometheus en sont exclues : leur ensemble dépend de la version de Go et
de la plateforme.

## Familles ajoutées pour la supervision

| Famille | Sens |
|---|---|
| `tobby_sync_runs_total{outcome}` | Exécutions de synchronisation, `success` ou `failure` (une exécution en échec, ou qui a laissé un item en échec). |
| `tobby_sync_duration_seconds{outcome}` | Leur durée, en histogramme. |
| `tobby_sync_last_success_timestamp_seconds` | Fin de la dernière exécution réussie. Absente tant qu'aucune n'a réussi depuis le démarrage du processus. |
| `tobby_sync_ingredients{status}` | Ingrédients de la dernière exécution : `new`, `outdated`, `up_to_date`. |
| `tobby_sync_parallelism` | La borne configurée de transferts concurrents, à comparer à `tobby_sync_transfers_inflight`. |
| `tobby_server_certificate_expiry_timestamp_seconds` | `NotAfter` du certificat que le listener présente, repli auto-signé compris ; lu à chaque collecte, donc un remplacement est suivi. Absente en HTTP clair. |
| `tobby_clock_implausible{anchor}` | 1 tant que l'[horloge](../../air-gap/clock-check/) est trouvée antérieure au build ou à un support. |
| `tobby_diagnostics_bundles_total{outcome}` | [Bundles de diagnostic](../diagnostics/) par issue. |

Une famille est exposée dès la première collecte **avec une valeur vraie, ou pas
du tout** : une jauge d'horodatage qui n'a rien mesuré est absente, jamais à
`0`, car `0` dirait « le dernier succès date de 1970 ». Une alerte de péremption
distingue donc *jamais* de *il y a longtemps* par la présence.

## Les règles d'alerte

Le chart de référence livre ces règles, **désactivées par défaut** et activées par
une valeur chacune, sous deux formes : une ressource `PrometheusRule`, et les
mêmes règles en fichier de règles Prometheus brut.

```sh
helm upgrade tobby oci://ghcr.io/tobby-fetch/charts/tobby --version <version> \
  --namespace tobby --reuse-values \
  --set alerting.prometheusRule.enabled=true \
  --set alerting.prometheusRule.labels.release=<votre release Prometheus> \
  --set dashboard.enabled=true
```

`alerting.rulesFile.enabled=true` rend le fichier brut dans une ConfigMap
(`tobby-alerts.rules.yaml`) pour un Prometheus qui n'est pas piloté par
l'Operator ; le même fichier, aux seuils par défaut, est gardé dans le dépôt à
`deploy/charts/tobby/files/tobby-alerts.rules.yaml` et est généré à partir du
chart, jamais édité.

| Alerte | Se déclenche quand | Borne par défaut (`alerting.thresholds.*`) |
|---|---|---|
| `TobbySyncFailing` | Au moins `syncFailures` exécutions en échec dans `syncFailuresWindow`. | 3 en `1h` |
| `TobbySyncStale` | Aucune synchronisation réussie depuis plus de `syncMaxAgeSeconds`. | `21600` (6 h) ; `0` retire la règle |
| `TobbySyncNeverSucceeded` | Des exécutions ont échoué et aucune n'a jamais réussi depuis le démarrage du processus. | pendant `syncFailuresWindow` |
| `TobbySyncSaturated` | Transferts collés à `sync.parallelism`. | pendant `saturationFor`, `15m` |
| `TobbyStoreOccupancyHigh` | Le store dépasse `storage.occupancyThreshold`. | pendant `storeOccupancyFor`, `15m` |
| `TobbyVulnerabilityDatabaseStale` | La base est plus ancienne que `scan.maxDbAge`. | pendant `scannerFor`, `1h` |
| `TobbyVulnerabilityDatabaseAbsent` | Pas de base de vulnérabilités. | pendant `scannerFor`, `1h` |
| `TobbyScannerDegraded` | Aucun scanner vérifié utilisable. | pendant `scannerFor`, `1h` |
| `TobbyServerCertificateExpiring` | Le certificat présenté expire dans `certificateExpirySeconds`, ou a déjà expiré. | `1209600` (14 j), pendant `certificateExpiryFor`, `10m` |
| `TobbyClockImplausible` | L'horloge est antérieure au build ou à un support. | pendant `clockFor`, `5m` |

Quelques notes honnêtes. Les **instances miroir** synchronisent quand un
opérateur le décide : une borne sur l'âge du dernier succès n'alerterait
personne utilement, mettez donc `alerting.thresholds.syncMaxAgeSeconds` à `0`.
Les règles sont des **points de départ, non réglés pour votre site** : observez
ce que fait votre instance pendant une semaine, puis déplacez les bornes.
`/metrics` est derrière la même authentification que le reste ; la collecte a
donc besoin d'identifiants (`metrics.serviceMonitor.basicAuth`).

Chaque expression ne nomme que des familles du référentiel publié, et les labels
sur lesquels elle sélectionne sont dans les ensembles fermés que le référentiel
documente ; un test tient le fichier livré à cela. Chaque règle a aussi un test
unitaire `promtool` qui lui fournit la condition pour laquelle elle existe et
celle pour laquelle elle ne doit pas se déclencher
(`deploy/charts/tobby/tests/tobby-alerts_test.yaml`) :

```sh
promtool test rules deploy/charts/tobby/tests/tobby-alerts_test.yaml
```

## Le tableau de bord

`dashboard.enabled=true` rend une ConfigMap portant le label que le sidecar
Grafana surveille (`dashboard.labels`). C'est un **exemple** : exécutions par
issue, âge du dernier succès, durée, ingrédients de la dernière exécution,
transferts face à la borne, octets, occupation du store, temps restant du
certificat, base de vulnérabilités, horloge, refus par code, connexions et
bundles. Le JSON est `deploy/charts/tobby/files/tobby-dashboard.json` ;
importez-le à la main si vous n'avez pas le sidecar.
