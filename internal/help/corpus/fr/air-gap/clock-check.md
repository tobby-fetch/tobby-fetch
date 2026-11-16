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
| **Le build** | La date de build du binaire (`SOURCE_DATE_EPOCH` du build reproductible). | Au démarrage, puis de nouveau toutes les cinq minutes tant que l'instance tourne. |
| **Le support** | L'heure de résolution écrite dans le manifeste du support — la **plus récente** parmi les supports vérifiés depuis le démarrage. | À chaque vérification ou import d'un support — `tobby media verify`, `tobby media import`, l'écran Support, l'API — puis de nouveau toutes les cinq minutes. |

Une date prouvée passée le reste : ouvrir ensuite un support plus ancien, ou un
support dont le manifeste ne porte aucune heure de résolution, ne remplace pas
l'ancre d'un support plus récent, et ne peut donc pas masquer un écart déjà
constaté. Un support dont la vérification a **bloqué** n'est pas une ancre du
tout — le blocage peut venir de sa signature, et une date lue dans un tel
manifeste n'est pas crue. Le réexamen périodique attrape une horloge reculée
pendant que l'instance tourne, et lève l'avertissement une fois l'horloge
corrigée.

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
  d'elle-même dès que l'horloge n'est plus en retard sur les ancres — au
  réexamen suivant — sans acquittement ni redémarrage.
- **La ligne de commande** : `tobby media verify` et `tobby media import`
  impriment l'avertissement sur la sortie d'erreur — le rapport sur la sortie
  standard reste le rapport — et le verdict est inchangé.
- **Une métrique** : `tobby_clock_implausible{anchor="build|medium"}`, `1` ou
  `0`, exposée dès que l'ancre a été contrôlée. Il existe une
  [alerte](../../passthrough/monitoring/).
- **Le journal d'audit** : un événement `clock.implausible`, dont la cible est
  `build` ou le support et dont le détail porte l'écart, la tolérance et le code.

`GET /api/v1/clock` renvoie le verdict courant de chaque ancre à tout rôle
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
confiance. L'avertissement disparaît de lui-même en cinq minutes au plus, sans
redémarrage. Si c'est le **support** qui est en avance, l'horloge de la zone source
est la suspecte : dites-le à qui l'a produit.
