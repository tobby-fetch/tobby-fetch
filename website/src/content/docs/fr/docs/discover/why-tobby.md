---
title: Pourquoi Tobby
description: Le problème que Tobby résout, ce qu'il n'est délibérément pas, et comment choisir entre ses deux modes de fonctionnement.
sidebar:
  order: 1
---

Votre organisation qualifie des logiciels dans une zone connectée : elle
construit ou sélectionne des images de conteneur, des charts Helm, des modèles
d'IA et des fichiers, les analyse, les épingle par digest et signe le
résultat. Les zones qui ont besoin de ce contenu se trouvent derrière des
frontières réseau — réseaux restreints et, à l'extrême, zones sans aucun
chemin réseau. Y acheminer du contenu qualifié passe généralement par
re-taguer des images, réécrire des valeurs de charts ou reconditionner le tout
dans une archive opaque. Chacune de ces étapes casse le digest ou la signature
qui rendaient le contenu fiable au départ. Tobby fait franchir ces frontières
au contenu **sans en modifier un seul octet**, et revérifie tout à l'arrivée.

<svg viewBox="0 0 640 336" role="img" aria-label="Trois formes de déploiement indépendantes : passthrough entre deux zones connectées, cascade d'instances passthrough, et instances mirror échangeant un store sur média amovible — chacune acheminant du contenu des registries sources vers la registry d'une zone restreinte" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
  <defs>
    <marker id="wt-arrow-fr" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--sl-color-gray-3)" />
    </marker>
  </defs>
  <!-- row 1: passthrough, one hop -->
  <text x="8" y="14" font-size="11" font-weight="600" fill="var(--sl-color-gray-2)">Passthrough — entre deux zones connectées</text>
  <rect x="8" y="24" width="120" height="40" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="68" y="41" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Registries sources</text>
  <text x="68" y="55" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">docker.io, ghcr, …</text>
  <rect x="245" y="24" width="120" height="40" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="305" y="41" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="305" y="55" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">passthrough</text>
  <rect x="482" y="24" width="150" height="40" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="557" y="41" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Registry de zone restreinte</text>
  <text x="557" y="55" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">docker · helm · oras</text>
  <line x1="128" y1="44" x2="241" y2="44" stroke="var(--sl-color-gray-3)" marker-end="url(#wt-arrow-fr)" />
  <line x1="365" y1="44" x2="478" y2="44" stroke="var(--sl-color-gray-3)" marker-end="url(#wt-arrow-fr)" />
  <!-- row 2: cascade -->
  <text x="8" y="119" font-size="11" font-weight="600" fill="var(--sl-color-gray-2)">Cascade — chaque zone alimente la suivante, recipes inchangées</text>
  <rect x="8" y="129" width="120" height="40" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="68" y="146" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Registries sources</text>
  <text x="68" y="160" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">docker.io, ghcr, …</text>
  <rect x="166" y="129" width="120" height="40" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="226" y="146" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="226" y="160" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">passthrough</text>
  <rect x="324" y="129" width="120" height="40" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="384" y="146" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="384" y="160" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">passthrough</text>
  <rect x="482" y="129" width="150" height="40" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="557" y="146" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Registry de zone restreinte</text>
  <text x="557" y="160" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">docker · helm · oras</text>
  <line x1="128" y1="149" x2="162" y2="149" stroke="var(--sl-color-gray-3)" marker-end="url(#wt-arrow-fr)" />
  <line x1="286" y1="149" x2="320" y2="149" stroke="var(--sl-color-gray-3)" marker-end="url(#wt-arrow-fr)" />
  <line x1="444" y1="149" x2="478" y2="149" stroke="var(--sl-color-gray-3)" marker-end="url(#wt-arrow-fr)" />
  <!-- row 3: mirror / air-gap -->
  <text x="8" y="224" font-size="11" font-weight="600" fill="var(--sl-color-gray-2)">Air-gap (mirror) — le store voyage sur média amovible</text>
  <rect x="8" y="234" width="120" height="40" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="68" y="251" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Registries sources</text>
  <text x="68" y="265" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">docker.io, ghcr, …</text>
  <rect x="166" y="234" width="120" height="40" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="226" y="251" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="226" y="265" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">mirror — côté source</text>
  <rect x="324" y="234" width="120" height="40" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="384" y="251" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="384" y="265" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">mirror — côté isolé</text>
  <rect x="482" y="234" width="150" height="40" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="557" y="251" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Registry de zone isolée</text>
  <text x="557" y="265" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">docker · helm · oras</text>
  <line x1="128" y1="254" x2="162" y2="254" stroke="var(--sl-color-gray-3)" marker-end="url(#wt-arrow-fr)" />
  <line x1="286" y1="254" x2="320" y2="254" stroke="var(--sl-color-gray-3)" stroke-dasharray="4 4" marker-end="url(#wt-arrow-fr)" />
  <text x="303" y="246" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">média</text>
  <line x1="444" y1="254" x2="478" y2="254" stroke="var(--sl-color-gray-3)" marker-end="url(#wt-arrow-fr)" />
  <!-- bottom note -->
  <text x="320" y="308" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-2)">Un outil, trois formes de déploiement — combinez-les selon vos zones</text>
  <text x="320" y="326" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">Mêmes digests, mêmes signatures — revérifiés à chaque frontière</text>
</svg>

## Le transpalette qui ne signe rien

Tobby est un outil de transport, et délibérément rien de plus. Une image
utile : un transpalette dans un entrepôt. Il transporte des palettes
scellées, contrôle les scellés à chaque porte et refuse une palette dont le
scellé est brisé. Il n'ouvre jamais une palette, n'en refait jamais une et
n'appose jamais de scellé de son cru.

**Ce que Tobby est :**

- Un transporteur de contenu OCI : images de conteneur, charts Helm,
  artefacts OCI arbitraires et jeux de fichiers — piloté par des
  [Recipes](../../recipes/understand/) signées et épinglées par digest.
- Un vérificateur à chaque frontière : signatures cosign contrôlées contre les
  trust roots propres à la destination, digests revérifiés avant chaque push,
  liste d'autorisation des registries appliquée avant tout transfert.
- Une registry OCI portable : chaque instance expose un endpoint `/v2/`
  conforme aux standards, que `docker`, `podman`, `helm`, `oras` et `skopeo`
  consomment directement ; elle peut ainsi amorcer un nouvel environnement ou
  se substituer à une registry de zone.

**Ce que Tobby n'est pas :**

- Il ne construit pas, ne teste pas, n'analyse pas pour qualifier, ne
  sélectionne pas de logiciel. Il consomme la sortie de votre pipeline de
  qualification.
- Il **ne signe rien** et ne détient aucune clé privée. L'authenticité
  provient exclusivement des signatures cosign de votre organisation,
  vérifiées contre les trust roots configurées sur l'instance réceptrice. Si
  Tobby signait du contenu, il deviendrait une ancre de confiance — et une
  cible.
- Il ne réécrit pas les références. `docker.io/library/postgres` reste
  reconnaissable sous la forme `<zone-registry>/docker.io/library/postgres` ;
  digests et signatures franchissent le trajet inchangés.
- Il ne purge pas les registries de destination et n'orchestre pas de
  déploiements. Voir [limites et hors périmètre](../../discover/limits/) pour
  la liste complète, chaque point avec sa conséquence et sa justification.

## Choisir son mode

Une instance fonctionne dans exactement un mode, choisi au démarrage.

| | Passthrough | Mirror |
|---|---|---|
| Entre | Deux zones connectées | Une zone connectée et une zone air-gap |
| Fonctionne comme | Service conteneurisé de longue durée (Kubernetes ou autre runtime) | Binaire unique sur un poste de travail ou un hôte transportable |
| Déclenchement | Périodique, intervalle configurable | Manuel uniquement — jamais sans surveillance |
| Transport | Push réseau différentiel vers la registry de zone | Transport physique d'un store autonome sur média amovible |
| Statut | **Disponible** (v0.4.x) | **Disponible** (v0.5.x) |

Les deux modes sont livrés. Le parcours du média est décrit de bout en bout
dans [le workflow média](../../air-gap/media-workflow/), son modèle de
sécurité dans [le modèle de sécurité du média](../../air-gap/media-security/).

## Trois façons de faire ses preuves

**Promotion continue entre zones connectées.** Une zone restreinte déclare ce
qu'elle veut dans un unique manifeste, son *Retriever*. Une instance
passthrough le relit selon un calendrier, résout les recipes depuis le
cookbook de production, vérifie signatures et politique, et ne pousse que ce
qui manque à la registry de zone. Un second passage sans nouveauté ne transfère
aucun octet. Les recipes signées voyagent avec leur contenu, si bien que la
zone peut prouver ce qu'elle détient. Commencez par
[la vue d'ensemble du passthrough](../../passthrough/overview/), ou
[essayez une première promotion](../../try/first-promotion/) en dix minutes.

![L'écran de correspondance d'une recipe : chaque ingrédient avec son chemin relocalisé, son tag, son digest et sa présence locale](../../../../../assets/docs/recipe-mapping.png)

**Franchir l'air gap.** La même déclaration pilote un poste mirror : il
synchronise les recipes choisies sur un store autonome, un opérateur porte le
média de l'autre côté du gap, et la même application de l'autre côté revérifie
tout — complétude, digests, signatures contre les trust roots de la
destination — avant qu'un seul octet n'atteigne la registry de zone. Le
parcours est décrit de bout en bout dans
[la section air-gap](../../air-gap/media-workflow/), de
[la préparation du poste source](../../air-gap/prepare-source/) à
[l'import côté isolé](../../air-gap/import-destination/).

**Amorcer et se substituer.** Parce que chaque instance est une registry OCI
conforme, Tobby peut amorcer un environnement tout neuf — y compris en servant
des paquets OS depuis des FileSets vérifiés en HTTP simple — ou faire office de
remplaçant temporaire pendant l'indisponibilité d'une registry de zone.

## Et ensuite

- [Essayer en 10 minutes](../../try/install-and-start/) — un binaire, un
  démarrage rapide guidé.
- [Comparaison honnête](../../discover/comparison/) — Zarf, Hauler, réplication
  Harbor, scripts skopeo, y compris là où ils l'emportent.
- [Le modèle de sécurité en une page](../../security/one-pager/) — le document
  à remettre à votre RSSI.
- [État du projet](../../discover/status/) — chaque fonctionnalité, son
  statut, son jalon.
