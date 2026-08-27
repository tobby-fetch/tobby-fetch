---
title: Modèle de sécurité du média
description: Pourquoi le manifeste de média est délibérément non signé, ce que la destination vérifie et dans quel ordre, et ce qui ne voyage jamais sur le média.
sidebar:
  order: 5
---

Cette page est le modèle de sécurité du média amovible : ce qui voyage, ce
qui est vérifié, par qui, et contre quelle autorité. Elle est écrite pour le
relecteur sécurité qui doit approuver une procédure de transfert par média.

Le modèle est acté et spécifié
([ADR-0006](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0006-removable-media-transport.md),
[ADR-0007](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0007-signing-cosign-key-based.md),
[ADR-0016](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/adr/ADR-0016-media-manifest.md),
SRS FR-054), et implémenté tel qu'il est décrit ici. Le travail
d'homologation peut s'appuyer sur cette page.

## Ce qui voyage sur le média

Le store transportable embarque, dans un seul répertoire autonome :

- les **artefacts** — images, charts, artefacts OCI, filesets — dans un
  store OCI adressé par contenu ;
- les **recipes signées** qui justifient chaque artefact, avec leurs
  signatures cosign attachées ;
- les **journaux d'opération** de la synchronisation qui a produit le
  store, sous `_tobby/` ;
- le **manifeste de média** (`meta/media.json`) : inventaire de chaque
  fichier couvert avec sa taille et son SHA-256, recipes couvertes avec
  leurs digests épinglés, identité de zone, identifiant du support,
  horodatage de résolution, version productrice et run, et version du
  format de store.

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

Un second mécanisme est ce qui rend le premier inoffensif. Chaque fichier
couvert est contrôlé **contre sa propre adresse de contenu** en plus de son
entrée d'inventaire. Un attaquant qui corrompt un blob et réécrit
l'inventaire pour qu'il concorde met en défaut l'inventaire et se fait
quand même prendre par le digest sous lequel le contenu est rangé
([`TBY-MED-015`](../../reference/errors/#tby-med-015)). Retirer ce contrôle
rendrait le manifeste non signé porteur — précisément ce que cette section
refuse.

Ce que l'inventaire non signé vous apporte réellement :

- des **échecs rapides et localisés avec précision** — quel fichier, quelle
  recipe — au lieu d'une erreur de vérification générique au moment du
  push ;
- un **inventaire à lire** avant de brancher le support en zone isolée, et
  une réponse de complétude que le contenu seul ne peut pas donner : un blob
  qui n'a pas traversé est sinon indiscernable d'un blob qui n'avait rien à
  faire là ;
- deux **gardes anti-accident** : l'identité de zone et l'horodatage de
  résolution permettent à la destination de refuser un média préparé pour
  une autre zone, ou plus ancien que le dernier qu'elle a importé, avant
  toute autre chose.

### La couverture s'arrête là où le média est écrit après coup

L'inventaire couvre chaque fichier ordinaire sous l'arborescence de la
registry et sous `meta/`, à l'exception de `meta/media.json` lui-même — un
fichier ne peut pas s'inventorier lui-même. Tout ce qui est sous `_tobby/`
est **hors couverture** par construction : la zone de tâches, les journaux
d'opération et les journaux de retour de la destination. Des fichiers qui
continuent d'être écrits après la prise de l'inventaire ne peuvent pas y
figurer sans l'invalider à la ligne suivante qu'ils reçoivent.

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

1. **Le manifeste** — il se parse, son propre format et la version du format
   de store sont lisibles par ce build, ses chemins sont bien formés, et les
   gardes d'identité de zone et de fraîcheur sont évaluées.
2. **Chaque recipe** — chaque fichier qu'elle atteint est contrôlé contre son
   entrée d'inventaire *et* contre sa propre adresse de contenu, puis la
   signature cosign de la recipe est vérifiée contre les trust roots de la
   destination.
3. **Un balayage final** — pour le contenu présent et non comptabilisé, ou
   inventorié et atteint par aucune recipe vérifiée.

L'ordre est délibéré : les échecs d'intégrité sont bon marché à détecter et
nomment le fichier exact, ils sortent donc en premier ; la vérification des
signatures s'exécute ensuite sur un store réputé intact au bit près.

Le blocage est tout aussi fixe :

- La vérification et la décision se font **par recipe** (R-19) : une recipe
  dont la signature vérifie et dont chaque fichier atteint correspond à son
  digest épinglé est poussable ; une recipe qui échoue à l'un des deux est
  bloquée **entière**, sans dérogation, et nommée dans le rapport avec le
  fichier qui a décidé. Une livraison vérifiée à moitié n'est pas une
  livraison — mais un support qui porte plusieurs livraisons livre quand
  même celles qui sont intactes.
- Un **échec d'intégrité ou de signature n'a aucune dérogation**. Pas de
  drapeau, pas de boîte de confirmation, pas de chemin admin autour d'un
  média corrompu, pour aucun rôle.
- **Quatre refus restent globaux**, parce qu'un sauvetage par recipe n'y a
  aucun sens : un manifeste absent, illisible ou dans un format non
  supporté, et un graphe de recipes altéré, bloquent tout sans dérogation ;
  un support adressé à une autre zone, et un support plus ancien que le
  dernier importé ici, bloquent tout et sont les **deux seuls** qu'un
  administrateur peut lever, chaque levée étant écrite au
  [journal d'audit](../../security/audit-log/) avec l'acteur et l'origine.

Les deux refus levables sont des gardes anti-accident sur une affirmation
*non signée*, pas des contrôles de sécurité — c'est exactement pour cela que
ce sont les deux que l'on peut lever, et que rien de ce qui repose sur la
cryptographie ne le peut. La table code par code complète est sur
[importer côté zone isolée](../../air-gap/import-destination/).

### Servir fait partie de l'ordre

« Précède tout push, tout **service** et toute écriture locale » compte trois
verbes. Une instance de destination qui détient un support transporté retient
`/v2/` et `/files/` — en entier, pour tous les rôles, administrateurs
compris — tant qu'une vérification *qu'elle a menée* n'a pas validé le
support, et répond `403` avec
[`TBY-MED-030`](../../reference/errors/#tby-med-030) et la marche à suivre.
Le verrou ne s'ouvre que sur un support intégralement validé, ne conserve
aucune trace persistante, se referme à chaque redémarrage, et n'a aucun
réglage de contournement.

## Les secrets ne voyagent jamais

Les fichiers de secrets — credentials de registry, clés privées TLS, mots de
passe de proxy — appartiennent à une instance, pas à un transfert. Ils
vivent dans le répertoire d'état de l'instance et ne sont jamais écrits sous
le store transportable. Le répertoire transporté contient du contenu, des
signatures, des journaux et le manifeste ; rien dedans n'authentifie
personne.

La règle est un contrôle appliqué, pas une consigne (NFR-020, R-16) : une
instance **refuse de démarrer**
([`TBY-CFG-002`](../../reference/errors/#tby-cfg-002)) quand `state.root`,
`registries.credentialsFile` ou `server.tls.keyFile` se résout dans le
store. La comparaison passe par le système de fichiers réel — chemins
relatifs, `..` et liens symboliques compris — si bien qu'un chemin qui se lit
« dehors » et atterrit dedans est attrapé, et le refus nomme à la fois le
réglage et le chemin résolu. Les fichiers porteurs de secrets sont créés
accessibles au seul propriétaire. Détails dans
[Secrets](../../security/secrets/).

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
- Exactement deux refus peuvent être levés, par un administrateur, audités,
  et tous deux portent sur des affirmations non signées ; les échecs
  d'intégrité et de signature n'admettent aucune dérogation, pour personne.
- Rien n'a été servi depuis le support avant sa vérification — la registry
  et la surface de fichiers restent fermées jusque-là, et aucun réglage ne
  les rouvre.
- La vue contrôle par contrôle, avec l'état de livraison, est sur le
  [modèle de sécurité en une page](../../security/one-pager/).
