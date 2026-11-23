---
title: Comparaison honnête
description: Comment Tobby se situe face à Zarf, Hauler, la réplication Harbor et les scripts skopeo — y compris dans les cas où l'alternative l'emporte.
sidebar:
  order: 2
---

Aucun outil n'existe seul. Si vous évaluez Tobby, vous regardez sans doute
aussi Zarf, Hauler, la réplication de Harbor ou un ensemble de scripts
skopeo. Cette page les compare au niveau des capacités et dit sans détour
dans quels cas chaque alternative est le meilleur choix. Les affirmations sur
les autres outils reposent sur leur documentation publique ; vérifiez l'état
actuel de chaque projet avant de décider — tous évoluent.

## La comparaison, axe par axe

| Axe | Tobby | Zarf | Hauler | Réplication Harbor | Scripts skopeo |
|---|---|---|---|---|---|
| Vérification à l'arrivée (signatures et digests revérifiés à la frontière de destination, bloquant par défaut) | Oui — cosign contre les trust roots de la destination, avant tout push | Signature cosign au niveau du paquet, vérifiée au déploiement si vous fournissez la clé ; pas de politique par artefact amont | Vérification cosign optionnelle à l'ajout du contenu au store ; pas de barrière bloquante sur les signatures au chargement d'un haul côté destination | Analyse et politique dans la registry, une fois le contenu arrivé | Possible via le `policy.json` de containers, à assembler et maintenir vous-même |
| Promotion entre zones connectées **et** transfert air-gap dans un seul outil | Oui — même application, mêmes recipes, deux modes | Orienté air-gap ; la promotion en zone connectée n'est pas le modèle | Orienté air-gap (archives haul) | Registry à registry connecté uniquement ; aucun parcours par média amovible | Les deux, si vous scriptez les deux |
| Aucune réécriture de références (digests et signatures amont préservés) | Oui — relocalisation canonique sous l'hôte source, sans mutation | Non — les références d'image des pods sont modifiées pour tirer depuis la registry interne de Zarf (webhook Zarf Agent) | Le contenu est reconditionné en hauls ; les références dépendent de la façon dont vous les servez | Oui — et les signatures cosign associées sont répliquées avec les images | Oui — avec `--all`/`--preserve-digests` ; une copie par défaut peut re-résoudre une liste de manifestes |
| Pas de chaîne d'outils Node, pas de télémétrie, aucune connexion sortante non déclarée | Oui — binaire Go unique, garantie testée par un canari d'egress | Binaire Go ; vérifiez vous-même le comportement actuel en télémétrie et mises à jour | Binaire Go ; à vérifier vous-même | Plateforme multi-services ; surface à auditer plus large | Oui — binaire unique |
| Servir des paquets OS (apt/rpm) depuis du contenu vérifié | Oui — FileSets servis en lecture seule sous `/files/` | Hors objectif | Un serveur de fichiers existe ; pas de barrière de signature devant lui | Non | Non |
| Déployer des workloads dans un cluster | Non — hors périmètre | **Oui — c'est sa force principale** | Non | Non | Non |

## Là où les alternatives l'emportent

L'honnêteté coûte moins cher qu'une évaluation ratée ; la voici donc sans
détour :

- **Zarf l'emporte quand vous voulez du déploiement, pas seulement du
  transport.** Zarf empaquette toute une application — images, charts,
  manifestes, jusqu'à l'amorçage d'un cluster — et la déploie côté isolé. Si
  votre objectif est « monter cette application Kubernetes en zone air-gap
  avec une seule commande », Zarf fait tout le parcours et Tobby s'arrête
  volontairement à la registry de zone. Le prix est le modèle de Zarf : les
  workloads sont modifiés pour tirer depuis sa registry interne, et ce que
  vérifie le côté isolé est la signature cosign du paquet, non celle de
  chaque artefact amont.
- **Hauler l'emporte pour un haul ponctuel et rapide.** Si vous devez de
  temps en temps faire passer quelques images et charts à travers un gap
  sans politique permanente, le flux « collecter puis servir » de Hauler est
  plus léger que le déploiement d'une instance vérifiante de chaque côté. Il
  sait aussi vérifier les signatures cosign pendant la collecte et emporte
  par défaut signatures, attestations et SBOM dans le haul — ce qui lui
  manque est une barrière de politique bloquante côté destination.
- **La réplication Harbor l'emporte si Harbor est déjà partout chez vous.**
  Pour de la réplication connectée de registry à registry entre instances
  Harbor — avec projets, quotas, cache proxy et une interface mûre pour de
  nombreuses équipes —, la réplication intégrée de Harbor est le choix
  naturel. Elle n'a aucune histoire de média amovible, et son modèle de
  vérification vit dans la registry plutôt qu'à la frontière de transfert.
- **skopeo l'emporte par son ubiquité et l'absence d'infrastructure.** Il est
  sur la machine de chaque opérateur, n'exige aucun service et copie tout ce
  qui est OCI — avec les bons flags, digests compris. Ce que vous assemblez
  vous-même, c'est tout ce qui entoure la copie : l'inventaire de l'état
  désiré, la synchronisation différentielle, les reprises, la distribution
  de la politique de signatures, les journaux d'audit et la discipline de
  vérification côté destination. Cet assemblage est, pour l'essentiel, ce
  qu'est Tobby.

## Là où Tobby l'emporte

Un seul outil couvre la boucle de promotion en état déclaré **et** le
parcours physique air-gap, avec la même discipline de vérification à chaque
frontière : signatures vérifiées contre les trust roots propres à la
destination avant tout push, digests épinglés de bout en bout, références
jamais réécrites, liste d'autorisation devant chaque connexion sortante, et
piste d'audit qui franchit le gap avec le contenu. La chaîne
d'approvisionnement de l'outil lui-même fait partie de l'offre : builds
reproductibles, provenance SLSA Build L3 et SBOM signés que [vous pouvez
vérifier vous-même](../../project/verify-a-release/).

## Réversibilité : la sortie ne coûte rien

Adopter Tobby n'enferme pas votre contenu. Le store est du contenu OCI
standard servi par une registry conforme, et
[`tobby export`](../../reference/cli/#tobby-export) l'écrit — ou une partie
— sous forme de layout d'image OCI standard. Au pire, skopeo travaille
directement sur le store : tout artefact peut être copié avec des outils
standard, signatures comprises. Le [format de recipe](https://tobby-fetch.github.io/recipe-spec/)
est une spécification ouverte Apache-2.0 avec schémas JSON et SDK Go,
implémentable par n'importe quel outil — y compris un outil qui remplacerait
Tobby.
