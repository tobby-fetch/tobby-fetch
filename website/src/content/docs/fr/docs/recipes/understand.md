---
title: Recipes, cookbooks et retrievers
description: Le vocabulaire, le cycle de vie du brouillon à la recipe cuisinée, comment choisir un type d'ingrédient, et comment Tobby résout les versions et ne transfère que ce qui manque à une zone.
sidebar:
  order: 1
---

Une recipe décrit tout ce dont une application a besoin pour tourner dans une
zone — images, charts, modèles, fichiers — épinglé à des digests exacts, de
sorte qu'une seule signature atteste toute la livraison. Cette page explique
les concepts tels qu'on les rencontre dans Tobby. Le format lui-même est une
spécification ouverte, publiée séparément sous Apache-2.0 sur
[tobby-fetch.github.io/recipe-spec](https://tobby-fetch.github.io/recipe-spec/) :
tout ce qui est normatif — grammaire, JSON Schemas, règles de validation — s'y
trouve, et cette page y renvoie au lieu de le répéter.

## Le vocabulaire

La métaphore est celle d'une cuisine, et elle porte du sens : chaque mot
désigne un objet précis. Le reste du vocabulaire du produit est dans le
[glossaire](../../discover/glossary/).

| Terme | Ce que c'est |
| --- | --- |
| **Recipe** | Un document YAML qui liste les *ingrédients* d'une application, chacun épinglé par version et digest. |
| **Ingrédient** | Un artefact OCI qu'une recipe référence : une image de conteneur, un chart Helm, un artefact OCI générique ou un jeu de fichiers. |
| **Cookbook** | Un espace de noms de dépôts OCI où les recipes sont publiées comme des artefacts OCI ordinaires. Le cookbook d'une zone est le catalogue de référence des logiciels approuvés pour cette zone. |
| **Retriever** | Un document YAML qui liste les recipes (et les contraintes de version) qu'une zone veut. C'est l'état désiré de cette zone. |

Aucun serveur dédié n'est derrière tout cela. Un cookbook est une simple
registry OCI ; une recipe est un artefact OCI ordinaire que n'importe quel
outil peut pousser, tirer et signer. C'est ce choix qui permet aux recipes de
franchir les zones avec les mêmes outils que le contenu qu'elles décrivent —
signatures comprises. Les définitions normatives sont dans les sections
[terminologie](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#3-terminology)
et [cookbook](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#11-the-cookbook)
de la spécification.

Les recipes sont des manifestes de style Kubernetes
(`apiVersion: recipe.tobbyfetch.dev/v1alpha1`, `kind: Recipe`), volontairement
familiers à quiconque exploite un cluster — et volontairement stricts : un
champ mal orthographié est rejeté, jamais ignoré en silence, car une faute de
frappe dans `digest:` ne doit pas affaiblir l'épinglage
([validation stricte](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#43-strict-validation)).

## Brouillon et cuisinée

Une recipe existe dans exactement l'un de deux états, et cet état n'est pas un
champ : il est déterminé par l'endroit où vit le document et par ce qu'il
contient.

- Un **brouillon** (*draft*) est une recipe en cours de construction : il vit
  dans votre espace de travail ou dans git, les contraintes de version y sont
  permises (`~0.16.2`, `26.x`), les digests sont facultatifs. Il se modifie
  librement et se relit en diff comme tout autre fichier.
- Une recipe **cuisinée** (*cooked*) est publiée dans un cookbook : chaque
  ingrédient porte un tag exact **et** un digest, et l'artefact est immuable et
  signé. Récupérer ses ingrédients à leurs digests enregistrés donne un
  contenu identique au bit près, partout et pour toujours — ou échoue de façon
  vérifiable.

**Cuisiner** est l'acte intermédiaire : résoudre chaque contrainte en un tag
exact, enregistrer chaque digest et publier le résultat. Un pipeline de
qualification cuisine typiquement une recipe une fois ses contrôles passés.
Une recipe cuisinée n'est jamais modifiée : tout changement — même d'un seul
digest — exige une nouvelle `metadata.version`. Les règles complètes sont
dans la section
[brouillon et cuisinée](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#8-draft-and-cooked-recipes)
de la spécification.

## Le cycle de vie dans le contexte de Tobby

De bout en bout, une recipe passe entre cinq mains :

1. **Un auteur écrit un brouillon** — en général à partir d'une recipe
   existante ou des
   [exemples annotés](../write-and-publish/#apprendre-à-partir-des-exemples),
   et le valide avec la CLI `recipe lint` de la spécification.
2. **Quelqu'un la cuisine et la publie** dans le cookbook de production — avec
   [`tobby recipe push`](../write-and-publish/#publier-avec-tobby-recipe-push),
   depuis l'[interface](../write-and-publish/#publier-depuis-linterface) ou
   avec un outillage OCI générique — puis signe le digest publié avec cosign.
   Tobby ne détient jamais de clé privée et ne signe rien.
3. **Un retriever déclare ce qu'une zone veut.** Il liste des noms de recipes
   avec des versions exactes ou des contraintes, résolues contre le cookbook.
   Une instance Tobby lit son retriever depuis un fichier, une URL `https://`
   ou un artefact OCI — voir
   [Retriever de zone et cascade](../../passthrough/retriever-cascade/).
4. **Tobby synchronise.** À chaque cycle, il résout de nouveau chaque entrée
   du retriever, vérifie la signature de chaque recipe contre les
   [racines de confiance](../../security/content-trust/) configurées de la
   destination, et transfère la recipe **avec** tous ses ingrédients — à
   l'identique au bit près, digests et signatures amont intacts. L'artefact
   recipe lui-même atterrit dans le cookbook de la zone, qui reste ainsi
   auto-descriptive et auditable hors ligne.
5. **Les clients consomment le contenu** depuis la zone —
   [Brancher vos clients](../../passthrough/connect-clients/).

<svg viewBox="0 0 640 148" role="img" aria-label="Cycle de vie d'une recipe : un recipe.yaml brouillon est validé, cuisiné, publié et signé dans le cookbook ; le Retriever nomme ce que la zone veut, Tobby le résout à chaque synchronisation, vérifie et promeut, puis pousse différentiellement les ingrédients et la recipe dans la registry de la zone" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
  <defs>
    <marker id="rl-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--sl-color-gray-3)" />
    </marker>
  </defs>
  <!-- retriever -->
  <rect x="350" y="10" width="120" height="38" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="410" y="26" text-anchor="middle" font-size="11" font-weight="600" fill="var(--sl-color-gray-1)">Retriever</text>
  <text x="410" y="41" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">ce que la zone veut</text>
  <line x1="410" y1="48" x2="410" y2="66" stroke="var(--sl-color-gray-3)" marker-end="url(#rl-arrow)" />
  <!-- chain -->
  <rect x="8" y="70" width="130" height="48" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="73" y="90" text-anchor="middle" font-size="10.5" font-family="monospace" fill="var(--sl-color-gray-1)">recipe.yaml</text>
  <text x="73" y="106" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">brouillon — modifiable</text>
  <rect x="174" y="70" width="130" height="48" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="239" y="90" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Cookbook (OCI)</text>
  <text x="239" y="106" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">immuable · signé</text>
  <rect x="340" y="70" width="140" height="48" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="410" y="90" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="410" y="106" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">vérifie + promeut</text>
  <rect x="506" y="70" width="126" height="48" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="569" y="90" text-anchor="middle" font-size="11" fill="var(--sl-color-gray-1)">Registry de zone</text>
  <text x="569" y="106" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">ingrédients + recipe</text>
  <!-- arrows -->
  <line x1="138" y1="94" x2="170" y2="94" stroke="var(--sl-color-gray-3)" marker-end="url(#rl-arrow)" />
  <text x="156" y="62" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">valider · cuisiner · publier · signer</text>
  <line x1="304" y1="94" x2="336" y2="94" stroke="var(--sl-color-gray-3)" marker-end="url(#rl-arrow)" />
  <text x="315" y="62" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">résolu à chaque synchro</text>
  <line x1="480" y1="94" x2="502" y2="94" stroke="var(--sl-color-gray-3)" marker-end="url(#rl-arrow)" />
  <text x="493" y="62" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">push différentiel</text>
  <!-- draft / cooked -->
  <line x1="156" y1="70" x2="156" y2="128" stroke="var(--sl-color-gray-5)" stroke-dasharray="3 3" />
  <text x="73" y="140" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-2)">brouillon — librement modifié</text>
  <text x="403" y="140" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-2)">cuisinée — immuable, une signature atteste toute la livraison</text>
</svg>

Pour un besoin ponctuel — une seule image, une fois — une recipe est
disproportionnée ; voir [Imports unitaires](../../passthrough/one-off-import/)
et ce que ce choix implique pour la provenance.

## Choisir un type d'ingrédient

Quatre types couvrent tout ce qu'une zone consomme. Le choix est le plus
souvent évident ; les pièges, non.

### ContainerImage

Une image exécutable, éventuellement multiplateforme. Le digest épingle
l'**index** (la liste de manifestes multiplateforme), pas le manifeste d'une
plateforme. La liste facultative `platforms:` nomme ce que la destination doit
avoir — ne nommer que ce que votre zone exécute garde les transferts et les
supports petits, tandis que l'index épinglé reste intact et vérifiable. Un
libellé s'écrit `os/arch[/variant]`, la variante étant facultative :
`linux/arm64` sélectionne l'image arm64 quelle que soit la variante que la
registry lui donne — ce qui compte, car les images officielles étiquettent la
leur `linux/arm64/v8`. Nommez une variante et elle s'impose : `linux/arm/v7`
n'accepte pas une image v6, et une plateforme que l'index ne porte tout
simplement pas fait échouer l'ingrédient au lieu d'être écartée en silence.
Spécification :
[§7.1](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#71-containerimage).

### HelmChart

Un chart stocké comme artefact OCI (format natif Helm ≥ 3.8). Deux choses
font trébucher tout nouvel auteur :

- **Un chart n'implique jamais ses images.** Le format refuse d'évaluer les
  templates et les values : chaque image dont le chart aura besoin doit être
  listée explicitement comme ingrédient `ContainerImage`, pour que le
  transfert reste relisible. Trouver cette liste est la vraie difficulté de
  l'écriture — le
  [parcours des exemples](../write-and-publish/#apprendre-à-partir-des-exemples)
  est consacré aux quatre façons dont une image échappe à `helm template`.
- **Les dépôts `index.yaml` historiques sont hors périmètre.** Un chart publié
  uniquement dans un dépôt HTTP classique doit être republié dans une registry
  OCI que vous contrôlez avant de pouvoir être un ingrédient. L'archive n'est
  pas réécrite — les octets, et toute signature amont, survivent au
  déménagement.

Laissez `vendorDependencies` à sa valeur par défaut (`false`) sauf si vous
avez lu
[§7.2](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#72-helmchart) :
le vendoring réécrit l'archive du chart, si bien que le digest enregistré
n'est plus celui de l'amont et que les signatures amont du chart ne
s'appliquent plus.

### OCIArtifact

Tout le reste de ce qui vit dans une registry OCI : modèles d'IA et poids,
bundles de bases de vulnérabilités, collections de SBOM, modules WASM, bundles
de politiques. Le champ facultatif `artifactType` est une garde : s'il est
renseigné, le type de l'artefact récupéré doit correspondre, ce qui attrape
les erreurs de réutilisation de tag et les confusions de dépôt.
Spécification :
[§7.3](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#73-ociartifact).

### FileSet

Des fichiers quelconques — bundles de configuration, documentation hors ligne,
scripts, certificats — empaquetés **comme une image OCI standard**. Cet
empaquetage donne gratuitement deux modes de consommation : le monter en
lecture seule dans Kubernetes via les image volumes, épinglé par digest sans
dérive, ou l'extraire sur disque selon les règles de sécurité de la
spécification. Le site de la spécification propose un guide pratique,
[Packaging a FileSet](https://tobby-fetch.github.io/recipe-spec/guides/packaging-a-fileset/),
pour en construire un au digest déterministe. Un FileSet est aussi la façon
dont une zone sert un dépôt de paquets d'OS — voir
[Brancher vos clients](../../passthrough/connect-clients/).

Une habitude de plus à prendre : une donnée qui bouge vite n'a pas sa place
dans une recipe lente. Une base de vulnérabilités republiée plusieurs fois par
semaine ne doit pas être épinglée à côté d'une registry que vous mettez à
niveau deux fois par an — donnez-lui sa propre recipe, à sa propre cadence.

## Contraintes de version, et ce qui bouge vraiment

La `version` d'un ingrédient — comme celle d'une entrée de retriever — est
soit un tag exact, soit une contrainte : `~0.16.2` (niveau correctif),
`^0.163.2` (compatible), `26.x` (série), comparaisons et conjonctions. La
grammaire complète, y compris les sémantiques qui diffèrent des bibliothèques
courantes, est normative au
[§9](https://github.com/tobby-fetch/recipe-spec/blob/main/RECIPE-SPEC.md#9-version-constraint-syntax).
Trois comportements comptent en exploitation :

- Les contraintes sont résolues de nouveau contre le cookbook à **chaque**
  synchronisation : publier une version corrective est la façon de la faire
  parvenir à chaque zone — aucun fichier de retriever à modifier.
- La version correspondante la **plus haute** l'emporte ; si rien ne
  correspond, cette entrée échoue et le dit. Il n'y a pas de repli silencieux,
  et une recipe irrésoluble ne bloque jamais les autres.
- Dans une recipe cuisinée, les contraintes ont disparu : seulement des tags
  exacts et des digests. La résolution est une notion de temps d'écriture et
  de temps de retriever.

Les transferts sont ensuite différentiels, décidés digest par digest. Pour
chaque élément, Tobby compare ce que la recipe épingle à ce que la destination
détient et le classe **nouveau** (absent, sera transféré), **périmé** (le tag
existe mais pointe vers un autre digest, sera mis à jour) ou **à jour** (déjà
ce digest exact, zéro octet déplacé). Relancer une synchronisation qui n'a
rien à faire ne déplace rien — une propriété que les scénarios d'acceptation
vérifient sur de vraies registries.

## Les recipes dans l'interface

Les recipes sont des objets de premier rang dans l'interface de Tobby, avec
les mêmes données disponibles par l'[API](../../reference/api/) :

- **La liste des recipes** (*Recipes*) montre chaque recipe que l'instance a
  résolue, sa version, quand elle a été résolue et son état de vérification —
  une recipe admise sans signature sous un
  [périmètre de confiance](../../security/content-trust/) déclaré le dit
  explicitement, jamais en silence.
- **Le mapping par recipe** liste chaque ingrédient de chaque version
  enregistrée : son type, le dépôt local relocalisé où il a atterri (avec
  lien, pour parcourir le contenu que cette instance détient réellement), son
  tag et son digest épinglé — chacun copiable en un clic.
- **Le document de la recipe elle-même** est affiché sur la page de contenu
  de l'artefact publié : le YAML exact que cette instance détient et a vérifié
  à l'entrée — pas une relecture du cookbook amont — avec son digest, pour que
  la distinction reste vérifiable. Vous pouvez le copier ou le télécharger
  comme fichier. Une recipe cuisinée étant immuable, c'est volontairement un
  téléchargement et non un éditeur : c'est le point de départ naturel pour
  écrire la version suivante.

![L'écran des recipes : chaque recipe avec son badge de vérification de signature](../../../../../assets/docs/try-recipes-verified.png)
![Le document de la recipe sur sa page de contenu : corps YAML, digest, actions de copie et de téléchargement](../../../../../assets/docs/recipe-document.png)

## Pour aller plus loin

- [Écrire, publier et signer une recipe](../write-and-publish/) — le parcours
  d'écriture, d'un document minimal à une publication signée.
- [Retriever de zone et cascade](../../passthrough/retriever-cascade/) — comment
  une instance consomme des recipes, et comment les zones s'enchaînent sans les
  modifier.
- [Signatures, racines de confiance et liste d'autorisation](../../security/content-trust/) —
  comment la vérification se configure côté consommateur.
