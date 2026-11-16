---
title: Licences, gouvernance, pérennité
description: GPL-3.0 pour l'application, Apache-2.0 pour le format — pourquoi, ce que cela signifie pour votre service juridique, comment les vulnérabilités sont traitées, et pourquoi le projet reste auditable sans son éditeur.
sidebar:
  order: 4
  badge:
    text: Partial
    variant: caution
---

:::caution[Partiellement complet]
Le contenu sur les licences, le DCO et le signalement de vulnérabilités
ci-dessous est à jour. Le traitement des vulnérabilités s'étoffe avec le
jalon 6, quand l'analyse avec politique est livrée et reçoit sa propre
documentation. Suivez-le sur la page [état du projet](../../discover/status/).
:::

## Deux licences, une frontière

Le projet est réparti sur deux dépôts sous deux licences, et ce découpage est
une décision de conception
([ADR-0003](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0003-repo-and-licensing-split.md)) :

| Quoi | Licence | Où |
|---|---|---|
| L'application Tobby (CLI, service, registry embarquée, interface, API) | GPL-3.0-only | [tobby-fetch/tobby-fetch](https://github.com/tobby-fetch/tobby-fetch) |
| Le format Recipe/Retriever : spécification, schémas JSON, SDK Go, `recipe lint` | Apache-2.0 | [tobby-fetch/recipe-spec](https://github.com/tobby-fetch/recipe-spec) |

Le raisonnement : le **format** doit se diffuser sans aucune friction —
n'importe quel outil, y compris propriétaire, peut l'implémenter, embarquer
le SDK et interopérer. L'**application** est protégée par le copyleft : les
améliorations de Tobby lui-même restent ouvertes. Le copyright est détenu par
infraBuilder SASU et les contributeurs ; il n'y a pas de CLA — les
contributions sont acceptées sous le Developer Certificate of Origin (voir
plus bas).

## Les questions GPL que posera votre service juridique

Réponses courtes d'abord, précédents ensuite. Ceci n'est pas un avis
juridique ; c'est une carte de l'analyse usuelle.

**Exécuter Tobby rend-il notre logiciel GPL ?** Non. Les obligations de la
GPL portent sur la distribution d'œuvres dérivées du programme, pas sur son
exécution. Le contenu que Tobby transporte est une donnée traitée par le
programme ; il n'est pas lié à Tobby et ne devient pas une œuvre dérivée.

**Nos recipes deviennent-elles GPL ?** Non. Les recipes sont des documents
YAML dans un format spécifié sous Apache-2.0. Ce sont des entrées du
programme, comme un `Makefile` l'est pour Make. Le SDK pour les analyser est
sous Apache-2.0.

**Pouvons-nous scripter la CLI et l'API ?** Oui. Invoquer un programme GPL
comme processus séparé, ou appeler son API REST, relève de l'usage, non de
l'édition de liens. C'est la même posture que pour les outils GPL que la
plupart des organisations exécutent déjà en production : Git, Ansible, Bash.
Vous les *exécutez* ; vous ne les liez pas à vos produits.

**Quand les obligations de la GPL s'appliquent-elles réellement ?** Si vous
modifiez le code source de Tobby et distribuez le binaire modifié à des tiers,
vous devez fournir le code source correspondant sous GPL-3.0. L'usage interne
d'un build modifié, sur votre propre infrastructure, ne déclenche aucune
obligation de distribution.

**Quelle version de la GPL, exactement ?** `GPL-3.0-only` (et non « ou
ultérieure »), indiquée dans le fichier
[LICENSE](https://github.com/tobby-fetch/tobby-fetch/blob/main/LICENSE). Les
termes de la licence ne peuvent pas changer silencieusement avec une future
révision de la FSF.

## Contributions et DCO

Chaque commit doit être signé (`git commit -s`), ce qui atteste le
[Developer Certificate of Origin](https://developercertificate.org/) : vous
avez le droit de soumettre la modification sous la licence du projet. Le
contrôle est automatique en CI sur chaque pull request. Le flux complet de
contribution — compilation depuis les sources, pyramide de tests, DCO en
pratique — vit en un seul endroit de référence : [Contribuer](../../project/contribute/).

## Signaler une vulnérabilité

Le projet publie une politique de sécurité
([SECURITY.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/SECURITY.md)).
L'essentiel :

- **Canal :** signalements privés via les GitHub Security Advisories, onglet
  Security du dépôt — jamais une issue publique.
- **Réponse :** accusé de réception sous 7 jours ; divulgation coordonnée à
  une date convenue ; les vulnérabilités confirmées sont corrigées et publiées
  en version corrective de la ligne mineure courante, avec un avis qui crédite
  le rapporteur.
- **Versions prises en charge :** avant la 1.0, seule la dernière version
  mineure reçoit des correctifs de sécurité. Le tableau des versions prises en
  charge sera publié à la sortie de la 1.0.
- **Périmètre :** le dépôt de l'application (CLI, service, registry,
  interface, API). Le format et le SDK ont leur propre politique dans le
  dépôt `recipe-spec`.

Côté sortant, la **règle OpenVEX** s'applique aux releases de Tobby
elles-mêmes : si un scanner remonte un constat qui ne s'applique pas à Tobby,
la justification est livrée sous la forme d'un `tobby.openvex.json` signé
joint à la release — jamais une règle d'ignorance silencieuse (la politique et
son fonctionnement sont décrits dans
[`.vex/README.md`](https://github.com/tobby-fetch/tobby-fetch/blob/main/.vex/README.md)).
À ce jour aucun constat n'a été écarté, donc aucune release ne porte ce
document : son absence signifie que les gates CRITICAL/HIGH sont passées sans
rien exclure. Les images de release sont aussi reconstruites chaque semaine
sur la base Wolfi courante afin de maintenir l'affirmation « zéro CVE connue »
entre deux releases.

## Pérennité : auditable sans l'éditeur

Un outil destiné à des environnements réglementés et isolés doit répondre à
une question inconfortable : *que se passe-t-il si l'éditeur disparaît ?* La
réponse de Tobby ne repose pas du tout sur la confiance envers l'éditeur :

- **Builds reproductibles.** Toute partie disposant de Git et de Go peut
  reconstruire n'importe quelle release taguée bit à bit et comparer les
  digests — la vérification la plus forte disponible en zone isolée, sans
  infrastructure de signature. La procédure est dans
  [Vérifier une release](../../project/verify-a-release/).
- **Spécification ouverte.** Le format de recipe est sous Apache-2.0 avec
  schémas JSON et SDK Go. Votre inventaire de ce qui a franchi chaque
  frontière est lisible — et réimplémentable — sans Tobby.
- **Stockage standard.** Le store est du contenu OCI standard derrière une
  API de registry conforme ; les outils standard (`skopeo`, `oras`, `crane`)
  peuvent tout extraire, signatures comprises.
- **Conception publique.** Les exigences ([SRS](../../reference/srs-adr/)),
  les décisions d'architecture et les
  [rapports de recette](../../project/acceptance-reports/) bruts sont
  publiés. Un audit ne dépend pas de la coopération de l'éditeur.

La GPL-3.0 complète le tableau : le code ne peut pas être refermé
rétroactivement, et tout successeur — commercial ou communautaire — hérite du
droit de le continuer.
