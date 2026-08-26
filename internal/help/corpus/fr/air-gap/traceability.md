---
title: Tracer et prouver un transfert
description: Comment un run ID suit un transfert à travers l'air gap, et comment le média porte lui-même la preuve dans les deux sens.
sidebar:
  order: 3
  badge:
    text: Partiel
    variant: caution
---

Un transfert à travers l'air gap doit être prouvable après coup : ce qui a
traversé, quand, préparé par quel run, vérifié avec quels verdicts. La
réponse de Tobby : **la preuve voyage avec le contenu** — les journaux
structurés et le manifeste vivent sur le média lui-même, corrélés par un
identifiant de run unique du poste source jusqu'à la registry de
destination.

## Un run ID, de bout en bout

Chaque run de synchronisation reçoit un **run ID** unique au démarrage. Il
est porté par chaque enregistrement de journal du run, à côté des autres
champs de corrélation : task ID, recipe, ingrédient, digest (SRS FR-090).
Filtrer les journaux sur un run ID reconstitue ce run intégralement.

Le même run ID traverse ensuite le sas :

1. **À la source** — chaque ligne de journal de la synchronisation miroir le
   porte.
2. **Sur le média** — le manifeste de média l'enregistre, à côté de
   l'identité de zone et de l'horodatage de résolution.
3. **À la destination** — l'instance qui ouvre le store transporté réutilise
   le run ID dans ses propres journaux pendant qu'elle vérifie et pousse.

Un seul identifiant relie donc la préparation, la charge et l'import — à
travers deux machines qui n'ont jamais partagé un réseau.

:::note[À venir — jalon 5]
Les journaux JSON corrélés et le run ID existent aujourd'hui (livrés avec le
socle v0.1.x). Le manifeste de média qui enregistre le run ID, et sa
réutilisation par l'instance de destination, arrivent avec le jalon 5 (SRS
FR-054, FR-090). À suivre sur la page [État du projet](../../discover/status/).
:::

## Des journaux JSON durables sur le média

En mode miroir, les journaux d'opération ne partent pas sur stdout : ils
sont écrits **dans un fichier à l'intérieur du store transporté** (chemin
configurable), pour que la destination puisse auditer ce que contient le
média et comment il a été produit (SRS FR-053).

Parce qu'un média amovible peut être arraché, le fichier de journal est tenu
par un contrat de durabilité (SRS FR-056) :

- un **fsync explicite à chaque frontière de tâche** — un média arraché ou
  défaillant perd au plus les entrées de la tâche en cours ;
- une **rotation par taille** — le journal reste dans son budget configuré,
  sur des supports où l'espace est disputé par construction.

Les journaux sont en JSON Lines aux clés stables : exploitables par votre
SIEM ou par `jq`, sans deviner le format.

:::note[À venir — jalon 5]
Le journal en fichier sur le store de transport et son contrat de durabilité
sont un comportement du jalon 5. Le schéma de journal et les champs de
corrélation sont ceux qui sont déjà livrés aujourd'hui.
:::

## Les événements de sécurité sur le même canal

Les événements d'audit de sécurité — authentification, cycle de vie des
comptes et des tokens, changements de configuration sensibles, et les
dérogations média auditées — utilisent un schéma dédié à six champs
(acteur, action, cible, résultat, horodatage, origine) et voyagent sur le
même canal que les journaux d'opération, séparables par un champ marqueur
stable. Sur une instance miroir, ce canal est le fichier sur le média : **la
piste d'audit traverse l'air gap avec le contenu dont elle rend compte.** Le
schéma et ses garanties sont décrits dans le
[journal d'audit](../../security/audit-log/).

## L'exploitation au retour

Le média est un canal d'audit à double sens :

- **À l'aller**, il porte les journaux côté source du run qui a produit la
  charge.
- **Côté zone isolée**, l'instance de destination écrit ses propres journaux
  — verdicts de vérification, pushes, toute dérogation — sur le média, dans
  un chemin dédié *hors* couverture du manifeste, pour que l'inventaire de
  l'aller reste vérifiable au retour.
- **Au retour**, le côté connecté relit les journaux de la destination :
  filtrez sur le run ID et vous tenez l'histoire complète du transfert, les
  deux côtés inclus, sans qu'aucun lien réseau n'ait existé.

En pratique : archivez le contenu de `logs/` du média (les deux sens) avec
le dossier de transfert, indexé par run ID. Cette archive est votre preuve
rejouable pour une revue de sécurité.

Une limite honnête, dite clairement : comme le manifeste, les journaux ne
sont **pas signés**. Ce sont des preuves opérationnelles, pas une ancre de
confiance — l'authenticité du contenu lui-même ne repose jamais sur eux
(voir le [modèle de sécurité du média](../../air-gap/media-security/)).

## Bordereau de transfert imprimable

Les sites qui escortent leurs médias avec du papier obtiennent un document
de premier rang : un bordereau de transfert imprimable et bilingue, dérivé
de l'écran Média — synthèse du support, rapport de vérification, résultats
d'analyse — exportable en HTML ou en texte, et clairement marqué comme
**aide non signée**.

:::note[À venir — jalon 6]
Le bordereau de transfert (R-07) arrive avec le jalon 6, une fois livré au
jalon 5 l'écran Média dont il dérive. D'ici là, le run ID et les journaux
sur média ci-dessus sont l'ossature de traçabilité sur laquelle bâtir une
procédure papier.
:::
