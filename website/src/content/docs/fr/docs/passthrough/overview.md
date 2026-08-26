---
title: Zones connectées — vue d'ensemble
description: Comment une instance passthrough maintient le registre d'une zone au niveau demandé par son Retriever, et l'ordre dans lequel parcourir cette section.
sidebar:
  order: 1
---

Une instance passthrough est le cas d'usage livré de Tobby (jalon 4,
train v0.4.x) : un service permanent placé entre deux zones réseau
connectées. D'un côté, elle lit depuis les registres sources —
directement, ou à travers le proxy d'entreprise. De l'autre, elle pousse
dans le registre de la zone, celui dont vos clusters et vos hôtes tirent
réellement leur contenu. Entre les deux vivent son propre store et un
registre OCI embarqué, si bien que la zone peut aussi consommer le
contenu directement depuis Tobby.

<svg viewBox="0 0 640 230" role="img" aria-label="Les registres sources alimentent une instance Tobby en passthrough dont le Retriever pilote chaque cycle — relire, réconcilier, pousser en différentiel — vers le registre de zone, depuis lequel les clients de la zone tirent le contenu" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
  <defs>
    <marker id="po-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--sl-color-gray-3)" />
    </marker>
  </defs>
  <!-- retriever -->
  <rect x="245" y="16" width="130" height="36" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="310" y="31" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Retriever</text>
  <text x="310" y="44" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">état désiré de la zone</text>
  <line x1="310" y1="52" x2="310" y2="82" stroke="var(--sl-color-gray-3)" marker-end="url(#po-arrow)" />
  <text x="320" y="70" font-size="9.5" fill="var(--sl-color-gray-3)">relu à chaque cycle</text>
  <!-- sources -->
  <rect x="16" y="93" width="130" height="44" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="81" y="112" text-anchor="middle" font-size="12" fill="var(--sl-color-gray-1)">Registres sources</text>
  <text x="81" y="126" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">direct ou via proxy</text>
  <!-- tobby -->
  <rect x="230" y="88" width="160" height="54" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="310" y="109" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Tobby — passthrough</text>
  <text x="310" y="124" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-3)">store + registre embarqué</text>
  <!-- loop -->
  <path d="M 288 146 C 272 176, 348 176, 334 148" fill="none" stroke="var(--sl-color-gray-3)" marker-end="url(#po-arrow)" />
  <text x="310" y="192" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">chaque cycle : relire → réconcilier → pousser</text>
  <!-- zone -->
  <rect x="460" y="64" width="172" height="156" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <text x="546" y="54" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Zone</text>
  <rect x="476" y="96" width="140" height="38" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="546" y="119" text-anchor="middle" font-size="12" fill="var(--sl-color-gray-1)">Registre de zone</text>
  <rect x="476" y="160" width="140" height="44" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="546" y="179" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Clients de la zone</text>
  <text x="546" y="193" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">docker · containerd · helm</text>
  <!-- flows -->
  <line x1="146" y1="115" x2="226" y2="115" stroke="var(--sl-color-gray-3)" marker-end="url(#po-arrow)" />
  <text x="186" y="107" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">fetch + vérifier</text>
  <line x1="390" y1="115" x2="472" y2="115" stroke="var(--sl-color-gray-3)" marker-end="url(#po-arrow)" />
  <text x="431" y="107" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">push différentiel</text>
  <text x="431" y="129" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">recipes comprises</text>
  <line x1="546" y1="134" x2="546" y2="156" stroke="var(--sl-color-gray-3)" marker-end="url(#po-arrow)" />
  <text x="556" y="149" font-size="9.5" fill="var(--sl-color-gray-3)">pull</text>
</svg>

## La promotion continue

La boucle est volontairement simple, et chaque étape est une exigence
que vous pouvez auditer :

1. **Relire le Retriever.** À chaque cycle, l'instance re-télécharge le
   document d'état désiré depuis `retriever.source` (FR-010) et résout
   chaque recipe listée depuis le cookbook, en respectant les
   contraintes de version (`~`, `^`, `12.x`) à chaque passage — une
   version corrective arrive par simple publication, sans aucun fichier
   à modifier.
2. **Réconcilier.** Le registre de destination est comparé à ce que les
   recipes épinglent. Seul ce qui manque est déplacé.
3. **Pousser en différentiel.** Les blobs et manifestes déjà présents à
   la destination ne sont jamais renvoyés (FR-028). Un second cycle sur
   un contenu inchangé transfère zéro octet — le crucible joue
   exactement ce scénario.
4. **Re-vérifier avant chaque push.** Les signatures sont contrôlées
   contre la copie locale avant chaque push, et non une seule fois à
   l'import (FR-033). Un store altéré entre deux cycles ne se propage
   pas.
5. **Propager les recipes.** Les artefacts de recipe signés eux-mêmes
   sont poussés vers le cookbook de la zone, à côté de leurs ingrédients
   (FR-034), si bien que le cookbook de la zone reflète toujours ce que
   la zone détient réellement — et qu'une zone plus en aval peut s'y
   enchaîner.

Le cycle s'exécute tous les `sync.interval` (15m par défaut).
L'intervalle se change à chaud depuis l'écran d'administration et depuis
l'API, sans redéployer (FR-013), et cette modification est auditée comme
configuration sensible (FR-094). Une synchronisation peut aussi être
déclenchée à la main, depuis l'écran des recipes ou avec
`POST /api/v1/sync`.

Les ingrédients arrivent à destination sous leur hôte source nominal —
`docker.io/library/nginx` devient
`registry.zone.example/docker.io/library/nginx` — digests et signatures
inchangés. Tobby ne réécrit jamais et ne re-signe jamais le contenu ; la
règle de nommage et ses conséquences pour vos clients font l'objet de
[Brancher vos clients](../../passthrough/connect-clients/).

## Prérequis

- Un hôte Linux ou un cluster Kubernetes pour l'instance — voir la
  [matrice OS](../../passthrough/deploy/#matrice-des-systèmes-dexploitation).
- Deux répertoires sur des volumes distincts : le **store** (volumineux,
  re-téléchargeable) et l'**état** (petit, la cible de sauvegarde). Ils
  ne doivent pas être imbriqués l'un dans l'autre ; Tobby refuse de
  démarrer sinon.
- Un accès réseau aux registres sources, directement ou à travers un
  proxy sortant, et au registre de destination.
- Un registre de destination qui accepte les chemins de dépôt imbriqués
  — Tobby le teste avant de pousser et échoue explicitement quand ce
  n'est pas le cas (FR-035).
- Un document Retriever et des recipes signées dans un cookbook. Pour
  comprendre ces notions d'abord, lisez
  [recipes, cookbook, retriever](../../recipes/understand/).

## Parcourir cette section

Les pages sont ordonnées comme se déroule un déploiement réel :

1. **[Déployer](../../passthrough/deploy/)** — Kubernetes (chart Helm ou
   manifestes bruts), paquets pour VM, image de conteneur, et le premier
   compte.
2. **[Réseau d'entreprise](../../passthrough/network/)** — proxy
   authentifié, autorités de certification privées, et le TLS que
   l'instance sert elle-même.
3. **[Retriever de zone et cascade](../../passthrough/retriever-cascade/)** —
   le document d'état désiré, et comment les zones s'enchaînent sans
   toucher aux recipes.
4. **[Brancher vos clients](../../passthrough/connect-clients/)** —
   pourquoi les chemins ont cette forme, les mirrors containerd, les
   pièges GitOps, et l'endpoint pour les paquets d'OS.
5. **[Imports ponctuels](../../passthrough/one-off-import/)** — faire
   entrer une image ou un chart isolé par référence, hors de toute
   recipe.
6. **[Exploiter dans la durée](../../passthrough/operate/)** — probes,
   suivi des tâches et reprise, sauvegarde, croissance, montées de
   version.

Si vous n'avez encore jamais fait tourner Tobby, le parcours en dix
minutes [installer et démarrer](../../try/install-and-start/) et le pas à
pas [première promotion](../../try/first-promotion/) sont l'introduction
la plus rapide ; cette section suppose que vous les avez faits et que
vous visez la production.
