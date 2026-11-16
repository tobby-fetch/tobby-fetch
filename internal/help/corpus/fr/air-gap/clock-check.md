---
title: Détecter une horloge fausse
description: Pourquoi une zone isolée ne peut pas se fier à son horloge, les deux dates auxquelles Tobby la compare, et les cinq endroits où une horloge invraisemblable est signalée — sans la corriger.
sidebar:
  order: 10
---

Une zone isolée n'a aucune source de temps fiable, et une horloge fausse
fausse en silence tout ce qui est jugé contre elle : l'âge de la base de
vulnérabilités, la garde de fraîcheur des supports, l'expiration des jetons et
des dérogations, la validité des certificats. Tobby ne peut pas connaître
l'heure juste. Il détient en revanche deux dates dont il peut prouver, sans
aucun réseau, qu'elles sont passées, et il contrôle l'horloge locale contre
elles.

## Les deux ancres

| Ancre | D'où elle vient | Contrôlée |
|---|---|---|
| **Le build** | La date de build du binaire (`SOURCE_DATE_EPOCH` du build reproductible). | Au démarrage. |
| **Le support** | L'heure de résolution écrite dans le manifeste du support. | À chaque vérification ou import d'un support — `tobby media verify`, `tobby media import`, l'écran Support, l'API. |

Une horloge locale **antérieure** à une ancre de plus de `clock.skewTolerance`
(`1h` par défaut, entre `0` et `7d`) est **invraisemblable**. La tolérance
absorbe la dérive ordinaire entre zones. Un écart égal à la tolérance reste
vraisemblable. Un build de développement ne porte aucune date de build et ne
prouve rien : il n'enregistre aucun verdict de build.

## Ce qui est montré

Quand l'horloge est invraisemblable, Tobby le dit à cinq endroits et **ne change
rien d'autre** :

- **Un enregistrement de journal** de niveau avertissement dans le journal de
  démarrage (ou celui de la vérification), avec le code
  [`TBY-CLK-001`](../../reference/errors/#tby-clk-001) (build) ou
  [`TBY-CLK-002`](../../reference/errors/#tby-clk-002) (support, qui le nomme),
  l'écart et la tolérance.
- **Une bannière** sur chaque page de l'interface, qui nomme le code, l'heure
  locale, l'écart et l'ancre. Elle est relue à chaque page : elle disparaît
  d'elle-même quand un contrôle ultérieur est vraisemblable, sans acquittement ni
  redémarrage.
- **La ligne de commande** : `tobby media verify` et `tobby media import`
  impriment l'avertissement sur la sortie d'erreur — le rapport sur la sortie
  standard reste le rapport — et le verdict est inchangé.
- **Une métrique** : `tobby_clock_implausible{anchor="build|medium"}`, `1` ou
  `0`, exposée dès que l'ancre a été contrôlée. Il existe une
  [alerte](../../passthrough/monitoring/).
- **Le journal d'audit** : un événement `clock.implausible`, dont la cible est
  `build` ou le support et dont le détail porte l'écart, la tolérance et le code.

`GET /api/v1/clock` renvoie le dernier verdict de chaque ancre à tout rôle
connecté.

## Ce que cela ne fait pas

Cela ne corrige pas l'horloge, et ne prend aucune décision. Les expirations et
les contrôles de fraîcheur continuent d'être jugés contre l'horloge locale,
désormais visiblement mise en doute : un support n'est pas refusé à cause de
cela, une dérogation n'expire pas plus tôt. Une horloge en **avance** sur la
vérité n'est pas détectée — rien de ce que l'instance détient ne prouve de
combien. L'heure de résolution d'un support est elle-même écrite par l'horloge
de la zone source : si celle-là était fausse, le contrôle trompe dans l'autre
sens, c'est pourquoi l'avertissement nomme le support.

## Que faire

Vérifiez l'horloge de l'hôte contre une référence de confiance de la zone — un
serveur de temps local, ou une valeur posée à la main depuis une source de
confiance — puis redémarrez pour le contrôle du build, ou vérifiez de nouveau le
support. Si c'est le **support** qui est en avance, l'horloge de la zone source
est la suspecte : dites-le à qui l'a produit.
