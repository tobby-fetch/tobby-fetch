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

<!-- TODO: diagram: instance source (poste connecté) et instance de destination (zone isolée), le média amovible traversant le sas via la station de décontamination, les cinq étapes annotées le long du chemin, les journaux de retour repartant sur le même média -->

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

:::note[À venir — jalon 5]
L'écran Média côté destination, ses verdicts par recipe et l'enchaînement
guidé Vérifier → Rapport → Pousser arrivent avec le jalon 5 (SRS FR-052,
FR-054). L'ordre de vérification, lui, est déjà normatif — voir le
[modèle de sécurité du média](../../air-gap/media-security/).
:::

## Après l'import

La registry de zone sert désormais le contenu. Y raccorder les clusters et
les hôtes de la zone fonctionne exactement comme en mode passthrough — voir
[Brancher vos clients](../../passthrough/connect-clients/).

<!-- TODO: checklists imprimables par étape générées au build depuis cette page -->
