---
title: SRS et ADR
description: Index de la spécification des exigences et des registres de décisions d'architecture, hiérarchie des sources entre les deux, et processus des ADR.
sidebar:
  order: 7
---

Deux documents gouvernent ce que Tobby doit faire et pourquoi il est construit
ainsi : la **spécification des exigences logicielles** (SRS) et les **registres
de décisions d'architecture** (ADR). Les deux vivent dans le dépôt — le texte
canonique est là-bas, pas ici. Cette page est un index rendu ; un rendu HTML
complet du SRS et des ADR dans le site est une amélioration documentaire
ultérieure.

## Le SRS

**Texte canonique :**
[docs/SRS.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/SRS.md)
dans le dépôt.

Le SRS est la référence de conception, d'implémentation, de test et de
recette de Tobby v1.0.0. Sa structure :

1. **Introduction** — objet, périmètre, les deux modes de fonctionnement,
   définitions.
2. **Description générale** — perspective produit, classes d'utilisateurs, une
   Recipe illustrative (le schéma normatif vit dans
   [recipe-spec](https://tobby-fetch.github.io/recipe-spec/)).
3. **Exigences fonctionnelles** — `FR-xxx`, regroupées par thème (modes et
   configuration, acquisition, médias, promotion, UI/API, authentification,
   exploitation…).
4. **Exigences non fonctionnelles** — `NFR-xxx` (performance, sécurité,
   portabilité, qualité).
5. **Hors périmètre (v1.0)** — non-objectifs explicites, chacun avec sa
   justification.
6. **Annexe A** — l'index des ADR.

### Comment les exigences sont écrites et tracées

- Chaque exigence porte un numéro stable (`FR-035`, `NFR-015`), est rédigée
  comme un **énoncé testable** et accompagnée d'un court **critère
  d'acceptation**.
- Les numéros d'exigence sont cités partout dans le code et ses tests : les
  paquets qui implémentent une exigence la nomment dans leurs commentaires de
  documentation, les lignes de journal notables portent un champ
  `requirement`, et les tests structurels (le test anti-dérive du RBAC, le
  recoupement OpenAPI, le canari de sortie réseau) sont écrits contre
  l'exigence qu'ils prouvent. `git grep FR-035` dans le dépôt est une requête
  de traçabilité opérante.
- Les amendements sont datés sur place (par ex. *« FR-005 — amendement
  2026-08-12 »*), de sorte que l'historique d'une exigence se lit dans son
  propre texte.
- Trois autres familles d'identifiants apparaissent à côté. `UC1` à `UC3`
  sont les trois cas d'usage de la SRS (promotion passthrough,
  synchronisation miroir, registre de substitution ou d'amorçage). `R-nn`
  numérote un élément de feuille de route ajouté après la spécification
  initiale ; la
  [feuille de route](https://tobbyfetch.dev/roadmap.html)
  les liste, et l'amendement de la SRS qui en implémente un le cite.
  `B-nnn` numérote un défaut, cité par l'entrée du changelog de son
  correctif.

La façon dont la pyramide des exigences se traduit en preuves exécutables est
décrite dans [Tests et preuves de qualité](../../project/tests-and-proofs/).

## Hiérarchie des sources

Les deux documents répondent à des questions différentes et sont ordonnés :

- Le **SRS énonce ce que Tobby doit faire** — c'est l'autorité sur les
  exigences, et la recette se juge contre lui.
- Un **ADR consigne comment et pourquoi une décision a été prise** pour
  satisfaire ces exigences. Un ADR ne prime jamais sur une exigence : quand une
  décision demande que l'exigence elle-même change, le SRS est amendé (daté,
  sur place) et l'ADR référence l'amendement.
- Quand le code livré et l'un des deux documents divergent, c'est un défaut à
  résoudre — la documentation ne le masque jamais.

## Le processus des ADR

Les ADR suivent le format Nygard — *Statut / Contexte / Décision /
Conséquences / Alternatives étudiées* — et trois règles
([docs/adr/README.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/README.md)) :

- **Naissance** — les ADR sont numérotés dans l'ordre où les décisions ont été
  prises. Une orientation tranchée et déjà référencée par d'autres documents de
  conception pendant que le registre complet s'écrit porte le statut
  **Drafting**.
- **Arbitrage et amendement** — un ADR **Accepted** est en vigueur. Il peut
  être amendé *sur place* pour des changements additifs ou correctifs qui
  n'inversent pas la décision ; la ligne de statut consigne alors la date et la
  portée de l'amendement.
- **Substitution** — un renversement n'est jamais un amendement : il donne un
  nouvel ADR qui remplace l'ancien. L'ADR remplacé conserve son fichier avec le
  statut **Superseded by ADR-XXXX**. Les ADR ne sont jamais supprimés.

## Index des ADR

Tous les registres sont **Accepted**. Chaque lien ouvre le texte canonique sur
GitHub. Les titres des ADR sont ceux du dépôt (en anglais).

| ADR | Titre | Date | Résumé |
|---|---|---|---|
| [ADR-0001](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0001-recipe-format-k8s-style.md) | Recipe format: Kubernetes-style YAML manifests, versioned API group | 2026-07-11, amendé 2026-11-22 | Les recipes sont des manifestes YAML déclaratifs avec un groupe d'API versionné, familiers aux opérateurs Kubernetes et validables par schéma. |
| [ADR-0002](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0002-recipes-as-oci-artifacts.md) | Recipes as OCI artifacts; cookbook = OCI repository | 2026-07-11, amendé 2026-08-04 | Une recipe est publiée comme artefact OCI ; un cookbook est un dépôt OCI — signatures, transport et versionnement s'appuient sur l'écosystème des registries. Amendé pour aligner le transport des signatures sur le défaut de cosign 3.x. |
| [ADR-0003](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0003-repo-and-licensing-split.md) | Repository & licensing split: recipe-spec (Apache-2.0) / tobby (GPL-3.0) | 2026-07-11, amendé 2026-08-04 | Le format de recipe et son SDK sont Apache-2.0 pour que quiconque puisse l'implémenter ; l'application est GPL-3.0. |
| [ADR-0004](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0004-embedded-registry-cncf-distribution.md) | Embedded OCI registry: CNCF distribution v3 as a library | 2026-07-11 | L'instance embarque CNCF distribution comme store et surface `/v2/` standard, au lieu de s'appuyer sur une registry externe. |
| [ADR-0005](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0005-direct-to-storage-import.md) | Direct-to-storage import (no self-push loopback) | 2026-07-11 | Les imports écrivent directement dans le store au lieu de pousser vers le point d'accès HTTP de la registry de l'instance elle-même. |
| [ADR-0006](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0006-removable-media-transport.md) | Removable-media transport: self-contained store + OCI image layout export | 2026-07-11, amendé 2026-08-26 | Le franchissement de l'air-gap est le store lui-même, autonome et transportable, avec un export OCI image-layout interopérable. Amendé : la destination de l'export est un argument positionnel, `--output` nomme le format du rapport. |
| [ADR-0007](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0007-signing-cosign-key-based.md) | Signing & verification: Sigstore/cosign, key-based for air-gap | 2026-07-11 | Signatures cosign à clé, vérifiables sans aucun service en ligne — la forme qui fonctionne en zone isolée. |
| [ADR-0008](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0008-vulnerability-scanning-trivy.md) | Vulnerability scanning: Trivy, offline DB shipped as OCI artifact | 2026-07-11, amendé 2026-08-04, 2026-08-12 et 2026-11-11 | Analyse avec Trivy comme binaire externe épinglé, sous une politique CVSS numérique affinée par le Retriever et la recipe ; la base de CVE franchit l'air-gap comme artefact OCI porté par Tobby lui-même. |
| [ADR-0009](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0009-authentication-oidc-saml-tokens-basic.md) | Authentication: OIDC, SAML, static tokens, basic auth; minimal RBAC | 2026-07-11 | Quatre méthodes d'authentification sur un RBAC minimal à trois rôles ; les mêmes comptes servent l'UI, l'API et les clients de registry. |
| [ADR-0010](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0010-ui-server-rendered-htmx.md) | Web UI: Go server-rendered + htmx, no Node toolchain, i18n, themable CSS | 2026-07-11 | L'UI est rendue par le binaire Go avec l'interactivité htmx — zéro Node à la compilation comme à l'exécution, bilingue, thématisable par surcharge de feuille de style. |
| [ADR-0011](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0011-supply-chain-slsa-l3-apko.md) | Tobby's own supply chain: SLSA Build L3, melange+apko, reproducible builds | 2026-07-11, amendé 2026-08-04 | Comment Tobby est lui-même construit et attesté : builds reproductibles, images melange/apko, SLSA Build L3, barrière de conformité des licences et OpenVEX. |
| [ADR-0012](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0012-observability.md) | Observability & operations: structured logs, OpenMetrics, health probes | 2026-07-11 | Journaux JSON à clés de corrélation stables, point d'accès OpenMetrics, sondes de vivacité/disponibilité et arrêt gracieux. |
| [ADR-0013](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0013-ingredient-relocation-destination-naming.md) | Ingredient relocation and destination naming | 2026-08-03 | La convention de nommage déterministe : `docker.io/x` devient `<zone-registry>/docker.io/x`, hôtes canoniques, port `:` écrit `_`. |
| [ADR-0014](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0014-crucible-test-infrastructure-incus.md) | Crucible test infrastructure: Incus | 2026-08-04 | Le crucible de recette : topologies multi-zones réelles sur Incus, scénarios par jalon, rapports bruts conservés. |
| [ADR-0015](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0015-ui-invariants.md) | Web UI invariants: canonical URLs, reserved prefixes, htmx doctrine | 2026-08-12 | Les règles que toute contribution à l'UI applique : schéma d'URL canonique avec le séparateur `/-/`, préfixes de chemin réservés, et doctrine d'usage de htmx. |
| [ADR-0016](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0016-media-manifest.md) | Media manifest: an unsigned inventory, and what verification actually rests on | 2026-08-26 | Le manifeste du média est un inventaire non signé qui aide l'opérateur à lire un support ; la confiance repose sur la vérification de chaque artefact, pas sur le manifeste. |
| [ADR-0017](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0017-scan-by-content.md) | Scanning by content, whatever the declared kind | 2026-11-12 | L'analyse est décidée par ce que contient un artefact, pas par le type qu'il déclare. |

La correspondance exigences ↔ mécanismes ↔ preuves construite sur ces
documents est dans [Conformité](../../project/compliance/), et les preuves de
recette dans [Rapports de recette](../../project/acceptance-reports/).
