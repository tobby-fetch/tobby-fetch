---
title: Modèle de sécurité du média
description: Pourquoi le manifeste de média est délibérément non signé, ce que la destination vérifie et dans quel ordre, et ce qui ne voyage jamais sur le média.
sidebar:
  order: 2
---

Cette page est le modèle de sécurité du média amovible : ce qui voyage, ce
qui est vérifié, par qui, et contre quelle autorité. Elle est écrite pour le
relecteur sécurité qui doit approuver une procédure de transfert par média.

Le modèle est acté et spécifié
([ADR-0006](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0006-removable-media-transport.md),
[ADR-0007](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0007-signing-cosign-key-based.md),
SRS FR-054). Le jalon 5 l'implémente ; le modèle lui-même est un engagement
de conception et ne changera pas. Le travail d'homologation peut s'appuyer
sur cette page avant la livraison du code.

## Ce qui voyage sur le média

Le store transportable embarque, dans un seul répertoire autonome :

- les **artefacts** — images, charts, artefacts OCI, filesets — dans un
  store OCI adressé par contenu ;
- les **recipes signées** qui justifient chaque artefact, avec leurs
  signatures cosign attachées ;
- les **journaux d'opération** de la synchronisation qui a produit le
  store ;
- le **manifeste de média** : inventaire de chaque fichier avec son
  checksum, recipes couvertes avec leurs digests, identité de zone,
  horodatage de résolution, run ID et version du format de store.

Deux choses ne voyagent jamais : les **secrets** et le **matériel de
confiance** (voir plus bas).

## Le manifeste est un inventaire non signé — et c'est sûr

Le manifeste de média n'est délibérément **pas signé**. C'est le point où la
plupart des revues de sécurité s'arrêtent, alors voici le raisonnement en
entier.

Le manifeste est une *aide d'intégrité et de complétude*, pas une ancre de
confiance. Tobby transporte du contenu ; il n'est pas une autorité de
provenance. Une clé de signature détenue par l'outil de transport
n'ajouterait aucune sécurité : un attaquant capable d'altérer le store
pourrait re-signer le manifeste altéré avec cette même clé. Le signer
fabriquerait une fausse ancre de confiance, pas une vraie.

L'authenticité vient d'ailleurs : des signatures cosign des recipes
elles-mêmes, apposées par votre chaîne de qualification avant que Tobby ne
voie le contenu, et vérifiées côté destination contre les trust roots
configurées de l'instance de destination. Chaque ingrédient est ensuite
contrôlé contre le digest épinglé dans sa recipe signée. Les recipes signées
rendent donc la complétude *dérivable indépendamment* : chaque digest
épinglé doit être présent et correct, si bien qu'altérer le manifeste ne
peut cacher à la vérification ni un contenu manquant, ni un contenu modifié,
ni un contenu en trop.

Ce que l'inventaire non signé vous apporte réellement :

- des **échecs rapides et localisés avec précision** — quel fichier, quelle
  recipe — au lieu d'une erreur de vérification générique au moment du
  push ;
- une **garde anti-accident** : le champ d'identité de zone permet à la
  destination de refuser un média préparé pour une autre zone avant toute
  autre chose.

Les limites sont dites avec la même honnêteté : le manifeste ne prouve rien
par lui-même, et la documentation de Tobby ne prétend jamais le contraire.
Voir aussi [Limites et hors-périmètre](../../discover/limits/).

## Les trust roots de la destination sont la seule autorité

L'instance de destination vérifie les signatures des recipes
**exclusivement** contre ses propres trust roots configurées. Tout matériel
de confiance présent sur le média — fichiers de clés, trust roots
alternatives — est ignoré. Un média ne peut donc jamais apporter sa propre
autorité avec lui : compromettre le canal de transport ne compromet pas la
décision de confiance.

Conséquences, toutes normatives (SRS FR-054) :

- Le contenu signé pour l'environnement cible est accepté ; tout le reste ne
  l'est pas.
- Un contenu présent sur le média mais **non atteignable depuis une recipe
  vérifiée** n'est jamais poussé, et est rapporté. Il n'y a pas de porte
  dérobée pour les artefacts orphelins.
- Deux zones aux trust roots différentes peuvent recevoir le même média
  physique et en accepter légitimement des sous-ensembles différents.

La configuration, les périmètres et la rotation des trust roots sont
couverts dans
[Signatures, trust roots et liste blanche](../../security/content-trust/).

## L'ordre des vérifications

À l'arrivée, la vérification suit un ordre fixe, et **tout précède le
moindre push, le moindre service, la moindre écriture locale** :

1. **Complétude et checksums** — chaque fichier listé au manifeste est
   présent et correspond à son checksum ; la version du format de store est
   supportée ; l'identité de zone correspond à celle de l'instance.
2. **Signatures et digests** — la signature cosign de chaque recipe vérifie
   contre les trust roots de la destination ; chaque ingrédient correspond
   au digest épinglé dans sa recipe.

L'ordre est délibéré : les échecs d'intégrité sont bon marché à détecter et
nomment le fichier exact, ils sortent donc en premier ; la vérification des
signatures s'exécute ensuite sur un store réputé intact au bit près.

Le blocage est tout aussi fixe :

- Un **échec d'intégrité ou de complétude bloque, sans dérogation**. Pas de
  drapeau, pas de boîte de confirmation, pas de chemin admin autour d'un
  média corrompu.
- Le **désaccord d'identité de zone** est le seul refus dérogeable : un
  admin peut le lever, et la dérogation est écrite au
  [journal d'audit](../../security/audit-log/).
- La vérification et la décision se font **par recipe** : les recipes dont
  la signature et tous les digests vérifient sont poussables ; le reste est
  bloqué et listé nommément. Un inventaire corrompu ou une zone erronée
  restent bloquants globalement.

:::note[À venir — jalon 5]
Le pipeline de vérification arrive avec le jalon 5. L'ordre, les règles de
blocage et la granularité par recipe ci-dessus sont le comportement spécifié
qu'il implémente (SRS FR-054, R-19).
:::

## Les secrets ne voyagent jamais

Les fichiers de secrets — credentials de registry, clés privées TLS, mots de
passe de proxy — appartiennent à une instance, pas à un transfert. Ils
vivent dans le répertoire d'état de l'instance et ne sont jamais écrits sous
le store transportable. Le répertoire transporté contient du contenu, des
signatures, des journaux et le manifeste ; rien dedans n'authentifie
personne.

:::note[À venir — jalon 5]
Le jalon 5 transforme cette règle en contrôle appliqué (R-16) : Tobby refuse
de démarrer si des fichiers de secrets résident dans le store transportable,
et applique des permissions restrictives par défaut. La séparation
elle-même est une règle de conception sur laquelle vous pouvez déjà bâtir
votre procédure. Détails dans [Secrets](../../security/secrets/).
:::

## Le chiffrement au repos est l'affaire de l'OS

Tobby ne chiffre pas le média. Le chiffrement au repos est délégué à la
couche OS : LUKS sous Linux, BitLocker sous Windows, ou l'outillage approuvé
par votre site. Le raisonnement :

- Le chiffrement de volume est une capacité OS mature et certifiée ; la
  réimplémenter dans une application la dupliquerait, en moins bien.
- Cela placerait la gestion de clés dans une application qui traverse des
  frontières de confiance — exactement là où le matériel de clé ne doit pas
  vivre.
- Les sites qui imposent un outillage de chiffrement approuvé ne pourraient
  de toute façon pas utiliser un mécanisme applicatif.

La conséquence opérationnelle est honnête et simple : **la confidentialité
du média relève de la politique média de votre site, appliquée avec les
outils de votre site.** Tobby garantit l'intégrité et l'authenticité de ce
qui est lu depuis le média, quoi qu'il y ait dessous.

## Station de décontamination

Les sites industriels font couramment passer les médias entrants par une
station de décontamination ou d'inspection. Tobby suppose cette étape au
lieu de la combattre :

- La charge est faite de **fichiers ordinaires aux checksums vérifiables** —
  aucune particularité exotique de système de fichiers, aucun format
  conteneur opaque qu'une station ne saurait inspecter.
- La destination **re-vérifie tout après** la station, dans l'ordre
  ci-dessus. La station n'a pas besoin d'être de confiance pour
  l'intégrité : si elle altère la charge, la vérification le dit, fichier
  par fichier.
- Si la station rejette ou met en quarantaine des fichiers, le modèle de
  blocage par recipe dégrade proprement : les recipes intactes restent
  poussables, les recipes touchées sont bloquées et nommées.

## Ce que cela donne à un auditeur

- L'authenticité est ancrée dans les clés de votre organisation, vérifiée
  côté isolé, hors-ligne, contre une configuration qui ne voyage jamais.
- L'intégrité est contrôlée fichier par fichier avant tout push ou service.
- Chaque dérogation est une action admin nommée et auditée ; les échecs
  d'intégrité n'en ont aucune.
- La vue contrôle par contrôle, avec l'état de livraison, est sur le
  [modèle de sécurité en une page](../../security/one-pager/).
