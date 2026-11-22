---
title: Modèle de menaces et flux réseau
description: Actifs, adversaires, frontières de confiance par mode, matrice complète des flux réseau, et chaque contrôle avec sa référence d'exigence et son état.
sidebar:
  order: 2
---

Cette page dit ce que Tobby défend, contre qui et par quel mécanisme. Chaque
contrôle porte son numéro d'exigence et son état de livraison. Pour le modèle
dans son ensemble, voir [le modèle de sécurité](../model/).

## Actifs

- **Le contenu transporté** — images de conteneurs, charts Helm, artefacts OCI,
  FileSets, et les recipes signées qui les décrivent. L'actif est l'intégrité
  au bit près et la provenance, de bout en bout.
- **Le registry de la zone de destination** — ce que les charges de travail de
  la zone protégée exécuteront réellement. Rien ne doit l'atteindre sans avoir
  été vérifié.
- **Les trust roots** — les clés publiques configurées qui décident de ce que
  « vérifié » signifie pour une instance.
- **L'état de l'instance** — comptes, hachages de tokens, clé TLS : l'identité
  de l'instance ([secrets](../../security/secrets/)).
- **La piste d'audit** — le relevé de qui a fait quoi
  ([journal d'audit](../../security/audit-log/)).

## Adversaires et ce qui les arrête

**Support altéré** (chemin air-gap). Un attaquant ayant un accès physique au
support de transport peut modifier ou remplacer tout ce qu'il contient — y
compris le manifeste et tout fichier de trust root qui y serait placé.
Contrôles : vérification côté destination de la complétude du manifeste et des
sommes de contrôle, puis des fichiers de chaque recipe contre leurs digests
épinglés et de sa signature cosign contre les trust roots de *cette* instance,
avant tout push, tout service et toute écriture dans le store (hors journal
d'opération et enregistrements de tâches sous `_tobby/`, hors couverture) ; chaque fichier couvert
est contrôlé contre sa propre adresse de contenu autant que contre l'inventaire,
si bien que réécrire l'inventaire non signé pour qu'il s'accorde avec un blob
corrompu ne change rien ; le contenu non atteignable depuis une recipe vérifiée
n'est jamais poussé et est signalé ; un échec d'intégrité ou de signature
bloque la recipe concernée en entier, sans contournement pour personne ; les
trust roots présents sur le support sont ignorés (FR-054, R-19 — livré en
v0.5.0). L'ordre de vérification et sa justification :
[sécurité des supports](../../air-gap/media-security/).

**Un support venu du mauvais endroit ou du mauvais mois.** Un support adressé
à une autre zone, ou plus ancien que le dernier importé par cette zone, est
refusé juste après le contrôle du manifeste et du graphe de recipes, avant la
lecture du moindre contenu de recipe (R-28). Ces deux refus sont des
**garde-fous contre l'accident, pas des contrôles de sécurité** — le manifeste
n'est pas signé, donc un tiers hostile peut forger l'un ou l'autre champ —, ce
qui explique précisément pourquoi ce sont les deux seuls que l'on peut lever,
et pourquoi le point haut enregistré ne recule jamais quand l'un d'eux est
levé. Sur l'écran et l'API, une dérogation exige le rôle `admin` ; en ligne
de commande il n'y a aucun contrôle de rôle, et l'accès à l'hôte est le
contrôle. Toute dérogation appliquée est auditée (FR-094), à l'étape
Vérifier comme à l'import : sur l'écran et l'API, une vérification avec
dérogation peut ouvrir le verrou de service, aussi son enregistrement est-il
écrit au lancement de la vérification, avant tout verdict.

**Un client qui demande du contenu à une instance non vérifiée.** Une instance
de destination qui détient un support transporté retient `/v2/` et `/files/`
tant qu'une vérification n'a pas validé le support en entier, en répondant
`403` avec `TBY-MED-030`. Aucun rôle ne la contourne, aucun réglage ne la
rouvre, et aucun verdict ne survit à un redémarrage (FR-054 — livré en v0.5.0).

**Registry amont compromis.** Un registry qui sert du contenu altéré ne peut pas
le faire accepter par Tobby : chaque ingrédient est épinglé par digest dans une
recipe cuite, chaque signature de recipe est vérifiée contre les trust roots
configurés à l'import *et* de nouveau avant le push (FR-033 — livré en
v0.3.0), de sorte qu'un ingrédient altéré ne correspond plus au digest
qu'épingle une signature vérifiée — la signature amont propre à un ingrédient
est consignée en plus, et exigée seulement sous
`trust.requireIngredientSignatures` — et les registries hors liste d'autorisation sont refusés
avant tout transfert (FR-030 — livré en v0.4.0). Un registry compromis peut au
pire provoquer un déni de service, pas injecter du contenu.

**Opérateur malveillant ou négligent.** Trois rôles fixes protègent chaque
surface (FR-074 — livré en v0.4.0) ; les actions qui touchent au contenu et les
changements de configuration sensibles sont inscrits au journal d'audit avec
l'acteur et l'origine (FR-094 — livré en v0.1.x, catalogue en croissance avec
les fonctions). Les assouplissements de sécurité sont par périmètre, déclarés
dans la configuration et visibles sur chaque surface : il n'existe pas de
contournement global discret (FR-033, FR-075). Le dernier administrateur ne peut
être ni supprimé ni rétrogradé
([authentification et RBAC](../../security/auth-rbac/)).

**Clé de signature compromise.** Tobby vérifie contre un *ensemble* de trust
roots : une clé d'organisation compromise est donc renouvelée par recouvrement —
ajouter le nouveau root, re-signer, retirer l'ancien (ADR-0007). Tobby ne
détient lui-même aucune clé de signature, de sorte que compromettre une
instance Tobby ne donne aucune capacité de signature. La garde des clés
(HSM/KMS, révocation) relève volontairement de l'organisation qui exploite
Tobby et sort de son périmètre (ADR-0007).

**Contenu honnêtement signé, mais porteur de CVE connues.** Une signature
prouve qui se porte garant d'un contenu, pas qu'il est exempt de
vulnérabilités. L'analyse (FR-031, FR-032, ADR-0008 — livrée en v0.6.0) répond
à cet adversaire sous une politique consultative ou bloquante, et le contenu
admis sous un périmètre assoupli reste marqué élément par élément (R-22 —
livré en v0.6.0). Voir [analyse de vulnérabilités](../vulnerability-scanning/)
et le [marqueur d'assurance réduite](../reduced-assurance/).

**Qui peut écrire le Retriever.** Le Retriever n'est volontairement pas
signé. Il porte la même confiance que la configuration de l'instance, car
son emplacement, `retriever.source`, est fixé au démarrage et ne peut être
changé ni par l'interface ni par l'API (l'écran de plan et
`POST /api/v1/plan` acceptent un document candidat, pour un plan à blanc
seulement). Le document est relu à chaque synchronisation. Quand
`retriever.source` est une URL ou une référence OCI, quiconque peut écrire à
cet emplacement peut donc changer l'état désiré de la zone et assouplir sa
politique d'analyse (relever le seuil, ajouter des exceptions ; voir
[analyse de vulnérabilités](../vulnerability-scanning/)). Protégez cet
emplacement comme le fichier de configuration lui-même.

## Frontières de confiance par mode

<svg viewBox="0 0 640 206" role="img" aria-label="Frontières de confiance : côté connecté, le contenu des registries sources n'est pas fiable tant que Tobby ne l'a pas vérifié ; à travers l'air gap, le support est revérifié de zéro par le Tobby de destination avant que les clients ne reçoivent du contenu vérifié uniquement — les trust roots configurés de la destination sont la seule autorité" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
  <defs>
    <marker id="tm-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--sl-color-gray-3)" />
    </marker>
  </defs>
  <!-- sides -->
  <rect x="8" y="58" width="240" height="112" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <rect x="296" y="58" width="336" height="112" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <text x="128" y="48" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Côté connecté</text>
  <text x="464" y="48" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Côté isolé</text>
  <!-- trust boundaries -->
  <line x1="138" y1="82" x2="138" y2="158" stroke="var(--sl-color-gray-3)" stroke-dasharray="3 3" />
  <line x1="392" y1="82" x2="392" y2="158" stroke="var(--sl-color-gray-3)" stroke-dasharray="3 3" />
  <line x1="518" y1="82" x2="518" y2="158" stroke="var(--sl-color-gray-3)" stroke-dasharray="3 3" />
  <text x="138" y="74" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-2)">non fiable avant vérification</text>
  <text x="392" y="74" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-2)">revérifié de zéro</text>
  <text x="518" y="74" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-2)">contenu vérifié seul</text>
  <!-- boxes -->
  <rect x="18" y="88" width="108" height="46" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="72" y="107" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-1)">Registries</text>
  <text x="72" y="121" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-1)">sources</text>
  <rect x="152" y="88" width="84" height="46" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="194" y="107" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="194" y="121" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">côté source</text>
  <rect x="308" y="88" width="72" height="46" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="344" y="115" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Support</text>
  <rect x="406" y="88" width="100" height="46" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="456" y="107" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="456" y="121" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">destination</text>
  <rect x="532" y="88" width="88" height="46" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="576" y="115" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Clients</text>
  <!-- flows -->
  <line x1="126" y1="111" x2="148" y2="111" stroke="var(--sl-color-gray-3)" marker-end="url(#tm-arrow)" />
  <line x1="236" y1="111" x2="304" y2="111" stroke="var(--sl-color-gray-3)" stroke-dasharray="4 4" marker-end="url(#tm-arrow)" />
  <text x="272" y="102" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">air gap</text>
  <line x1="380" y1="111" x2="402" y2="111" stroke="var(--sl-color-gray-3)" marker-end="url(#tm-arrow)" />
  <line x1="506" y1="111" x2="528" y2="111" stroke="var(--sl-color-gray-3)" marker-end="url(#tm-arrow)" />
  <!-- caption -->
  <text x="320" y="196" text-anchor="middle" font-size="10.5" fill="var(--sl-color-gray-2)">Une règle à chaque frontière : les trust roots configurés de la destination sont la seule autorité</text>
</svg>

**Passthrough** : l'instance se place entre une zone source plus exposée et une
zone de destination mieux protégée. Frontière 1 : tout ce qui est récupéré en
amont n'est pas fiable tant que la signature et le digest ne sont pas vérifiés.
Frontière 2 : le registry de destination ne reçoit que du contenu vérifié
(FR-028, FR-033).

**Mirror** : trois frontières. L'instance source connectée récupère et vérifie
comme ci-dessus ; le support n'est plus fiable dès qu'il quitte la garde de la
source ; l'instance de destination revérifie tout à partir de ses propres trust
roots (FR-052, FR-054) comme si le support était hostile — parce qu'il a pu
l'être.

## Flux réseau

| Flux | Sens | Passthrough | Mirror |
| --- | --- | --- | --- |
| Interface, `/api/v1`, registry `/v2/`, `/files/` | entrant | un seul listener, authentifié (FR-075), TLS possible (FR-082) | identique |
| `/healthz`, `/readyz`, `/metrics` | entrant | non authentifiés, aucun contenu d'instance | identique |
| Registries sources et dépôts Helm | sortant | filtré par la liste d'autorisation (FR-030), compatible proxy (FR-080), CA privées sans désactiver TLS (FR-081) | côté source uniquement ; aucun côté destination |
| Source du Retriever (fichier, HTTP(S), OCI) | sortant | point d'accès configuré (FR-010) ; le document n'est pas signé | côté source uniquement |
| Registry de la zone de destination | sortant | push différentiel du contenu vérifié (FR-028) | côté destination, depuis le store transporté |
| URL de trust roots | sortant | récupérées à la configuration uniquement, jamais à la vérification (FR-033) | formes en ligne ou fichier à la place |
| Fournisseur d'identité | sortant | OIDC/SAML (FR-070, FR-071 — livrés en v0.6.0) | OIDC/SAML si configurés, comme en passthrough ; une zone isolée n'a souvent que des comptes locaux |
| Télémétrie, vérification de mise à jour, rapports de plantage | sortant | **aucun, jamais** (NFR-019) | aucun |

**Aucune autre destination, jamais.** NFR-019 exige qu'aucune connexion
n'existe sans que l'opérateur l'ait configurée. La preuve est structurelle :
chaque exécution d'acceptation du crucible commence par un canari de sortie —
une sonde depuis la zone isolée qui doit échouer, prouvant que l'air gap est
réel avant qu'un scénario soit crédité — et les suites e2e passent derrière un
proxy qui capture la sortie et consigne chaque tentative de connexion
(ADR-0014). Voir les [tests et preuves](../../project/tests-and-proofs/).

## Synthèse des contrôles

| Contrôle | Référence | État |
| --- | --- | --- |
| Vérification des signatures, à l'import et avant le push | FR-033, ADR-0007 | livré en v0.3.0 |
| Liste d'autorisation des registries sur l'hôte effectif | FR-030, FR-036 | livré en v0.4.0 |
| Authentification active par défaut, RBAC | FR-073–FR-076, ADR-0009 | livré en v0.4.0 |
| Journal d'audit de sécurité, schéma à six champs | FR-094 | livré en v0.1.x, en croissance |
| Aucune sortie non configurée | NFR-019, ADR-0012 | livré en v0.1.x, prouvé par canari |
| Hygiène des secrets, CSRF, traversée de chemin, échappement | NFR-011–NFR-015 | livré |
| Déploiement de référence au moindre privilège | NFR-014 | livré en v0.4.0 |
| Vérification du support avant tout push, service ou écriture dans le store | FR-054, ADR-0006, ADR-0016 | livré en v0.5.0 |
| Blocage par recipe, sans contournement sur l'intégrité ou la signature | R-19 | livré en v0.5.0 |
| Secrets jamais sur un support, imposé au démarrage | R-16, NFR-020 | livré en v0.5.0 |
| Analyse Trivy avec politique, base hors ligne portée par Tobby | FR-031, FR-032, ADR-0008 — [Analyse de vulnérabilités](../vulnerability-scanning/) | v0.6.0 |
| OIDC et SAML 2.0 | FR-070, FR-071 | livré en v0.6.0 |
