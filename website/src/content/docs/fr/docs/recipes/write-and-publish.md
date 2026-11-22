---
title: Écrire, publier et signer une recipe
description: D'un document minimal à une publication signée — les exemples annotés, recipe lint, tobby recipe push, l'écran de publication et la signature cosign vérifiable hors ligne.
sidebar:
  order: 2
---

Cette page suit le chemin d'écriture, dans l'ordre : écrire, apprendre des
exemples, valider, publier, signer. Le format est normatif sur le
[site recipe-spec](https://tobby-fetch.github.io/recipe-spec/) et dans la
[spécification](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md) ;
ce qui suit montre le chemin et renvoie là-bas pour chaque règle. Si le
vocabulaire (brouillon, cuisinée, cookbook) est nouveau pour vous, commencez
par [Recipes, cookbooks et retrievers](../understand/).

## Une recipe minimale

```yaml
apiVersion: recipe.tobbyfetch.dev/v1alpha1   # the schema version (spec §4)
kind: Recipe
metadata:
  name: hello          # becomes the cookbook repository name (spec §11.3)
  version: 1.0.0       # version of the application — becomes the OCI tag
spec:
  ingredients:
    - name: hello
      kind: ContainerImage
      ref: docker.io/library/hello-world   # always fully qualified — no default registry
      version: linux                       # exact tag; a draft may use a constraint
      platforms: ["linux/amd64"]
      # a cooked recipe adds:  digest: sha256:…
```

Trois règles évitent le plus d'allers-retours :

- `ref` est l'hôte et le dépôt seulement — ni tag, ni digest, ni schéma. La
  version et le digest ont leurs propres champs pour que les outils puissent
  les valider et les résoudre indépendamment.
- Les champs inconnus sont **rejetés**, non ignorés : un `digset:` mal
  orthographié ne doit pas affaiblir l'épinglage en silence. Les données
  d'extension vont dans `metadata.annotations`.
- `metadata.name` et `metadata.version` ne sont pas décoratifs : la
  publication refuse une recipe dont le contenu contredit la référence sous
  laquelle elle est poussée.

La grammaire complète — champs de métadonnées, les quatre types d'ingrédients
et leurs champs propres, syntaxe des contraintes — est dans la spécification
([structure du document](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#5-document-structure),
[le kind Recipe](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#6-the-recipe-kind),
[types d'ingrédients](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#7-ingredient-kinds)),
avec des [JSON Schemas](https://github.com/tobby-fetch/recipe-spec/tree/main/schemas)
publiés contre lesquels n'importe quel éditeur ou CI peut valider.

## Apprendre à partir des exemples

Le dépôt livre six documents annotés sous
[`examples/`](https://github.com/tobby-fetch/tobby-fetch/tree/main/examples),
écrits pour être lus autant qu'exécutés : chacun porte le raisonnement qui a
produit sa liste d'ingrédients, car ce raisonnement est la partie difficile.
Tous ont été vérifiés contre des amonts réels et passent le lint sans réserve.

| Fichier | Ce qu'il enseigne |
| --- | --- |
| [`otel-collector.yaml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/examples/otel-collector.yaml) | Le cas nominal — et un chart qui ne rend **aucune** image par défaut. |
| [`keycloak.yaml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/examples/keycloak.yaml) | Une dépendance que le chart ne mentionne jamais, et des libellés de plateforme qui ne sont pas ce que l'on taperait. |
| [`victoria-metrics-operator.yaml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/examples/victoria-metrics-operator.yaml) | Un opérateur qui porte ses images dans ses propres valeurs par défaut. À lire absolument. |
| [`harbor.yaml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/examples/harbor.yaml) | Des images mono-architecture, et des données mutables qu'il ne faut pas épingler. |
| [`metallb.yaml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/examples/metallb.yaml) | Un chart qui n'est pas un artefact OCI, et un sidecar qui suit sa propre cadence de release. |
| [`retriever.yaml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/examples/retriever.yaml) | L'état désiré d'une zone, avec des contraintes de version selon le besoin de stabilité. |

Le problème autour duquel tournent ces exemples est toujours le même : savoir
**quelles images de conteneur un chart Helm exigera, de l'autre côté, une fois
la porte fermée**. La réponse réflexe — `helm template | grep image:` — est un
bon début et ne suffit pas. Quatre façons pour une image d'y échapper, une par
exemple :

1. **Le chart ne rend rien.** Le chart de l'OpenTelemetry Collector livre
   `image.repository: ""` et refuse de choisir une distribution à votre place ;
   rendre les valeurs par défaut donne un inventaire vide. Inventoriez les
   manifestes rendus à partir de *vos* values, jamais des valeurs par défaut
   du chart.
2. **Le chart suppose qu'une chose existe déjà.** Keycloak rend exactement une
   image et exige un PostgreSQL qu'il ne mentionne jamais. Un inventaire rendu
   dit ce qu'un chart *déploie*, jamais ce dont il *dépend* — lisez les values
   et le README pour les dépendances externes.
3. **L'opérateur porte ses images ailleurs.** Rendez le chart de l'opérateur
   VictoriaMetrics et vous obtenez une image ; les ~25 images qu'il déploie
   réellement sont compilées dans son binaire comme valeurs par défaut.
   Interrogez l'opérateur (`--printDefaults`), les valeurs par défaut de ses
   CRD ou sa documentation — avant que la zone soit scellée, pas après.
4. **Le chart n'est pas du tout un artefact OCI.** MetalLB et Harbor ne
   publient leurs charts que dans des dépôts `index.yaml` historiques, que le
   format place volontairement
   [hors périmètre](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#72-helmchart).
   Republiez le chart une fois dans une registry que vous contrôlez ; l'archive
   n'est pas réécrite, donc les signatures amont survivent.

Le [README des exemples](https://github.com/tobby-fetch/tobby-fetch/blob/main/examples/README.md)
développe chaque cas, ainsi que trois leçons que l'on apprend d'ordinaire au
prix fort : un libellé de plateforme qui nomme une variante ne retient que
celle-ci, un libellé sans variante les accepte toutes, tout ce qui est
multiplateforme ne l'est pas vraiment, et les données mutables (une base
de vulnérabilités, par exemple) appartiennent à leur propre recipe, à leur
propre cadence.

## Valider avec `recipe lint`

Le linter appartient à la spécification, non à Tobby — la validation est la
même pour toutes les implémentations, et un poste d'écriture n'a pas besoin
d'une instance Tobby :

```sh
go install github.com/tobby-fetch/recipe-spec/cmd/recipe@latest

recipe lint examples/                     # draft profile (the default)
recipe lint --profile cooked recipe.yaml  # publishable? digests + exact tags
recipe lint --output json recipes/        # machine-readable, for CI
```

Le code de sortie 0 signifie que tous les documents sont valides ; 1, des
constats ; 2, une erreur d'usage ou d'E/S — adapté à la CI par construction.
L'usage complet est dans le
[dépôt de la spécification](https://github.com/tobby-fetch/recipe-spec#cli).

## Publier avec `tobby recipe push`

```sh
tobby recipe push harbor.yaml registry.example.com/cookbook/harbor:2.15.4
```

N'importe quel outil de push OCI peut publier une recipe — c'est tout l'intérêt
du format — et le site de la spécification documente ce chemin générique dans
[Publishing recipes with standard OCI tooling](https://tobby-fetch.github.io/recipe-spec/guides/publishing-recipes/).
Ce que la sous-commande ajoute, c'est de refuser de publier ce qu'une zone
rejettera plus tard. La publication est refusée lorsque :

- le document n'est pas une `Recipe` valide ;
- elle n'est pas entièrement épinglée — un cookbook ne contient que des recipes
  cuisinées, chaque ingrédient avec un tag exact et un digest ;
- son `metadata.name` ou `metadata.version` contredit la référence sous
  laquelle elle est publiée ;
- le tag existe déjà avec un contenu **différent** — une version de recipe
  publiée est immuable. Publier deux fois le même document est une opération
  sans effet, non un conflit.

La commande s'insère dans un pipeline de signature : le retour destiné à
l'humain va sur stderr, le digest publié **seul** va sur stdout. Elle accepte
les drapeaux de configuration partagés (`--config`, `--proxy-url`, …), lit les
identifiants de registry dans le
[fichier d'identifiants configuré](../../reference/configuration/), et applique
la [liste d'autorisation des registries](../../security/content-trust/) de
l'instance comme toute autre écriture sortante. Les refus portent les mêmes
[codes d'erreur `TBY-*`](../../reference/errors/) stables que le reste de
Tobby.

## Publier depuis l'interface

Depuis la v0.4, l'écran *Publier une recipe* est la contrepartie graphique de
`tobby recipe push` : collez un document de recipe — ou déposez-le comme
fichier —, nommez la référence de destination, puis publiez. La validation est
identique par construction (les deux chemins passent par le même moteur et le
même SDK recipe-spec), donc l'écran refuse exactement ce que refuse la
sous-commande, avec les mêmes codes d'erreur. En cas de succès, il affiche le
digest publié et la commande `cosign sign` exacte à lancer ensuite, et dit
clairement ce que Tobby n'a **pas** fait : Tobby ne détient aucune clé privée
et ne signe rien.

L'écran est réservé aux opérateurs — publier écrit dans une registry — et
chaque tentative est [auditée](../../security/audit-log/), réussie ou non. Sur
une instance démarrée sans destination de publication, le formulaire est
affiché inerte plutôt que d'accepter une soumission qui ne mènerait nulle
part.

![L'écran de publication après une publication réussie : digest et commande cosign prête à copier](../../../../../assets/docs/publish-success.png)

## Signer avec cosign

La signature reste hors de Tobby, qui ne détient jamais de clé privée : la
publication produit un artefact non signé et vous signez vous-même le digest.
Avec cosign 3.x et une paire de clés :

```sh
DIGEST=$(tobby recipe push harbor.yaml registry.example.com/cookbook/harbor:2.15.4)

cosign sign --key cosign.key \
  --use-signing-config=false --tlog-upload=false \
  "registry.example.com/cookbook/harbor@${DIGEST}"
```

Deux points de cette commande sont non négociables pour les zones restreintes :

- **Signez le digest, jamais le tag.** Un tag peut être repointé ; le digest,
  c'est le contenu. C'est pourquoi la commande push imprime le digest.
- **Gardez le journal de transparence en dehors.** Un simple
  `cosign sign --key …` sous cosign 3.x téléverse la signature dans le journal
  public Rekor — un appel réseau qu'un signataire de zone restreinte ne doit
  pas faire, et une consultation qu'un vérificateur isolé ne peut pas
  effectuer. `--use-signing-config=false --tlog-upload=false` garde la
  signature vérifiable entièrement hors ligne. La vérification n'exige alors
  que la clé publique : `cosign verify --key cosign.pub
  --insecure-ignore-tlog=true …` — le drapeau ne fait que sauter le contrôle
  Rekor que ce modèle écarte délibérément.

La signature atterrit dans le même dépôt que la recipe, dans l'un ou l'autre
des formats que produit cosign (`.sig` attaché ou bundle Sigstore) ; les
consommateurs acceptent les deux, le choix revient donc à celui qui signe. Le
modèle de signature, les formats de transport et les devoirs du consommateur
sont normatifs dans la section
[signature et vérification](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#12-signing-and-verification)
de la spécification, avec un pas-à-pas dans le
[guide de publication](https://tobby-fetch.github.io/recipe-spec/guides/publishing-recipes/#sign-with-cosign-key-based).
La façon dont l'instance Tobby consommatrice reçoit les clés publiques —
racines de confiance, périmètres, rotation avec chevauchement — est sur
[Signatures, racines de confiance et liste d'autorisation](../../security/content-trust/).

## La convention de titre `recipe.yaml`

Un artefact de recipe publié porte son YAML dans une couche unique, et la
[disposition de l'artefact](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#112-artifact-layout)
de la spécification prend position, délibérément, sur l'annotation
`org.opencontainers.image.title` de cette couche : les éditeurs **DEVRAIENT**
(*SHOULD*) la fixer à `recipe.yaml`, pour qu'un `oras pull` générique écrive un
fichier au nom sensé ; les consommateurs **NE DOIVENT PAS** (*MUST NOT*) en
dépendre, ni rejeter un artefact pour sa valeur ou son absence — l'outillage
générique écrit le nom de fichier qu'on lui a donné, et le format promet que
l'outillage générique y participe.

En pratique : `tobby recipe push` la fixe pour vous ; en publiant avec
`oras push`, nommez le fichier `recipe.yaml` avant de pousser. Côté
consommateur, Tobby respecte le MUST NOT — il identifie une recipe par son type
d'artefact et sa disposition, jamais par le titre.

## Cuisiner un brouillon

:::note[À venir]
`recipe cook` — la commande qui résout les contraintes d'un brouillon,
enregistre chaque digest et émet le document publiable — est prévue dans
l'outillage de la spécification, sur le site de celle-ci et non ici, car
cuisiner n'exige aucune instance Tobby. D'ici là, la cuisine est manuelle :
résolvez les versions, lisez les digests dans les registries (`crane digest`,
`oras manifest fetch`) et laissez `recipe lint --profile cooked` trancher.
Suivez-la sur la page [état du projet](../../discover/status/).
:::
