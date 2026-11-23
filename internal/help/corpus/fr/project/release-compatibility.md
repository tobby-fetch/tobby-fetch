---
title: Versions et compatibilité
description: Les trains de versions, la définition de « terminé », la politique de correctifs, et ce qui est stable dès maintenant par opposition à ce qui sera figé à la 1.0.
sidebar:
  order: 4
---

Cette page est la référence de la politique de compatibilité. Les autres
pages y renvoient ; aucune ne la répète.

## Trains de versions

Un jalon, un train mineur : le jalon N paraît en `v0.N.x`. Livrés à ce jour,
d'après le
[changelog](https://github.com/tobby-fetch/tobby-fetch/blob/main/CHANGELOG.md) :

| Train | Jalon | Première version | Périmètre |
|---|---|---|---|
| v0.1.x | Fondations | v0.1.0 — 2026-08-11 | chaîne de build, registry embarquée, gates qualité |
| v0.2.x | Aperçu UX | v0.2.0 — 2026-08-12 | interface, authentification par défaut, taxonomie d'erreurs, tâches, `/api/v1` |
| v0.3.x | Moteur de recipes | v0.3.0 — 2026-08-16 | recipes signées, vérification à l'entrée, cascade |
| v0.4.x | Passthrough | v0.4.0 — 2026-08-18 | promotion continue, liste d'autorisation, RBAC, réseau d'entreprise |
| v0.5.x | Miroir et air-gap | v0.5.0 — 2026-08-27 | miroir sur un store transportable, parcours média, mode plan, FileSets |
| v0.6.x | Contrôles renforcés et identité d'entreprise | v0.6.0 — 2026-11-12 | analyse de vulnérabilités avec politique, OIDC et SAML, vérification du store |

Le jalon 7 (finitions, documentation et recette) sort en `v0.7.0`, une
version mineure ordinaire. La 1.0.0 suivra quand les audits de code, de
maintenabilité et de sécurité seront faits et leurs constats principaux corrigés.

L'état courant et le chemin vers la 1.0 sont sur la page
[état du projet](../../discover/status/) — la source de vérité unique pour le
statut des fonctionnalités.

## Définition de « terminé »

Un jalon ne paraît pas tant que : les exigences du SRS dans son périmètre ne
sont pas implémentées et testées, ses scénarios de crucible ne sont pas écrits
et verts, les chaînes visibles de l'utilisateur n'existent pas en anglais et
en français, la documentation du jalon n'est pas à jour, et le **rapport
d'acceptation brut n'est pas publié** — voir les
[rapports d'acceptation](../acceptance-reports/).

## Politique de correctifs — deux précédents

Une version corrective ne contient que des corrections, jamais de
fonctionnalités, sur le train mineur courant. La politique se lit au mieux à
travers ses précédents :

- **v0.4.1** (2026-08-18, le jour même de la v0.4.0) a comblé un manque par
  rapport à une exigence, découvert juste après l'acceptation : une instance
  qui servait le certificat de repli généré ne pouvait pas en adopter un de
  remplacement depuis l'écran d'administration (FR-082). Un seul correctif,
  publié sans attendre.
- **v0.4.2** (2026-08-22) a porté les corrections de
  l'[audit qualité du jalon 4](../acceptance-reports/) : deux défauts de
  concurrence confirmés dans le chemin du service de longue durée, un
  historique de tâches non borné, et un lot de durcissements de surface et de
  chaîne d'outils. Aucune fonctionnalité — et la version a été ré-acceptée en
  rejouant la suite crucible **entière**, m1 à m4, sur un hôte jetable neuf.

La ligne 0.5 a suivi le même schéma : **v0.5.1** (2026-11-07) et
**v0.5.2** (2026-11-09) ont été des versions correctives sur la ligne mineure
courante.

Les correctifs de sécurité suivent
[SECURITY.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/SECURITY.md) :
correctif sur la ligne mineure courante, divulgation coordonnée, seule la
dernière version mineure est prise en charge avant la 1.0.

## Déjà stable, en 0.x

Certaines surfaces ont été déclarées stables bien avant la 1.0, parce que les
opérateurs écrivent des scripts dessus dès le premier jour :

- **Codes de sortie de la CLI** — 0 succès, 1 échec, 2 usage, 3 refus de
  politique, 4 échec de vérification, 5 changements planifiés (`sync --dry-run`), selon les classes de la taxonomie
  d'erreurs depuis la v0.2.0. Voir la [référence CLI](../../reference/cli/).
- **Codes d'erreur `TBY-*`** — des identifiants courts et stables ; un code ne
  change jamais de sens. Voir
  [erreurs et dépannage](../../reference/errors/).
- **`/api/v1`** — la racine versionnée de l'API (FR-060), documentée par le
  document OpenAPI que l'instance sert. Voir la
  [référence de l'API](../../reference/api/).
- **Le schéma des événements d'audit** — six champs (acteur, action, cible,
  résultat, horodatage, origine), versionné avec la même discipline de
  compatibilité que l'API REST (FR-094). Voir le
  [journal d'audit](../../security/audit-log/).

## Figé à la 1.0

À la 1.0, la règle d'évolution par ajout devient une garantie SemVer sur
**quatre surfaces** : la CLI, l'API REST, le schéma de configuration et la
disposition du stockage sur disque.

Pour le store, la garantie est concrète (R-26) : le store estampille déjà la
version de son format avec une politique de compatibilité explicite, et sur
toute la série 1.x un store écrit par n'importe quelle version 1.x est lisible
par n'importe quelle autre, dans les deux sens. Si une version non prise en
charge est un jour rencontrée, l'erreur nomme les deux versions en jeu — et la
porte de sortie est l'outillage OCI standard : le store s'exporte et
s'importe comme une disposition d'image OCI que skopeo, oras et crane lisent
(FR-051). Au pire, votre contenu n'est jamais captif.

La CLI porte son propre contrat sous la même promesse (R-08) : `--output json`
sur toute commande qui rapporte quelque chose, avec un JSON Schema publié ; le
[tableau des codes de sortie](../../reference/cli/), généré à partir du code
et couvert par SemVer — supprimer un code ou en renuméroter un est un
changement incompatible ; et un fonctionnement garanti sans interaction.
Chacun des quatre est tenu par un test qui parcourt l'arbre réel des
commandes.

## Comment les défauts sont suivis aujourd'hui

Tobby est en phase de développement, avec des jalons livrés chaque semaine.
Pratique actuelle, visible dans le dépôt : les défauts reçoivent un
identifiant stable `B-nnn` dans la liste de suivi des mainteneurs, et chaque
défaut corrigé est divulgué dans le
[changelog](https://github.com/tobby-fetch/tobby-fetch/blob/main/CHANGELOG.md)
sous cet identifiant **avec sa cause racine** — lisez les entrées de la
v0.4.0 pour B-014 ou B-015 pour voir le niveau de détail. Les problèmes connus
trouvés mais reportés sont publiés eux aussi : la liste « reporté, avec
responsables » de l'audit qualité du jalon 4 est publique dans
[`docs/acceptance/`](https://github.com/tobby-fetch/tobby-fetch/tree/main/docs/acceptance).

Les rapports de bugs venus de l'extérieur sont les bienvenus dès maintenant,
via les [modèles de tickets](https://github.com/tobby-fetch/tobby-fetch/issues)
du dépôt — sauf les vulnérabilités présumées, qui passent par le canal privé
décrit dans SECURITY.md. À l'approche de la 1.0, les tickets publics
deviendront aussi l'unique outil de suivi des défauts.
