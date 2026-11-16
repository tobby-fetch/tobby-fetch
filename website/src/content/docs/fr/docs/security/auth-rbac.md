---
title: Authentification, comptes et RBAC
description: Les méthodes d'authentification et leur état, les trois rôles, la matrice complète rôle par surface et les garde-fous autour de la désactivation de l'authentification.
sidebar:
  order: 4
---

L'authentification est **active par défaut dans les deux modes** (FR-075 —
livrée en v0.4.0). Dès l'installation, une instance utilise l'authentification
basique gérée localement ; une instance sans aucun compte refuse de démarrer
plutôt que de démarrer ouverte.

## Méthodes et état

| Méthode | Cas d'usage | Référence | État |
| --- | --- | --- | --- |
| Authentification basique, comptes locaux | défaut, hôtes isolés | FR-073, ADR-0009 | livrée en v0.4.0 |
| Tokens bearer statiques | automatisation d'API, CI | FR-072, ADR-0009 | livrée en v0.4.0 |
| OIDC (code d'autorisation) | IdP d'entreprise | FR-070, ADR-0009 | livrée en v0.6.0 |
| SAML 2.0 (initié par le SP) | IdP d'entreprise historique | FR-071, ADR-0009 | livrée en v0.6.0 |

Les quatre méthodes coexistent : activer OIDC ou SAML ne désactive jamais les
comptes locaux ni les tokens, et ils continuent de fonctionner lorsque le
fournisseur d'identité est indisponible (FR-078). La configuration,
l'association groupes-rôles, les durées des sessions et des tokens et le
comportement en cas de panne se trouvent dans
[Identité d'entreprise](../enterprise-identity/). La limitation par origine des
tentatives d'authentification échouées a été livrée en v0.4.2 ; l'expiration des
sessions inactives et l'expiration optionnelle des tokens (R-14) avec la v0.6.0.

## Les trois rôles

Un ensemble fermé, ordonné `viewer < operator < admin` ; chaque rôle accorde
tout ce que donnent les rôles inférieurs (FR-074, ADR-0009).

| Rôle | Accorde |
| --- | --- |
| `viewer` | Lire l'instance : contenu, tâches, recipes, aide. Gérer son propre mot de passe. Tirer depuis la registry embarquée. |
| `operator` | + les actions qui font entrer du contenu : import unitaire, déclenchement de synchronisations, publication de recipes, push vers la registry. |
| `admin` | + l'administration : comptes, tokens, suppression de contenu, et les écrans qui révèlent la configuration de l'instance. |

## Rôle minimal par surface

C'est la forme publiée de la matrice appliquée
([`docs/rbac-matrix.md`](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/rbac-matrix.md)).
L'API reflète l'interface action pour action avec des seuils identiques
(FR-061) ; elles partagent donc un seul tableau.

| Action | Route de l'interface | Point d'accès de l'API | Seuil |
| --- | --- | --- | --- |
| Page de connexion, langue, thème | `GET/POST /login`, `POST /lang`, `POST /theme` | — | public (volontairement minimal) |
| Allers-retours de connexion fédérée, descripteur du SP | `/auth/oidc/…`, `/auth/saml/…` (uniquement si activés) | — | public |
| Tableau de bord, tâches, contenu, recipes, correspondances, aide, à propos, documentation de l'API | `GET /`, `/tasks…`, `/content…`, `/recipes…`, `/help`, `/about…`, `/api-docs` | `GET /api/v1/content…`, `/tasks…`, `/recipes…`, `/openapi.yaml` | viewer |
| Changement de son propre mot de passe | `POST /account/password` | `POST /api/v1/account/password` | tout rôle authentifié |
| Import unitaire (et inspection) | `GET/POST /import` | `POST /api/v1/import`, `GET /api/v1/import/inspect` | operator |
| Déclencher une synchronisation | `POST /recipes/sync` | `POST /api/v1/sync` | operator |
| Publier une recipe | `GET/POST /recipes/publish` | `POST /api/v1/recipes/publish` | operator |
| Supprimer du contenu importé à l'unité | `POST /content/{repo}/-/delete` | `DELETE /api/v1/content/{repo}` | admin |
| Comptes, rôles, tokens | `/admin/accounts` + routes `POST` | `/api/v1/accounts…`, `/api/v1/tokens…` | admin |
| Source et intervalle du Retriever | `/admin/retriever…` | `/api/v1/retriever…` | admin |
| Écran réseau, remplacement du certificat | `/admin/network…` | `/api/v1/network…` | admin |

La registry embarquée (`/v2/`) est contrôlée **par méthode HTTP**, non par
chemin, afin que les clients standard fonctionnent sans modification (FR-076) :
`GET`/`HEAD` (pull, catalogue, liste des tags) exigent `viewer` ; tout le reste
(push, envoi de blob, suppression) exige `operator`. Une requête non
authentifiée reçoit un `401` avec un défi `Basic`. `/files/` est en lecture
seule avec un seuil `viewer`, sauf les FileSets explicitement ouverts à l'accès
anonyme pour l'amorçage d'un hôte nu — signalés par un bandeau permanent
(FR-047). `/healthz`, `/readyz` et `/metrics` ne sont pas authentifiés : c'est
le contrat de l'orchestrateur, sans contenu de l'instance.

**Falsification de requête intersite (NFR-012).** Le cookie de session est
ambiant : le navigateur le joint de lui-même. Toute requête qu'il authentifie
et dont la méthode n'est ni `GET`, ni `HEAD`, ni `OPTIONS` doit donc porter le
token anti-falsification de la session : le champ `csrf` des formulaires de
l'interface, ou l'en-tête `X-CSRF-Token` pour un appel d'API fait depuis une
page connectée. Avec le token, un appel d'API est servi sous l'identité de la
session, et le seuil propre au point d'accès s'applique toujours. Sans lui,
le cookie seul n'autorise aucune modification par l'API. En défense en
profondeur, une telle requête est aussi refusée quand le navigateur indique
qu'elle vient d'une autre origine. Ce verdict vient de `Sec-Fetch-Site`
quand le navigateur l'envoie, et sinon d'`Origin`. `Origin` est comparé à
l'en-tête `Host` de la requête, ou à `X-Forwarded-Host` derrière un proxy
inverse qui réécrit `Host` ; le schéma n'est pas comparé, afin que TLS puisse
être terminé en amont. Les identifiants Basic et Bearer ne portent aucune
autorité ambiante et en sont exemptés. Les retours fédérés sous `/auth/` le
sont aussi : ils portent leurs propres contrôles `state` et `InResponseTo`.
Le formulaire de connexion et, sous la dérogation FR-075, toute requête qui
modifie l'état sans identifiant sont refusés sur le seul verdict d'origine.

`Origin: null` compte comme une autre origine. Un navigateur l'envoie pour
un cadre sandboxé, et pour tout envoi de formulaire d'une page servie avec
`Referrer-Policy: no-referrer` : une page de falsification peut donc le
choisir. Les pages de l'instance sont servies avec
`Referrer-Policy: same-origin` : aucun référent ne part vers un autre
site, et le navigateur envoie leur vraie `Origin` sur leurs propres envois.
Une requête sans `Origin` ni `Sec-Fetch-Site` passe la vérification (curl,
la CLI). Un navigateur ne peut pas emprunter ce chemin contre l'API : tout
point d'accès de l'API qui lit un document exige
`Content-Type: application/json` (`TBY-SRV-005` sinon), un type qu'une page
d'une autre origine ne peut pas envoyer sans vérification préalable CORS,
et l'instance n'en accorde aucune.

Les refus sont codés, pas seulement numérotés : aucun identifiant →
`TBY-AUTH-002` ; rôle sous le seuil → `TBY-AUTH-003`, qui nomme le rôle requis ;
token anti-falsification absent ou falsifié, ou modification depuis une autre
origine → `TBY-AUTH-004` (NFR-012) ; session expirée →
`TBY-AUTH-005` ; token expiré → `TBY-AUTH-013` ; connexion fédérée refusée →
`TBY-AUTH-014` (OIDC), `TBY-AUTH-015` (SAML), fournisseur injoignable →
`TBY-AUTH-016`, aucun groupe associé → `TBY-AUTH-017`. Voir les
[erreurs](../../reference/errors/).

**La matrice ne peut pas dériver.** `internal/ui/rbac_matrix_test.go` parcourt
la table de routes que le serveur monte réellement et échoue si une route
manque dans la matrice documentée ; `TestRBACMatrixMirrorsUIFloors` fige chaque
paire interface/API. Une route ajoutée plus tard ne peut pas être livrée avec un
seuil non revu. C'est une preuve exécutable, pas une habitude de relecture —
voir [tests et preuves](../../project/tests-and-proofs/).

## Comptes et tokens en pratique

Les comptes se gèrent avec `tobby user add | passwd | list` sur l'hôte (le
premier compte est forcé `admin` ; `--password-stdin` pour l'automatisation),
et depuis `/admin/accounts` ou l'API. Le hash argon2id est toujours calculé par
l'outil — aucune surface n'accepte un hash précalculé. Tout compte authentifié
peut changer son propre mot de passe en fournissant l'actuel ; le succès comme
l'échec sont consignés à l'audit (FR-077 — livré en v0.3.0).

Les tokens d'API portent leur propre rôle, ne sont montrés qu'une fois à la
création, ne sont stockés que sous forme de hash et sont révocables
immédiatement (FR-072). Ils peuvent porter une expiration (R-14), affichée dans
chaque liste ; un token échu est refusé avec son propre code et le refus est
audité. Faites-les tourner par chevauchement : créez le token de remplacement,
basculez les clients, révoquez l'ancien — sans jamais de fenêtre sans
identifiant valide.

`docker login`, `podman login`, `helm registry login` et `oras login` utilisent
ces mêmes comptes et tokens sur `/v2/` (FR-076) : un seul système d'identité
pour toutes les surfaces.

Deux invariants sont appliqués dans le magasin de comptes lui-même, sous
verrou, de sorte qu'aucune surface ne peut les contourner : le dernier compte
`admin` ne peut être ni supprimé (`TBY-AUTH-011`) ni rétrogradé — y compris par
lui-même.

## Désactiver l'authentification

`auth.disabled` existe pour des installations délibérément isolées. Il ne se
règle que dans le fichier de configuration ou par `TOBBY_AUTH_DISABLED` —
jamais par flag — et n'est jamais silencieux (FR-075) : chaque requête porte
alors l'identité explicite `anonymous` avec le rôle `admin`, l'interface
affiche un bandeau permanent, et le démarrage émet un
[enregistrement d'audit](../../security/audit-log/) `auth.override_active`. La
posture par défaut, et la seule prise en charge, est l'authentification activée.

Sous la dérogation, la vérification d'origine est la seule protection
contre les requêtes falsifiées. Elle n'arrête pas le rebinding DNS : un
site hostile qui fait pointer son propre nom vers l'adresse de l'instance
est, pour le navigateur, la même origine que lui. L'instance ne valide pas
l'en-tête `Host`, faute de nom public configuré auquel le comparer. Gardez
une instance sans authentification sur un réseau que les navigateurs de
personnes qui visitent des sites non fiables ne peuvent pas atteindre, ou
placez devant elle un proxy inverse qui ne répond qu'à son propre nom
d'hôte.
