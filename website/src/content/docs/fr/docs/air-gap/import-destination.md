---
title: Importer côté zone isolée
description: L'écran Média et son enchaînement Vérifier → Rapport → Pousser, ce qui bloque une recipe et ce qui bloque tout un support, les deux refus levables, et pourquoi un support non vérifié ne sert rien.
sidebar:
  order: 3
---

L'instance de destination, c'est la même application, en zone isolée,
pointée sur le store transporté. Elle traite le support comme non fiable
jusqu'à preuve du contraire, et l'ordre est la garantie plutôt qu'une suite
d'étapes : **rien n'est poussé, servi ni écrit avant que tout le support
n'ait été re-vérifié** (FR-054).

## Ce qui fait d'une instance une destination

Un réglage : `zone:` — l'identité de la zone que cette instance sert, le
`metadata.name` du Retriever qui la décrit.

```yaml
mode: mirror
zone: isolated-production
storage:
  root: /mnt/usb/tobby-store
state:
  root: /var/lib/tobby/state
destination:
  registry: registry.zone.example.com
```

Une instance côté source lit sa zone dans le Retriever qu'elle résout et ne
pose rien ici. Une destination n'a pas de Retriever — son contenu arrive sur
un support — et sans `zone:` elle ne peut pas savoir si un support lui est
adressé. `tobby media verify`, `tobby media import` et les quatre points
d'accès `/api/v1/media` refusent de s'exécuter sans elle, en nommant le
réglage ([`TBY-CFG-001`](../../reference/errors/#tby-cfg-001)).

Vérifier ne demande rien d'autre. **Importer exige en plus
`destination.registry`** : la vérification lit, le push écrit, et le push
doit avoir où aller.

## L'écran Média

`/media` est le pendant guidé du pipeline de vérification. Il ouvre sur la
synthèse d'inventaire du support — la zone à laquelle il est adressé, quand
la livraison a été résolue, quelles recipes il porte, combien de fichiers et
d'octets — puis sur une séquence numérotée de trois étapes.

**1 — Vérifier.** Relit et recalcule l'empreinte de chaque fichier couvert,
puis vérifie la signature de chaque recipe contre les trust roots de *cette*
instance. Sur un disque plein, c'est plusieurs minutes d'entrées-sorties :
la vérification tourne en arrière-plan avec une progression en direct et la
page interroge le serveur, vous pouvez fermer l'onglet et revenir. Demander
une seconde vérification pendant qu'une autre parcourt le support est refusé
([`TBY-MED-031`](../../reference/errors/#tby-med-031), HTTP `409`) — deux
parcours du même disque se ralentissent l'un l'autre et n'apprennent rien de
neuf.

**2 — Rapport.** Trois étapes nommées séparément — complétude et sommes du
manifeste, empreintes des ingrédients, signatures des recipes — et un
verdict par livraison : `pushable`, `partial` ou `blocked`. Une livraison
bloquée nomme le fichier fautif, ce qui fait la différence entre « recopier
le disque » et « appeler la zone source ». Le rapport brut est le document
JSON que sert `GET /api/v1/media/verification`.

**3 — Pousser.** Le bouton **n'existe pas** tant qu'un verdict n'a pas
validé au moins une livraison. Pas grisé : absent du document. Ce qui est
alors poussé passe par les mêmes contrôles qu'une promotion passthrough — la
liste blanche de registries et les signatures des recipes, re-contrôlées sur
les octets exacts qui vont partir — seul ce qui manque à la registry de zone
bouge, et les recipes signées atterrissent dans le cookbook de la zone avec
leurs signatures.

<!-- TODO: screenshot: l'écran Média côté destination — l'enchaînement en trois étapes avec une vérification en cours et sa progression -->

Lire l'écran est une action `viewer`. Vérifier et importer sont des actions
`operator`. Lever l'un des deux refus ci-dessous est une action
d'administrateur, et les cases de dérogation ne sont rendues que pour lui.

## Ce qui bloque quoi

L'unité de blocage est la **recipe**. Une recipe dont la signature vérifie
et dont chaque fichier atteint correspond à son digest épinglé est
poussable ; une recipe qui échoue à l'un des deux est bloquée entière, sans
dérogation, et nommée dans le rapport avec le fichier qui a décidé. Ses
voisines sur le même support ne sont pas touchées.

C'est délibéré. Une livraison vérifiée à moitié n'est pas une livraison —
une recipe est une signature sur les octets exacts d'un ensemble — mais
retenir ce qui a échoué n'est pas la même chose que jeter ce qui a réussi,
et un support qui porte plusieurs livraisons livre quand même celles qui
sont intactes. L'alternative, bloquer tout un voyage physique pour un octet
corrompu, est ce qui pousse les exploitants vers les dérogations, et la
dérogation qui compterait ici est précisément celle que ce produit refuse
d'offrir.

Refus par recipe, dérogeables par personne :

| Code | Condition |
|---|---|
| [`TBY-MED-010`](../../reference/errors/#tby-med-010) | un fichier atteint par la recipe n'est pas sur le support |
| [`TBY-MED-011`](../../reference/errors/#tby-med-011) | la taille d'un fichier couvert diffère de son entrée d'inventaire |
| [`TBY-MED-012`](../../reference/errors/#tby-med-012) | le contenu d'un fichier couvert ne donne pas l'empreinte de son entrée d'inventaire |
| [`TBY-MED-013`](../../reference/errors/#tby-med-013) | un fichier atteint par la recipe que l'inventaire ne liste pas |
| [`TBY-MED-014`](../../reference/errors/#tby-med-014) | un manifest ou un index atteignable qui ne se parse pas |
| [`TBY-MED-015`](../../reference/errors/#tby-med-015) | un blob dont les octets ne donnent pas le digest que son propre chemin annonce |

Le dernier est ce qui empêche l'inventaire non signé d'être porteur : un
attaquant qui corrompt un blob et réécrit l'inventaire pour qu'il concorde
met en défaut l'inventaire et se fait quand même prendre par l'adresse de
contenu.

### Quatre refus restent globaux

Un sauvetage par recipe n'a aucun sens pour eux : ils bloquent tout.

| Condition | Code | Dérogation |
|---|---|---|
| Manifeste absent, illisible, ou dans un format que ce build ne lit pas | [`TBY-MED-001`](../../reference/errors/#tby-med-001), [`TBY-MED-002`](../../reference/errors/#tby-med-002), [`TBY-MED-003`](../../reference/errors/#tby-med-003), [`TBY-MED-004`](../../reference/errors/#tby-med-004) | **aucune** |
| Le graphe de recipes (`meta/recipes.json`) ne correspond pas à son entrée d'inventaire | [`TBY-MED-005`](../../reference/errors/#tby-med-005) | **aucune** |
| Le support est adressé à une autre zone | [`TBY-MED-006`](../../reference/errors/#tby-med-006) | administrateur, auditée |
| Le support est plus ancien que le dernier importé pour cette zone | [`TBY-MED-007`](../../reference/errors/#tby-med-007) | administrateur, auditée |

Les deux premiers ne laissent rien à raisonner — sans l'inventaire il n'y a
pas de question de complétude à poser, et le graphe *est* l'ensemble
d'atteignabilité, si bien qu'un graphe altéré rend tout verdict par recipe
sans objet.

Les deux derniers s'adressent à quelqu'un d'autre, ou à un moment antérieur.
Ce sont des **gardes anti-accident, pas des contrôles de sécurité** : le
manifeste est non signé, donc une partie hostile peut forger l'un ou l'autre
champ. C'est exactement pour cela qu'ils sont les deux seuls qu'un
administrateur peut lever — `--allow-zone-mismatch` et `--allow-stale` en
ligne de commande, des cases sur l'écran, `allowZoneMismatch` et
`allowStale` sur l'API — et que la tentative comme la levée appliquée sont
écrites au [journal d'audit](../../security/audit-log/) avec l'acteur et
l'origine.

**Aucun rôle ne lève un verdict d'intégrité ou de signature.** Il n'existe
ni drapeau, ni boîte de confirmation, ni clé de configuration pour cela,
pour personne, administrateurs compris.

### Des constats qui ne bloquent rien

Trois conditions sont rapportées et jamais poussées, sans rien bloquer : un
fichier sous couverture du manifeste que l'inventaire ne liste pas
([`TBY-MED-020`](../../reference/errors/#tby-med-020)), un fichier
inventorié qu'aucune recipe vérifiée n'atteint
([`TBY-MED-021`](../../reference/errors/#tby-med-021)), et un fichier de
comptabilité couvert autre que le graphe de recipes qui ne correspond pas à
son entrée d'inventaire
([`TBY-MED-022`](../../reference/errors/#tby-med-022)).

Il n'y a pas de porte dérobée pour les artefacts en vrac : le contenu que le
support porte et qu'aucune recipe vérifiée n'atteint est rapporté nommément
et reste où il est.

## Un support non vérifié ne sert rien

« La vérification précède tout push, tout **service** et toute écriture
locale » compte trois verbes. Une instance de destination qui détient un
support transporté retient `/v2/` et `/files/` tant qu'une vérification
qu'elle a menée n'a pas validé le support.

- Les deux surfaces répondent **`403`** avec
  [`TBY-MED-030`](../../reference/errors/#tby-med-030) — ou
  [`TBY-MED-032`](../../reference/errors/#tby-med-032) une fois qu'une
  vérification a eu lieu et que le support n'est pas ressorti intact — dans
  la forme que comprennent les clients de chaque surface : l'enveloppe
  d'erreur OCI pour `docker` et `helm`, du texte brut pour `apt` et `dnf`.
  Jamais un `404`, jamais un `503` silencieux. Le refus nomme le support et
  l'écran qui ouvre le verrou.
- **L'instance reste vivante et prête.** `/healthz` et `/readyz` répondent
  tous deux `200`, et `/readyz` indique dans son corps quelles surfaces sont
  fermées et où les ouvrir. Un `503` sortirait l'instance de la rotation et
  retirerait l'écran même qui corrige la situation.
- **Le verrou s'ouvre sur un support intégral et sur rien d'autre.** Un
  verdict `partial` ne l'ouvre pas. La décision de push est par recipe parce
  qu'une recipe est une livraison ; servir n'est pas cette décision, parce
  que `/v2/` distribue des blobs et qu'un blob atteint par une recipe
  bloquée est exactement la plage d'octets qui a échoué. Poussez les recipes
  intactes dans la registry de zone, qui les sert alors depuis un contenu
  arrivé par le chemin contrôlé.
- **Il n'y a aucun contournement, et aucun verdict ne survit à un
  redémarrage.** Un verdict en cache dit que ces octets étaient bons une
  fois ; la question que pose le verrou, c'est s'ils le sont maintenant.
  Recalculer l'empreinte d'un disque au redémarrage, c'est des minutes.
- **Aucun rôle ne le contourne.** Un verrou fermé n'est pas un refus
  d'autorisation : la matrice des rôles décide qui peut demander, le verrou
  décide si cette instance est prête à distribuer quoi que ce soit.

Une instance miroir côté source n'est pas concernée. Son store porte lui
aussi un manifeste — elle l'a écrit — mais le support est sa propre sortie
plutôt qu'un objet qui a changé de mains, et elle n'a pas de `zone:`
configurée, ce qui est précisément la manière dont l'exigence distingue les
deux côtés. Une instance passthrough n'est pas concernée pour la même
raison : son store est un cache de transit, pas une livraison.

## En ligne de commande

Les deux commandes re-vérifient d'abord, et refusent toutes deux de tourner
sans zone.

```sh
tobby media verify --storage-root /mnt/usb/tobby-store --zone isolated-production
tobby media verify --storage-root /mnt/usb/tobby-store --zone isolated-production --output json | jq .verdict
tobby media import --config /etc/tobby/config.yaml
```

`tobby media verify` rapporte et n'écrit **rien du tout** — pas même le
journal d'opération du support. `tobby media import` fait tout le parcours
et consigne ce qu'il a fait sur le support, sous `_tobby/logs/` et donc hors
de la couverture du manifeste : ce fichier est le canal d'audit de retour du
transfert.

| Commande | `0` | `1` | `3` | `4` |
|---|---|---|---|---|
| `tobby media verify` | toutes les livraisons sont poussables | — | refusé par la politique (identité de zone, fraîcheur) | un échec de vérification |
| `tobby media import` | importé | un push a échoué | refusé par la politique | un échec de vérification |

Ces commandes tournent dans leur propre processus, directement contre le
répertoire du store. **Elles n'ouvrent pas les surfaces d'une instance qui
tourne** : le verrou de service s'ouvre sur une vérification que *cette
instance-là* a menée, depuis son écran ou par
`POST /api/v1/media/verify`. Le message de refus le dit, parce que la
confusion est facile.

## Après l'import

Un import terminé fait avancer le registre de fraîcheur de la zone — ce qui
fait de la réimportation du support du mois dernier un refus plutôt qu'un
retour en arrière silencieux — et la registry de zone sert désormais le
contenu transféré. Y raccorder les clusters et les hôtes de la zone
fonctionne à l'identique dans les deux modes : voir
[brancher vos clients](../../passthrough/connect-clients/).

Les supports qui repartent pour un second cycle, et l'entretien qui leur
garde une taille raisonnable, sont sur
[gérer les supports dans la durée](../../air-gap/manage-media/).
