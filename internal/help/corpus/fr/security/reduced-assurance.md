---
title: Le marqueur d'assurance réduite
description: Comment le contenu admis sous un périmètre de confiance assoupli reste visible — rapports de tâche, API, interface, journaux, métriques et manifeste du média — et pourquoi la destination relit le marqueur comme une déclaration, jamais comme un verdict.
sidebar:
  order: 7
---

Un [périmètre d'assouplissement](../content-trust/) est visible comme un
*réglage* : un bandeau, la configuration effective. Ce n'est pas suffisant. Ce
qu'il a laissé entrer doit rester visible lui aussi, élément par élément, bien
après la synchronisation qui l'a admis — ou le jour où quelqu'un demande
« laquelle de ces recipes n'a jamais été signée ? ».

Le **marqueur d'assurance réduite** (SRS FR-037) est cette visibilité, étendue
de la configuration au contenu. C'est le principe du « sécurisé par défaut »
(FR-075) : ce qui a été admis avec moins d'assurance n'est **jamais présenté
comme s'il avait une assurance complète**.

## Ce qui porte le marqueur

Une recipe admise **sans signature** par un périmètre déclaré, et chaque
élément qu'elle atteint, porte un marqueur en trois parties : le niveau
(`reduced`), le nom du périmètre qui l'a admise et la raison (`unsigned`). Une
recipe dont la signature a été vérifiée contre une clé de confiance n'en porte
aucun — l'absence signifie l'assurance complète.

| Surface | Comment le marqueur apparaît |
|---|---|
| **Rapport de tâche** | Le membre `assurance` de chaque élément de la recipe : son artefact, ses ingrédients, ses deux moitiés de promotion, son verdict média. Servi tel quel par `GET /api/v1/tasks/{id}`. |
| **API** | `assurance` sur chaque recipe enregistrée (`GET /api/v1/recipes`) et sur chaque élément de tâche. |
| **Interface** | Un badge *Assurance réduite*, avec le périmètre en infobulle, sur l'écran Recipes, sur les éléments du détail de tâche et sur l'écran Média. |
| **Journaux** | Le champ `assurance` (valeur `reduced`) et `assurance_scope` sur les enregistrements qui concernent la recipe — et **seulement** ceux-là : filtrez sur la présence du champ pour les retrouver. |
| **Métriques** | `tobby_assurance_reduced_recipes{scope}` : combien de recipes sont actuellement détenues en assurance réduite, par périmètre déclaré — dès le premier scrape, à zéro tant qu'un périmètre ne détient rien. |
| **Manifeste du média** | Le marqueur, par recipe, dans `meta/media.json`. Omis — et non `null` — pour une recipe d'assurance complète, afin qu'un support sans contenu réduit reste lisible par une version qui précède le champ. |
| **Vérification du store** | [`tobby store verify`](../../passthrough/verify-store/) re-signale le marqueur de chaque recipe enregistrée, tel que la configuration de confiance de cette instance le voit **maintenant**. |

## De l'autre côté de l'air-gap : une déclaration, jamais un verdict

Le marqueur d'un manifeste de média est une **déclaration de la zone source**.
Le manifeste n'est pas signé et Tobby ne signe rien (ADR-0006, ADR-0007) : côté
destination, c'est une donnée à afficher, pas quelque chose sur quoi s'appuyer.
La règle est symétrique :

- La destination calcule **son propre** marqueur à partir de **son propre**
  verdict de confiance — ses propres clés et ses propres périmètres. Une recipe
  qu'elle n'admet que parce qu'un de *ses* périmètres autorise les recipes non
  signées porte *son* marqueur, nommant *son* périmètre.
- Le marqueur du manifeste est réaffiché **à côté**, étiqueté comme déclaré par
  la zone source, et jamais à sa place. Le rapport de vérification du média
  porte les deux (`assurance` et `sourceAssurance`), tout comme l'écran Média et
  `tobby media verify`.
- **Un manifeste qui ne déclare rien ne cache rien.** Un support produit par une
  version plus ancienne, ou dont le marqueur a été retiré à la main, voit
  malgré tout chaque recipe que la destination admet par un périmètre assoupli
  marquée à partir de son propre verdict.
- Une recipe que la destination **bloque** montre quand même la déclaration de
  la source, pour que l'opérateur voie pourquoi les deux zones divergent.
- Un marqueur de manifeste doit avoir la forme que cette version écrit —
  réduit, un périmètre nommé, une raison connue, de taille bornée. Un manifeste
  qui porte autre chose est incohérent en lui-même et refusé comme tout autre
  manifeste mal formé.

## Rien n'est écrit dans la registry de destination

Le marqueur vit dans les surfaces propres à Tobby — rapports, enregistrement de
la tâche, manifeste, journaux. Il n'est **jamais** écrit dans une registry :
aucune annotation, aucun tag supplémentaire, aucun artefact annexe. Le contenu
transféré reste identique à l'octet près (RECIPE-SPEC §7.1) — la registry de
zone détient les mêmes digests que la source, et un client standard qui en tire
du contenu ne voit rien que Tobby ait ajouté.

## Ce que le marqueur n'est pas

- Ce n'est pas un second type de confiance. Il marque le contenu admis par un
  assouplissement que vous avez déclaré ; il ne rend pas ce contenu plus ou
  moins digne de confiance, et il ne bloque rien.
- Il ne couvre pas un périmètre qui ne fait que restreindre la confiance. Un
  périmètre qui restreint les `roots` n'assouplit rien et ne marque rien ; un
  refus en son sein nomme le périmètre ([`TBY-SIG-001`](../../reference/errors/#tby-sig-001)).
- Il est dérivé, pas stocké de sa propre autorité : le graphe des recipes garde
  les faits d'admission (vérifiée ou non, quel périmètre), et le marqueur en est
  calculé à chaque lecture — c'est pourquoi un store écrit par une version
  antérieure porte lui aussi les marqueurs.
