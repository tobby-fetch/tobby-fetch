---
title: Référence de l'API
description: Les principes de /api/v1, le contrat OpenAPI servi par l'instance, l'authentification des appels d'API, la stricte parité avec l'UI et les préfixes de chemin réservés.
sidebar:
  order: 3
---

Une instance Tobby sert une API REST versionnée sous `/api/v1`, sur le même
listener unique que l'UI, la registry embarquée et les sondes. Cette page
énonce les principes et les surfaces ; le contrat normatif, point d'accès par
point d'accès, est le document OpenAPI que l'instance sert elle-même.

## Principes

- **Chemin versionné.** Tout vit sous `/api/v1`. La version du chemin est la
  majeure de l'API ; la version de build du binaire figure sur `/about` et dans
  la métrique `tobby_build_info`.
- **Charges utiles pour machines.** Les réponses ne portent que des valeurs
  brutes et des horodatages RFC 3339. La localisation relève de l'UI : l'API ne
  renvoie jamais une phrase traduite là où une valeur stable est attendue.
- **Une seule taxonomie d'erreurs.** Toute erreur est une entrée de la
  [taxonomie TBY-\*](../../reference/errors/) rendue en document « problem »
  RFC 9457. Les membres d'extension `code`, `probable_cause`, `action` et
  `correlation_id` viennent du même catalogue que celui que rendent l'UI et la
  CLI ; le membre `type` est l'ancre de dépannage interne à l'instance
  `/help#<code>`, résoluble hors ligne. La langue du document « problem » suit
  l'en-tête `Accept-Language`.
- **Un chemin d'API inconnu répond par un document « problem »**, jamais par du
  HTML.

## Le contrat OpenAPI, servi par l'instance

Le document OpenAPI 3.1 est embarqué dans le binaire et servi par l'instance
elle-même :

```
GET /api/v1/openapi.yaml            # le document brut (rôle : viewer)
GET /api/v1/cli-output.schema.json  # les schémas du --output json de la CLI (rôle : viewer)
GET /api-docs                       # la page de visualisation intégrée (tout rôle connecté)
```

Le second document est l'autre moitié du contrat machine : le JSON Schema de ce
qu'écrit `tobby <command> --output json` (SRS FR-066, amendement R-08), une
entrée par commande qui produit un rapport. Il est publié à côté du document
OpenAPI, et servi par la même instance, parce qu'une automatisation qui pilote
Tobby utilise les deux — voir la
[référence de la CLI](../../reference/cli/#format-de-rapport).

Chaque release publie aussi `openapi.yaml`, signé, identique à l'octet près à ce
que l'instance sert (voir [Superviser une instance](../../passthrough/monitoring/)).

Un test à la compilation recoupe le document avec les routes enregistrées : un
point d'accès ne peut pas être livré sans documentation, et le document ne
peut pas décrire un point d'accès qui n'existe pas. Ce que sert votre instance
est donc le contrat faisant foi pour la version exacte que vous exécutez — y
compris hors ligne.

## Authentification

Rien sur l'API n'est anonyme (seules les sondes le sont). Deux schémas
d'identifiants, contre les mêmes comptes et tokens que toutes les autres
surfaces :

- **Basic** — `account:password`. Un secret de token est aussi accepté comme
  mot de passe Basic, si bien que `docker login` et `helm registry login`
  fonctionnent avec des tokens.
- **Bearer** — un token d'API statique (`Authorization: Bearer <secret>`). Les
  tokens sont limités à un rôle, révocables, et stockés hachés. Ils se gèrent
  sur `/api/v1/tokens` et l'écran correspondant de l'UI.

Un cookie de session d'UI valide fonctionne aussi pour les points d'accès en
**lecture** — c'est ce qui permet de « copier l'URL que l'on regarde » depuis un
navigateur. Les appels qui modifient exigent toujours Basic ou Bearer. Chaque
point d'accès est placé derrière un rôle minimal documenté (viewer, operator ou
admin) ; la matrice publiée et le test anti-dérive qui la fait respecter sont
décrits dans [Authentification, comptes et RBAC](../../security/auth-rbac/). Une
origine qui échoue à répétition à s'authentifier est ralentie par un `429`
([`TBY-AUTH-012`](../../reference/errors/#tby-auth-012)).

## La parité UI ↔ API est une fonctionnalité

L'API est le miroir strict de l'UI web (SRS FR-061) : chaque action de l'UI a
son point d'accès miroir, et **les filtres et la recherche partagent
exactement les mêmes paramètres**. Le champ de recherche de l'écran de
contenu, le filtre de type et le préfixe de chemin sont littéralement `q`,
`kind` et `prefix` sur `GET /api/v1/content` — copier l'URL de l'écran que l'on
regarde *est* l'appel d'API, moins le rendu. Les mêmes erreurs de la taxonomie
reviennent, sous forme de documents « problem » au lieu de fragments HTML.

La surface actuelle (d'après le document OpenAPI servi) :

| Domaine | Points d'accès |
|---|---|
| Contrat | `GET /api/v1/openapi.yaml`, `GET /api/v1/cli-output.schema.json` |
| Contenu | `GET /api/v1/content` (recherche, filtres, pagination), `GET /api/v1/content/{repo}`, `GET /api/v1/content/{repo}/-/tags/{tag}`, `DELETE /api/v1/content/{repo}` |
| Import unitaire | `GET /api/v1/import/inspect`, `POST /api/v1/import` |
| Tâches | `GET /api/v1/tasks`, `GET /api/v1/tasks/{id}`, `GET /api/v1/tasks/{id}/logs` |
| Recipes et synchronisation | `GET /api/v1/recipes`, `GET /api/v1/recipes/{recipe}/mapping` (`?format=csv|json|markdown` pour la table exportable), `GET /api/v1/mapping`, `GET /api/v1/registries.yaml`, `GET /api/v1/recipes/{recipe}/registries.yaml`, `POST /api/v1/recipes/publish`, `POST /api/v1/sync`, `GET /api/v1/sync/prune-preview`, `GET /api/v1/retriever`, `PUT/DELETE /api/v1/retriever/interval` |
| Plan (sans effet de bord) | `POST /api/v1/plan` |
| Parcours du média | `GET /api/v1/media`, `GET /api/v1/media/verification`, `GET /api/v1/media/slip`, `POST /api/v1/media/verify`, `POST /api/v1/media/import` |
| FileSets | `GET /api/v1/filesets`, `POST /api/v1/filesets/pack` |
| Interopérabilité et store | `POST /api/v1/oci-layout/plan`, `POST /api/v1/oci-layout/export`, `POST /api/v1/oci-layout/import`, `POST /api/v1/store/reset` |
| Intégrité du store | `POST /api/v1/store/verify`, `POST /api/v1/store/verify/cancel`, `GET /api/v1/store/verification` |
| Comptes et tokens | `POST /api/v1/account/password`, `GET/POST /api/v1/accounts`, `PATCH/DELETE /api/v1/accounts/{name}`, `GET/POST /api/v1/tokens`, `POST /api/v1/tokens/{name}/revoke` |
| Réseau | `GET /api/v1/network`, `PUT /api/v1/network/certificate` |
| Exploitation | `POST /api/v1/diagnostics/bundle`, `GET /api/v1/clock`, `GET /api/v1/state` |

Quatre d'entre eux méritent une remarque, car leur forme n'est pas celle que
le chemin laisserait deviner.

- **`POST /api/v1/plan` répond `200` quelle que soit l'issue.** Un plan est un
  rapport, pas une tentative : le corps est `{"plan": …, "exit_code": …}`, et le
  membre `exit_code` est le nombre que
  [`tobby sync --dry-run`](../../reference/cli/#tobby-sync) aurait renvoyé.
  Branchez-vous sur lui, pas sur le statut.
- **Le parcours du média est fait de quatre points d'accès, pas d'une machine
  à états.** Le *rapport* est le corps que renvoie `POST /api/v1/media/verify`
  — lui aussi `200` quel que soit le verdict — et le *push* est
  `POST /api/v1/media/import`. `GET /api/v1/media/verification` est l'état
  interrogeable : la barrière de mise en service, la vérification en cours avec
  son étape et ses compteurs d'octets, et le dernier rapport. Demander une
  seconde vérification pendant qu'une autre parcourt le média répond `409`
  ([`TBY-MED-031`](../../reference/errors/#tby-med-031)).
  `GET /api/v1/media/slip` est le [bordereau de transfert](../../air-gap/transfer-slip/) :
  le contenu de l'écran sous forme de document imprimable `html`, `text` ou
  `json`, en anglais ou en français, non signé et le disant ; il ne hache rien.
- **Une vérification de store est une tâche, et son rapport est un
  document.** `POST /api/v1/store/verify` répond `201` avec la tâche, et une
  seconde requête pendant qu'une autre est en attente ou en cours répond `409`
  ([`TBY-INT-004`](../../reference/errors/#tby-int-004)).
  `GET /api/v1/store/verification` sert le dernier rapport en entier — ses
  horodatages, son identifiant de run, son verdict et chaque défaut sous forme
  de code de la taxonomie désignant le fichier ou l'objet concerné — ou un
  instantané ne portant que la progression tant qu'elle tourne ;
  `verification` vaut `null` avant qu'une vérification ait été demandée. Un
  store présentant des défauts est une tâche *échouée* qui porte son code
  principal. Voir [Vérifier l'intégrité du store](../../passthrough/verify-store/).
- **Deux membres du corps de requête du média exigent le rôle `admin`**, au-dessus
  du plancher operator du point d'accès lui-même : `allowZoneMismatch` et
  `allowStale`, les deux garde-fous levables. Envoyer l'un des deux depuis un
  rôle inférieur donne [`TBY-AUTH-003`](../../reference/errors/#tby-auth-003), et
  la tentative comme la levée appliquée sont consignées dans le journal d'audit.
  Rien ne lève un verdict d'intégrité ou de signature, quel que soit le rôle.

- **`POST /api/v1/diagnostics/bundle` répond avec l'archive elle-même**
  (`application/gzip`, `Content-Disposition: attachment`), pour l'administrateur
  seulement. Elle est construite et contrôlée avant le premier octet envoyé :
  un bundle qui porterait un secret est un document problème
  ([`TBY-DIA-002`](../../reference/errors/#tby-dia-002)), jamais un
  téléchargement partiel ; l'archive contrôlée est ensuite diffusée en flux,
  sans `Content-Length`. Un seul bundle est construit à la fois : une demande
  concurrente reçoit 409 ([`TBY-DIA-003`](../../reference/errors/#tby-dia-003)).
  La demande est auditée. Voir
  [Collecter un bundle de diagnostic](../../passthrough/diagnostics/).
- **`GET /api/v1/clock` et `GET /api/v1/state` sont des rapports.** Le premier
  renvoie le dernier [verdict d'horloge](../../air-gap/clock-check/) de chaque
  ancre, à tout rôle ; le second l'inventaire du répertoire d'état, noms, tailles
  et permissions, aux administrateurs.

Sur une instance qui n'est pas le côté destination d'un transfert physique,
les quatre points d'accès du média répondent
[`TBY-CFG-001`](../../reference/errors/#tby-cfg-001) en nommant le réglage
`zone:` manquant, plutôt que de prétendre n'avoir rien à montrer.

Le segment `/-/` est le séparateur déterministe entre un chemin de dépôt — qui
peut lui-même contenir des barres obliques — et sa sous-ressource (`tags`,
`delete`). Il apparaît aussi bien dans les URL de l'UI que dans les chemins de
l'API.

## Préfixes de chemin réservés

L'UI web possède la racine du listener ; chaque surface machine garde un
préfixe réservé. La liste fait partie du contrat produit (affichée sur
`/about`, imposée par un test de collision) :

| Préfixe | Surface |
|---|---|
| `/v2/` | La registry OCI embarquée — API Distribution standard pour `docker`, `helm`, `oras`, avec les mêmes comptes et tokens. |
| `/api/` | L'API REST décrite ci-dessus. |
| `/metrics` | Point d'accès OpenMetrics — voir [Métriques et journaux](../../reference/metrics-logs/). |
| `/healthz`, `/readyz` | Sondes de vivacité et de disponibilité, les seules surfaces anonymes. |
| `/files/` | Service HTTP des FileSets (dépôts de paquets d'OS) — authentification Basic pour que les identifiants dans l'URL d'`apt`/`dnf` fonctionnent ; accès anonyme possible par FileSet, sur option. |
| `/auth/` | Réservé en amont des rappels OIDC/SAML du jalon 6. |
| `/static/` | Ressources d'UI embarquées. |

Tout ce qui est hors de ces préfixes est une route d'UI et peut évoluer
librement ; les préfixes eux-mêmes sont stables.
