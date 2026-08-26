---
title: Le parcours média de bout en bout
description: Les cinq étapes stables d'un transfert par média amovible — préparer, pré-vol, exporter, transporter, importer — et qui agit à chacune.
sidebar:
  order: 1
  badge:
    text: Partiel
    variant: caution
---

En mode miroir, un transfert est un répertoire. Tobby synchronise le contenu
demandé par une zone dans un store autonome, le store voyage sur un média
amovible à travers l'air gap, et une instance Tobby côté zone isolée le
vérifie puis le pousse dans la registry de zone. Déplacer le répertoire,
c'est le transfert — il n'y a aucune étape d'empaquetage ou de dépaquetage
propriétaire.

:::caution[Conception actée, procédures au jalon 5]
Le parcours ci-dessous — ses étapes, ce qui voyage, ce qui est vérifié et
dans quel ordre — est acté et spécifié
([ADR-0006](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0006-removable-media-transport.md),
SRS FR-050 à FR-056). Le code qui l'exécute arrive avec le jalon 5 : les
écrans, commandes et procédures pas à pas sont décrits sur
[Cap sur le jalon 5](../../air-gap/milestone-5/) et suivis sur la page
[État du projet](../../discover/status/). Utilisez cette page dès aujourd'hui
pour concevoir votre procédure de site et votre dossier d'homologation ;
revenez au jalon 5 pour le détail opérationnel.
:::

## Deux instances, un répertoire

<svg viewBox="0 0 640 246" role="img" aria-label="Une instance source sur un poste connecté prépare, passe le pré-vol et exporte sur un média amovible ; le média traverse la station de décontamination vers la zone isolée, où l'instance de destination importe — vérifie, puis pousse vers la registry de zone — et écrit ses journaux en retour sur le même média" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
  <defs>
    <marker id="mwf-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--sl-color-gray-3)" />
    </marker>
  </defs>
  <!-- côtés -->
  <rect x="8" y="32" width="200" height="178" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <rect x="432" y="32" width="200" height="178" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <text x="108" y="22" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Côté connecté</text>
  <text x="532" y="22" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Zone isolée</text>
  <!-- instance source -->
  <rect x="20" y="56" width="176" height="48" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="108" y="75" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Instance source</text>
  <text x="108" y="90" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">1 préparer · 2 pré-vol · 3 exporter</text>
  <!-- station -->
  <rect x="264" y="60" width="112" height="40" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="320" y="77" text-anchor="middle" font-size="10.5" fill="var(--sl-color-gray-1)">Station de</text>
  <text x="320" y="91" text-anchor="middle" font-size="10.5" fill="var(--sl-color-gray-1)">décontamination</text>
  <!-- instance de destination -->
  <rect x="444" y="56" width="176" height="48" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="532" y="75" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Instance de destination</text>
  <text x="532" y="90" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">5 importer — vérifier, puis pousser</text>
  <!-- trajet du média -->
  <line x1="196" y1="80" x2="260" y2="80" stroke="var(--sl-color-gray-3)" marker-end="url(#mwf-arrow)" />
  <line x1="376" y1="80" x2="440" y2="80" stroke="var(--sl-color-gray-3)" marker-end="url(#mwf-arrow)" />
  <text x="320" y="116" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-2)">4 transporter — média amovible</text>
  <!-- journaux de retour -->
  <text x="320" y="140" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">retour — journaux de la destination sur le même média</text>
  <line x1="440" y1="148" x2="200" y2="148" stroke="var(--sl-color-gray-3)" stroke-dasharray="4 4" marker-end="url(#mwf-arrow)" />
  <!-- registry de zone -->
  <rect x="460" y="166" width="144" height="36" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="532" y="188" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Registry de zone</text>
  <line x1="532" y1="104" x2="532" y2="162" stroke="var(--sl-color-gray-3)" marker-end="url(#mwf-arrow)" />
  <text x="540" y="140" font-size="9.5" fill="var(--sl-color-gray-3)">push vérifié</text>
  <!-- légende -->
  <text x="320" y="236" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-2)">Le matériel de confiance ne voyage jamais avec le contenu — les trust roots de la destination sont la seule autorité</text>
</svg>

La même application tourne des deux côtés.

- L'**instance source** tourne en mode miroir sur un poste connecté. Elle
  résout le Retriever de la zone, télécharge et vérifie le contenu, et
  l'écrit dans un store transportable : les artefacts, les recipes signées
  qui les justifient, les journaux d'opération et un manifeste de média,
  réunis dans un seul répertoire relogeable.
- L'**instance de destination** tourne dans la zone isolée. Pointée sur le
  répertoire transporté, elle re-vérifie tout depuis zéro et pousse le
  contenu de façon différentielle vers la registry de zone. Ses propres
  journaux d'opération sont écrits en retour sur le média : le média est
  aussi le canal d'audit au retour.

Rien d'autre ne traverse. Le matériel de confiance ne voyage jamais avec le
contenu — les trust roots configurées côté destination sont la seule
autorité (voir le [modèle de sécurité du média](../../air-gap/media-security/)).

## Les cinq étapes

Les noms des étapes sont stables : procédures, écrans et messages d'erreur
les emploient de façon cohérente — une procédure de site écrite avec ces
noms n'aura pas à être renommée plus tard.

| # | Étape | Ce qui se passe | Qui agit |
|---|-------|-----------------|----------|
| 1 | **Préparer** | Le poste source est configuré : mode miroir, Retriever de la zone, trust roots, liste blanche de registries. | admin |
| 2 | **Pré-vol** | Tobby calcule ce qui voyagerait et refuse les transferts impossibles avant qu'ils ne commencent. | operator |
| 3 | **Exporter** | Une synchronisation déclenchée manuellement remplit le store transportable ; le manifeste de média est écrit en dernier. | operator |
| 4 | **Transporter** | Le média traverse physiquement le sas, via les contrôles de gestion des supports du site. | procédure média du site (hors Tobby) |
| 5 | **Importer** | L'instance de destination vérifie le média, puis pousse le contenu vérifié vers la registry de zone. | operator (admin pour l'unique dérogation auditée) |

### 1 — Préparer

Une instance sert exactement une zone, dans exactement un mode, choisi au
démarrage. Préparer le poste source, c'est installer Tobby, sélectionner le
mode miroir, et configurer le Retriever de la zone, les trust roots et la
liste blanche de registries. Les secrets (credentials de registry, clés TLS)
vivent dans le répertoire d'état du poste — jamais dans le store
transportable.

Checklist :

- Le poste est installé selon la procédure d'installation hors-ligne de
  votre site.
- L'instance tourne en mode miroir et nomme le Retriever de la zone de
  destination.
- Les trust roots et la liste blanche de registries correspondent à la
  politique de la zone.
- Aucun fichier de secret ne réside sous le chemin du store transportable.
- Le média est formaté avec un système de fichiers adapté (pas de FAT32)
  et, si votre site l'exige, chiffré au niveau de l'OS (LUKS, BitLocker).

:::note[Conceptuel aujourd'hui]
Le choix du mode et la configuration de l'instance existent aujourd'hui. La
synchronisation miroir elle-même, et le refus de démarrer avec des secrets
sous le store, arrivent avec le jalon 5.
:::

### 2 — Pré-vol

Avant toute écriture, Tobby calcule le volume à transférer — par recipe,
dédupliqué par digest, net de ce que la cible détient déjà — et le compare à
l'espace libre du média. Il refuse de démarrer quand la projection ne tient
pas (en énonçant les octets manquants) et refuse les systèmes de fichiers
qui ne peuvent pas contenir la charge, comme FAT32 et sa limite de 4 Gio par
fichier.

Checklist :

- Le volume projeté, par recipe et total, a été relu.
- L'espace libre du média dépasse la projection plus la marge de sécurité.
- Le système de fichiers du média a été accepté par le pré-vol.
- Les refus éventuels ont été résolus par un prune ou un média plus grand —
  pas en sautant le contrôle (il n'y a pas de contournement).

:::note[À venir — jalon 5]
Le calcul de pré-vol et ses refus explicites sont un comportement du
jalon 5 (SRS FR-055), dry-run scriptable inclus. À suivre sur la page
[État du projet](../../discover/status/).
:::

### 3 — Exporter

L'opérateur déclenche la synchronisation manuellement — depuis l'interface
ou l'API, jamais sur un planning : en mode miroir, la préparation d'un média
est toujours un acte humain supervisé. Tobby télécharge ce qui manque,
vérifie signatures et digests à l'entrée, écrit tout dans le store sur le
média, et termine par l'écriture du manifeste de média : l'inventaire, les
recipes couvertes, l'identité de zone, le run ID et la version du format de
store.

Checklist :

- La synchronisation s'est terminée sans recipe bloquée, ou chaque recipe
  bloquée est comprise et son absence acceptée.
- Le manifeste de média a été écrit (c'est toujours la dernière écriture).
- Le run ID de la synchronisation est reporté dans votre dossier de
  transfert.
- Le média a été démonté proprement.

:::note[À venir — jalon 5]
La synchronisation miroir manuelle et le manifeste de média arrivent avec le
jalon 5 (SRS FR-014, FR-054).
:::

### 4 — Transporter

Tobby est volontairement absent de cette étape. Le média suit la procédure
de gestion des supports de votre site : chaîne de responsabilité, station de
décontamination ou d'inspection, enregistrement. La charge est conçue pour
survivre à l'inspection — fichiers ordinaires, checksums vérifiables, aucune
particularité exotique de système de fichiers — et pour être entièrement
re-vérifiée après, si bien que la station n'a pas besoin d'être de confiance
pour l'intégrité.

Checklist :

- La chaîne de responsabilité est documentée de l'export à l'import.
- Le média a passé la station de décontamination ou d'inspection du site.
- Le média est remis à l'opérateur de la zone isolée avec son dossier de
  transfert (zone, date, run ID).

Cette étape est purement organisationnelle : vous pouvez l'écrire et la
répéter dès aujourd'hui.

### 5 — Importer

L'instance de destination traite le média comme non fiable jusqu'à preuve du
contraire. La vérification précède tout push, tout service, toute écriture
locale : complétude et checksums du manifeste d'abord, puis signatures des
recipes contre les trust roots propres à la destination, puis chaque digest
d'ingrédient. Les recipes qui vérifient sont poussées de façon
différentielle vers la registry de zone ; tout le reste est bloqué et nommé.
Les journaux de la destination sont écrits en retour sur le média.

Checklist :

- L'identité de zone du média correspond à la zone de cette instance.
- Les verdicts de vérification ont été relus par étape et par recipe.
- Les recipes bloquées, s'il y en a, sont listées dans le rapport et
  traitées selon votre procédure de site — la seule dérogation est la
  dérogation admin auditée pour un désaccord d'identité de zone ; les échecs
  d'intégrité n'en ont aucune.
- Le push est terminé ; le média de retour porte les journaux de la
  destination.

#### L'écran Média

Tout ce qui précède a un écran : **Média**, dans la navigation principale, des
deux côtés du transfert. À la source, c'est la liste de colisage — à quelle
zone le support est adressé, quand il a été résolu, ce qu'il livre, ce qu'il
pèse — à lire avant de démonter le disque. À la destination, il ouvre
l'enchaînement guidé :

1. **Vérifier.** Relit et recalcule l'empreinte de chaque fichier couvert,
   puis vérifie la signature de chaque recipe contre les trust roots de *cette*
   instance. Sur un disque plein cela prend plusieurs minutes : la vérification
   tourne en arrière-plan avec une progression en direct, vous pouvez quitter
   la page et y revenir.
2. **Rapport.** Les trois étapes nommées séparément — complétude et sommes du
   manifeste, empreintes des ingrédients, signatures des recipes — et un
   verdict par livraison. Une livraison bloquée nomme le fichier fautif, ce qui
   fait la différence entre « recopier le disque » et « appeler la zone
   source ». Le rapport brut se télécharge en JSON.
3. **Pousser.** Le bouton n'existe pas tant qu'un verdict n'a pas validé au
   moins une livraison. Pas grisé : absent.

Un désaccord de zone et un support plus ancien que le dernier importé ici sont
les deux seuls refus qu'un administrateur peut lever, depuis l'étape Vérifier,
avec consignation au journal d'audit. Les verdicts d'intégrité et de signature
n'admettent aucune dérogation, pour personne.

#### Un support non vérifié ne sert rien

Une instance de destination démarrée sur un store transporté n'en sert pas le
contenu tant que la vérification ne l'a pas validé — le « tout service » de la
règle ci-dessus, appliqué et pas seulement écrit. `/v2/` et `/files/` répondent
`403` avec [TBY-MED-030](../../reference/errors/#tby-med-030) et la marche à
suivre ; l'interface, l'API et les sondes restent disponibles, puisque ce sont
elles qu'il faut pour vérifier. L'instance est **vivante et prête** : `/readyz`
répond `200` et indique dans son corps quelles surfaces sont fermées.

Le verrou s'ouvre sur un support intact et sur rien d'autre. Un support
partiellement endommagé livre quand même ses recipes intactes dans la registry
de zone — c'est tout l'intérêt de porter plusieurs livraisons sur un disque —
mais cette instance ne servira pas depuis le disque, parce que `/v2/` distribue
des blobs et qu'un blob atteint par une recipe bloquée est exactement le
contenu qui a échoué. Aucun réglage ne permet de servir un support non
vérifié ; le verdict n'est pas non plus conservé d'un redémarrage à l'autre.

Une instance miroir **côté source** n'est pas concernée : son store porte un
manifeste de média parce qu'elle en a écrit un, et elle sert normalement. Les
deux côtés se distinguent par l'identité de zone, que seule une instance de
destination configure.

## Après l'import

La registry de zone sert désormais le contenu. Y raccorder les clusters et
les hôtes de la zone fonctionne exactement comme en mode passthrough — voir
[Brancher vos clients](../../passthrough/connect-clients/).

<!-- TODO: checklists imprimables par étape générées au build depuis cette page -->
