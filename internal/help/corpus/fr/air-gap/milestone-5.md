---
title: Cap sur le jalon 5
description: Tout ce que le mode miroir et air-gap livre avec le jalon 5, consolidé sur une seule page jusqu'à la livraison.
sidebar:
  order: 4
  badge:
    text: J5
    variant: note
---

Le jalon 5 (train de release v0.5.x) livre le second cas d'usage complet :
préparer un média physique, le transporter, pousser son contenu en zone
isolée — avec des garde-fous à chaque étape pour un opérateur qui n'est pas
un expert de Tobby.

Tout ce qui suit est **à venir**. C'est une seule page consolidée, tenue
délibérément à la place de quatre coquilles vides ; à la livraison du
jalon 5, elle éclatera en quatre pages réelles — *Préparer le poste source*,
*Pré-vol*, *Importer côté zone isolée*, *Gérer les supports dans la durée* —
écrites contre le comportement livré. Les décisions de conception, elles,
sont déjà publiées : le [parcours média](../../air-gap/media-workflow/) et le
[modèle de sécurité du média](../../air-gap/media-security/) ne changeront pas
de forme. La livraison se suit sur la page
[État du projet](../../discover/status/).

## Préparer le poste source

L'instance source est un binaire unique sur un poste de travail, Linux ou
Windows — le parcours miroir sous Windows est validé de bout en bout en CI,
et la distribution poste ajoute un manifeste winget et un bucket Scoop aux
binaires de release et aux paquets deb/rpm/apk installables hors-ligne déjà
existants.

- **Installation hors-ligne** : les paquets s'installent sans dépôt
  distant ; la provenance de la release se vérifie au préalable — voir
  [Vérifier une release](../../project/verify-a-release/).
- **Quickstart en mode miroir** : le `tobby quickstart` guidé gagne les
  questions propres au miroir (emplacement du store transportable,
  emplacement de l'état, Retriever de la zone) et reste scriptable pour une
  mise en place non interactive.
- **Déclenchement manuel uniquement — une différence assumée avec le
  passthrough.** Une instance miroir ne se synchronise jamais sur un
  planning. La préparation d'un média est toujours un acte humain supervisé,
  déclenché depuis l'interface ou l'API (SRS FR-014). C'est une position de
  conception, pas une fonctionnalité manquante : un processus non supervisé
  ne doit pas décider de ce qui traverse un air gap.

## Pré-vol : vérifier avant d'exporter

Avant qu'une synchronisation ou un export ne démarre, le pré-vol répond aux
deux questions de l'opérateur — *est-ce que ça tient, est-ce que ça
passera ?*

- **Volume contre espace** : les octets à transférer sont calculés par
  recipe, dédupliqués par digest et nets de ce que la cible détient déjà,
  puis comparés à l'espace libre du média. Le pré-vol refusera de démarrer
  quand la projection dépasse l'espace libre moins une marge de sécurité
  configurable (10 % par défaut), en énonçant les octets manquants.
- **Refus explicites** : les systèmes de fichiers incapables de contenir la
  charge sont refusés nommément — FAT32 et sa limite de 4 Gio par fichier
  est le cas canonique — et un système de fichiers non identifiable produit
  un avertissement, jamais un silence. Une erreur « fichier trop gros » en
  cours d'écriture échoue proprement, store intact.
- **Dry-run scriptable** : un mode « plan » (CLI `--dry-run`, API,
  interface) produit le rapport complet d'une synchronisation à venir —
  résolution des versions, statuts par digest, volumes, nettoyage projeté,
  verdicts de politique — sans aucun effet de bord, et avec des codes de
  sortie distincts pour que les pipelines CI puissent s'en servir de porte.
  Il s'appuie sur le contrat CLI qui arrive aussi au jalon 5 :
  `--output json` documenté sur chaque commande et table des codes de sortie
  publiée sous versionnement sémantique.

### Le store sur le média

Le store transportable est un répertoire ordinaire, autonome et
auto-descriptif :

```text
<store>/
├── registry/   # store OCI adressé par contenu : images, charts,
│               # artefacts, filesets — et les recipes elles-mêmes
├── logs/       # journaux d'opération JSON structurés (les deux sens)
└── meta/       # version du format de store, état de synchro, manifeste de média
```

Pour l'interopérabilité, le store (ou une sélection de recipes) peut aussi
être exporté vers — et importé depuis — le format standard **OCI image
layout**, en répertoire ou en tar unique, lisible par `skopeo`, `oras` et
`crane`. La forme de commande prévue par la décision de conception :

```bash
tobby export --format oci-layout --output /media/usb/payload.tar   # aller
tobby import --format oci-layout /media/usb/payload.tar            # retour
```

L'export au layout ne porte que les artefacts — journaux et état de synchro
sont des concepts du store — et c'est exactement pourquoi il est le format
d'interopérabilité, pas le format principal. La remise à zéro d'un store
(repartir propre sur un média) est une action admin à confirmation tapée,
journalisée à l'audit.

## Importer côté zone isolée

L'instance de destination reçoit un écran **Média** dédié, pendant guidé du
pipeline de vérification :

- une synthèse d'inventaire — zone, horodatage, recipes, volumes ;
- des **verdicts par étape** (intégrité → signatures → digests) et **par
  recipe**, avec l'enchaînement guidé Vérifier → Rapport → Pousser ;
- un **blocage au juste grain** : les recipes dont la signature et chaque
  digest vérifient sont poussables ; tout le reste est bloqué, sans
  exception, et listé nommément dans le rapport. Un inventaire corrompu ou
  une identité de zone erronée restent bloquants globalement ;
- **une seule dérogation**, et une seule : un admin peut lever un désaccord
  d'identité de zone, et la dérogation est écrite au journal d'audit. Les
  échecs d'intégrité et de complétude n'ont aucun chemin de dérogation ;
- chaque refus dit quoi faire ensuite, dans le style de la taxonomie
  d'erreurs employée partout ailleurs — les codes d'erreur média
  rejoindront la [référence des erreurs](../../reference/errors/) à leur
  livraison.

## Gérer les supports dans la durée

- **Identité et fraîcheur** : chaque support reçoit un identifiant unique,
  inscrit à son inventaire et à ses journaux. La destination mémorise
  l'horodatage du dernier import de la zone et refusera, par défaut, un
  support plus ancien — l'accident classique du média interverti — avec un
  déblocage admin audité pour les cas légitimes.
- **Prune aligné sur le Retriever** : le contenu que le Retriever de la zone
  ne demande plus est retiré du store au moment de la synchronisation, avec
  la liste et la taille totale affichées avant l'opération. Les imports
  unitaires, la base de vulnérabilités et le seeding d'amorçage sont des
  racines protégées, jamais retirées. La taille d'un média qui tourne reste
  ainsi proportionnelle aux besoins *courants* de la zone, pas à son
  historique.
- **Un média = une zone** : un store porte l'identité de la zone pour
  laquelle il a été préparé, et la destination la fait respecter. Ne
  partagez pas un support physique entre zones ; dimensionnez un support par
  zone et par cycle.
- **Dimensionnement des cycles** : la déduplication par digest rend le
  deuxième cycle différentiel — un support dimensionné pour le premier
  transfert complet est confortable ensuite. Le rapport de pré-vol donne la
  projection avant chaque run : le dimensionnement se mesure, il ne se
  devine pas.
- **Cohérence d'horloge** : les comparaisons de fraîcheur supposent des
  horloges plausibles. La détection d'une horloge implausible au démarrage
  et à l'ouverture d'un support — avertir et consigner à l'audit, jamais
  corriger en silence — est prévue au jalon 7 (R-32).
- **Mettre à jour Tobby lui-même en zone isolée** : au jalon 6, chaque
  release est publiée en artefacts OCI prêts à référencer dans une recipe,
  si bien que les mises à jour de Tobby traversent le flux miroir standard
  comme n'importe quel contenu, signées par la chaîne de qualification de
  votre site — aucun auto-update, jamais (R-25).

## Fin du parcours

Après un import réussi, la registry de zone sert le contenu transféré. Y
raccorder vos clusters et vos hôtes —
[Brancher vos clients](../../passthrough/connect-clients/) — fonctionne à
l'identique dans les deux modes : cette page est la destination du parcours
air-gap, pas seulement celle du passthrough.
