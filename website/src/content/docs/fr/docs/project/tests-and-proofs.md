---
title: Tests et preuves
description: La pyramide de tests à cinq niveaux, les gates de CI telles qu'elles sont réellement câblées, et comment rejouer vous-même le crucible.
sidebar:
  order: 2
---

La qualité est une barrière d'entrée, pas une phase finale : la pyramide et la
réplique de l'environnement cible existaient avant les fonctionnalités, et
rien ne fusionne au rouge. Cette page décrit les gates telles qu'elles sont
câblées aujourd'hui — chaque affirmation renvoie au workflow ou au répertoire
qui l'implémente.

## La pyramide à cinq niveaux

1. **Unitaire** — `go test -race -count=2 ./...` : détecteur de courses
   toujours actif, et chaque test s'exécute deux fois dans le même processus,
   si bien qu'un test qui ne passe qu'une fois ne fusionne jamais.
2. **Intégration** — dans la même suite, contre de vrais protocoles : le
   protocole OCI n'est jamais simulé (une vraie registry embarquée sur des
   ports éphémères), et `docker login`, `helm login` et `oras login` sont
   exercés sur de vraies sockets contre des binaires clients épinglés.
3. **Bout en bout, cœur** — des scénarios de topologie hermétiques sur chaque
   pull request : des topologies multi-zones conteneurisées qui rejouent le
   parcours de chaque jalon.
4. **Non-régression navigateur** — un vrai Chrome qui pilote l'interface, pour
   la classe de bugs qui vit dans un attribut interprété par le client.
5. **Crucible** — le niveau réaliste sur de vrais nœuds jetables, qui
   conditionne l'acceptation des jalons plutôt que les fusions.

## Les gates de CI, telles que câblées

[`ci.yml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/.github/workflows/ci.yml)
exécute treize jobs bloquants sur chaque pull request ; son en-tête énonce le
contrat : *chaque job conditionne la fusion*. Par nom :

| Job | Ce qui bloque la fusion |
|---|---|
| `lint` | golangci-lint v2.15.0, profil strict, zéro suppression |
| `test` | suite race + `-count=2` sur linux/amd64, linux/arm64, macOS **et** Windows, puis le plancher de couverture par paquet |
| `cross-compile` | les six cibles de release (linux, windows, darwin × amd64/arm64) doivent compiler |
| `licenses` | chaque dépendance — arbre de test compris — contre la liste d'autorisation de compatibilité GPL-3.0 |
| `e2e-topology` | les scénarios hermétiques des jalons 1 à 4 et le parcours UC2 du jalon 7, rapports bruts des vérifications publiés dans le résumé du job |
| `e2e-browser` | scénarios chromedp, avec `TOBBY_E2E_REQUIRE_CHROME=1` pour que « pas de navigateur » soit un échec et non un saut |
| `coverage-core` | couverture de lignes des paquets du cœur (fetch, resolve, store, push, policy, signatures) à 70 % ou plus |
| `i18n` | catalogues anglais et français complets : aucune clé manquante ou orpheline, placeholders cohérents |
| `fuzz` | chaque cible de fuzzing tourne 30 secondes ; les entrées en échec sont conservées en artefact |
| `conformance` | la suite de conformité OCI distribution-spec épinglée, sur linux/amd64 et linux/arm64 |
| `govulncheck` | vulnérabilités connues atteignables, bibliothèque standard comprise — un constat est un chemin d'appel, pas une correspondance de manifeste |
| `secrets` | gitleaks sur tout l'historique git, pas seulement la pointe du checkout |
| `vulnerabilities` | Trivy sur les dépendances ; critique et haute font échouer le job |

Les actions tierces sont épinglées par SHA de commit complet, et les outils
téléchargés (helm, oras, gitleaks) sont vérifiés contre des digests SHA-256
épinglés avant usage (ADR-0011). Selon
[CONTRIBUTING.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/CONTRIBUTING.md),
`main` est protégée : pas de push direct, et une pull request exige une revue
approuvée et une CI verte pour fusionner. Il y a aussi une
[gate DCO](../contribute/) sur chaque commit.

## Preuves structurelles

Certains tests existent pour rendre impossible une *classe* de régression, non
pour vérifier un comportement :

- **Le test anti-dérive RBAC.** La matrice des permissions
  ([docs/rbac-matrix.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/rbac-matrix.md))
  est appliquée par une table qui fait échouer la suite quand une route est
  enregistrée sans déclarer son rôle minimal. Une surface ajoutée plus tard ne
  peut pas partir silencieusement sans protection — voir
  [authentification et RBAC](../../security/auth-rbac/).
- **Le canari de sortie qui doit échouer.** Chaque passage de topologie et de
  crucible commence par une instance du segment isolé qui tente un trafic
  sortant ; le scénario échoue sauf si cette tentative échoue. « Aucune route
  vers l'extérieur » est prouvé par construction à chaque passage, jamais
  supposé — l'affirmation sur laquelle s'appuie le
  [modèle de menaces](../../security/threat-model/).
- **Le négatif qui prouve le test.** Un test vérifie qu'aucun chemin sortant
  ne contourne le transport partagé (proxy, AC privées) ; son pendant retire
  le proxy et exige que tous les chemins échouent — ce qui prouve que le
  premier test observe réellement quelque chose.
- **Des tests navigateur sans Node.** La suite de l'interface pilote un vrai
  Chrome via [chromedp](https://github.com/chromedp/chromedp) — du Go pur,
  sans chaîne d'outils Node, cohérent avec la position zéro Node du produit.
  Chrome est pris dans l'environnement et jamais téléchargé (NFR-019). Le
  périmètre est volontairement étroit : chaque scénario nomme le bug ou
  l'exigence qu'il verrouille.

## Position sur la couverture

La gate est un **plancher par paquet, pas une moyenne globale** : chaque
paquet sous `internal/` doit tenir au moins 70 % de couverture d'instructions
([`tools/check-coverage.sh`](https://github.com/tobby-fetch/tobby-fetch/blob/main/tools/check-coverage.sh),
NFR-016), un paquet sans test compte pour 0 %, et seul `cmd/` (le câblage) est
exempté. Une moyenne globale laisse un paquet bien testé subventionner un
paquet non testé ; un plancher non. État mesuré à l'audit du jalon 4 : 86,8 %
en moyenne, 76,5 % au minimum. La couverture est traitée comme un fil de
déclenchement, non comme un objectif — ce sont les niveaux supérieurs de la
pyramide qui énoncent les garanties.

## Rejouer le crucible vous-même

Le crucible est une réplique jetable et versionnée de la réalité de
déploiement — une zone connectée, une zone isolée, un support amovible qui est
un vrai périphérique bloc — gérée par
[Incus](https://linuxcontainers.org/incus/) (ADR-0014). La CI conditionne les
fusions ; le crucible conditionne l'acceptation des jalons. Tout ce qu'il faut
pour le rejouer est dans le dépôt, sous
[`crucible/`](https://github.com/tobby-fetch/tobby-fetch/tree/main/crucible) :

```
crucible/
├── setup.sh          # one-time: project, networks, profiles, volumes
├── teardown.sh       # destroys the whole crucible project
├── run.sh            # scenario runner: ./run.sh m1 [m2 …] | all
├── lib.sh            # shared helpers (checks, raw report)
└── scenarios/
    ├── m1/run.sh … m7/run.sh   # one scenario per milestone
```

Les scénarios sont étiquetés par jalon et invocables indépendamment : la suite
de tous les jalons livrés reste rejouable à tout moment
(`./crucible/run.sh all`). L'air-gap est un pont géré sans lien montant,
associé à une ACL réseau qui rejette tout sauf le trafic intra-zone ; le
support est un périphérique bloc monté en boucle, rattaché, rempli, détaché et
rattaché de nouveau de part et d'autre de la coupure. Les prérequis matériels
sont dans le
[README du crucible](https://github.com/tobby-fetch/tobby-fetch/blob/main/crucible/README.md)
et résumés sur la page [contribuer](../contribute/).

Chaque passage écrit un rapport brut des vérifications : une ligne `ok`/`FAIL`
par vérification nommée, horodatée, avec les digests observés. L'acceptation
d'un jalon publie ce rapport sans retouche — à lire dans les
[rapports d'acceptation](../acceptance-reports/).
