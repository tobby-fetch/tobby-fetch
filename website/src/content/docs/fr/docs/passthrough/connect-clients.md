---
title: "Brancher vos clients : Docker, containerd, GitOps"
description: Pourquoi les chemins relocalisés ont cette forme, le tableau de correspondance par recipe, les mirrors containerd pour K3s/RKE2, les pièges GitOps et le point d'accès aux paquets système.
sidebar:
  order: 5
---

Le contenu est arrivé dans votre zone — promu en continu par une instance
passthrough, ou importé depuis un média amovible dans une zone isolée.
Dans les deux cas, cette page est la fin du parcours : pointer les
machines qui le consomment au bon endroit. Tout ce qui suit vaut pour
les deux modes.

## Pourquoi `docker.io/x` devient `registry.zone/docker.io/x`

Tobby relocalise chaque ingrédient sous son **hôte source nominal** :

```
<zone-registry>[/<base-prefix>]/<canonical-source-host>[_<port>]/<repository-path>
```

`docker.io/bitnami/wordpress` se tire depuis
`registry.zone.example/docker.io/bitnami/wordpress`, avec un digest
inchangé. La règle ([ADR-0013](../../reference/srs-adr/), FR-035) apporte
trois choses, au prix de noms plus longs :

- **Aucune collision.** Un chemin aplati
  `registry.zone/bitnami/wordpress` ne distingue pas `docker.io/foo/bar`
  de `ghcr.io/foo/bar` — un défaut de correction et une surface d'attaque
  par confusion de dépôt, d'un seul coup.
- **Prévisibilité.** À partir d'une recipe et d'une destination,
  l'emplacement de chaque ingrédient se calcule sans métadonnée
  supplémentaire — les audits, l'outillage de nettoyage et la comparaison
  différentielle de Tobby lui-même s'appuient tous sur la même fonction
  pure.
- **Invariance en cascade.** Le chemin dérive de l'hôte *écrit dans la
  recipe*, pas de l'hôte contacté : il est donc identique dans chaque
  zone d'une chaîne à plusieurs sauts.

Les hôtes sont ramenés à une forme canonique (mise en minuscules ;
`index.docker.io` et `registry-1.docker.io` se replient sur
`docker.io`), et le `:` d'un port devient `_` — `lab.example.com:5000`
se relocalise sous `lab.example.com_5000/`. Les noms ne sont jamais
tronqués : une destination qui ne peut pas accueillir un nom relocalisé
échoue explicitement, avant le push.

<svg viewBox="0 0 640 150" role="img" aria-label="La référence amont docker.io/library/alpine traverse Tobby et arrive dans le registre de zone sous registry.zone.example/docker.io/library/alpine, avec un digest sha256 identique des deux côtés" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
  <defs>
    <marker id="cc-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--sl-color-gray-3)" />
    </marker>
  </defs>
  <text x="117" y="40" text-anchor="middle" font-size="10" font-weight="600" fill="var(--sl-color-gray-2)">amont</text>
  <text x="520" y="36" text-anchor="middle" font-size="10" font-weight="600" fill="var(--sl-color-gray-2)">registre de zone</text>
  <!-- référence amont -->
  <rect x="12" y="50" width="210" height="58" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="117" y="75" text-anchor="middle" font-size="10" font-family="monospace" fill="var(--sl-color-gray-1)">docker.io/library/alpine</text>
  <text x="117" y="93" text-anchor="middle" font-size="9" font-family="monospace" fill="var(--sl-color-gray-3)">sha256:9b2a28eb…</text>
  <!-- tobby -->
  <rect x="262" y="54" width="116" height="50" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="320" y="75" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="320" y="91" text-anchor="middle" font-size="9" fill="var(--sl-color-gray-3)">ni réécriture, ni re-signature</text>
  <!-- référence relocalisée -->
  <rect x="408" y="46" width="224" height="66" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="520" y="66" text-anchor="middle" font-size="10" font-family="monospace" fill="var(--sl-color-gray-1)">registry.zone.example/</text>
  <text x="520" y="80" text-anchor="middle" font-size="10" font-family="monospace" fill="var(--sl-color-gray-1)">docker.io/library/alpine</text>
  <text x="520" y="98" text-anchor="middle" font-size="9" font-family="monospace" fill="var(--sl-color-gray-3)">sha256:9b2a28eb…</text>
  <!-- flux -->
  <line x1="222" y1="79" x2="258" y2="79" stroke="var(--sl-color-gray-3)" marker-end="url(#cc-arrow)" />
  <line x1="378" y1="79" x2="404" y2="79" stroke="var(--sl-color-gray-3)" marker-end="url(#cc-arrow)" />
  <!-- légende -->
  <text x="320" y="140" text-anchor="middle" font-size="10" fill="var(--sl-color-gray-2)">Même digest des deux côtés — seul l'hôte du registre change ; l'hôte source nominal survit dans le chemin</text>
</svg>

## Le tableau de correspondance, par recipe

Vous ne calculez jamais ces chemins à la main. Chaque recipe expose son
tableau source→destination — pour chaque ingrédient, la référence
nominale, le dépôt relocalisé, le tag et le digest épinglé (FR-065) :

- **Interface** : la vue de correspondance de la recipe, à
  `/recipes/{recipe}/mapping`, avec des références copiables.
- **API** : `GET /api/v1/recipes/{recipe}/mapping` — les mêmes données en
  JSON, une entrée par version résolue, ingrédients compris.

![Le tableau de correspondance par recipe : de la référence amont au chemin relocalisé dans la zone, avec boutons de copie](../../../../../assets/docs/recipe-mapping.png)

## Mirrors containerd pour K3s et RKE2

Pour les **tirages d'images à l'exécution**, vous n'avez aucune valeur de
chart à modifier : containerd réécrit les références au moment du pull.
Sur chaque nœud K3s/RKE2, écrivez
`/etc/rancher/{k3s,rke2}/registries.yaml` à la main, à partir du tableau
de correspondance :

```yaml
# /etc/rancher/rke2/registries.yaml
mirrors:
  docker.io:
    endpoint:
      - "https://registry.zone.example"
    rewrite:
      "^(.*)$": "docker.io/$1"
  ghcr.io:
    endpoint:
      - "https://registry.zone.example"
    rewrite:
      "^(.*)$": "ghcr.io/$1"
configs:
  registry.zone.example:
    auth:
      username: puller
      password: "…"
```

Une fois cela en place, un pod qui référence
`docker.io/bitnami/wordpress` tire depuis le registre de zone, et le
chart se déploie tel qu'il a été publié — valeurs inchangées, digests
intacts.

:::note[À venir]
Générer ce fragment par recipe et par destination est la seconde moitié
de FR-065 et n'est pas encore implémenté — aujourd'hui, le tableau de
correspondance est exposé et le fragment s'écrit à la main. À suivre sur
la page [État du projet](../../discover/status/).
:::

## Ce que les mirrors ne couvrent pas

Deux catégories de références contournent containerd et **doivent nommer
explicitement les chemins relocalisés**, à partir du tableau de
correspondance :

- **Sources de charts GitOps.** Un `repoURL` Argo CD ou un
  `HelmRepository` Flux pointant sur `oci://registry.zone.example/...`
  doit employer le chemin de chart relocalisé — le client Helm ne passe
  pas par les mirrors au niveau du nœud.
- **Politiques d'admission.** Les règles qui filtrent sur les références
  d'images (Kyverno, Gatekeeper, admission par vérification de
  signature) voient la référence telle qu'elle est écrite dans le pod
  spec. Décidez de quel côté de la réécriture vos politiques
  s'appliquent, et tenez-vous-y.

Une réserve de plus pour les vérificateurs : les signatures cosign
incluent la référence d'*origine* dans
`critical.identity.docker-reference`. Tobby copie les signatures au bit
près à côté du contenu ; un moteur de politique qui compare
`docker-reference` à l'emplacement **effectivement tiré** verra donc
`docker.io/...` alors que le pull vient de `registry.zone.example/...`.
Configurez la politique sur la référence nominale. Détails dans
[Signatures, trust roots et liste blanche](../../security/content-trust/).

Tobby ne réécrit jamais les valeurs d'un chart — cela casserait digests
et signatures, précisément ce qu'il existe pour préserver.

## S'authentifier

Le registre embarqué et les surfaces exposées à la zone utilisent les
comptes de l'instance (FR-076) — les mêmes que ceux de l'interface :

```sh
docker login registry.tobby.zone.example
helm registry login registry.tobby.zone.example
oras login registry.tobby.zone.example
```

Créez des comptes en lecture seule dédiés, ou des jetons CI, pour les
consommateurs ; voir
[Authentification et RBAC](../../security/auth-rbac/).

## Paquets système en HTTP : le point d'accès `/files/`

Un ingrédient `FileSet` peut empaqueter un dépôt apt ou rpm, et Tobby
sait servir son contenu vérifié en lecture seule sur HTTP sous
`/files/<name>/…` (FR-047) — de quoi rendre un hôte nu installable sans
aucune autre infrastructure. Le service est **désactivé par défaut** et
s'active FileSet par FileSet :

```yaml
files:
  filesets:
    - name: debs                     # servi sous /files/debs/
      ref: registry.example.com/filesets/site-packages
      version: "1.4.0"               # vide = plus haute version semver présente localement
      platform: linux/amd64          # utile seulement pour les FileSets multi-plateformes
      anonymous: true                # lectures non authentifiées, sur accord explicite
```

Seul ce que le store détient *et a vérifié* est servi ; les requêtes par
plage sont supportées ; il n'y a aucune surface d'envoi. Les lectures
exigent le rôle `viewer` par défaut. `anonymous: true` existe pour le cas
d'amorçage — un hôte nu qui ne peut pas s'authentifier tant qu'il n'a
rien installé — et n'est jamais silencieux : chaque FileSet servi
anonymement est nommé dans un bandeau permanent de l'interface et
rapporté par l'API, comme tout autre relâchement du défaut sécurisé
(FR-075).

```sh
# /etc/apt/sources.list.d/zone.list sur un hôte client
deb [trusted=yes] https://tobby.zone.example/files/debs stable main
```

La confiance du gestionnaire de paquets dans les métadonnées du dépôt
relève du mécanisme de votre distribution ; ce que Tobby garantit, c'est
que l'arborescence servie provient d'un FileSet dont la signature a été
vérifiée à l'entrée dans le store.

### Empaqueter des fichiers qui n'ont pas de recipe

Il arrive que les fichiers à servir n'aient aucun amont d'où les tirer :
quelques pilotes constructeur, un dépôt construit localement, un lot remis
sur un disque. `tobby fileset pack` transforme un répertoire en FileSet —
une image OCI standard dont l'unique couche est l'arborescence du
répertoire — et l'importe dans le store, épinglé par son digest :

```sh
tobby fileset pack ./apt-repo debs:1.0.0
```

L'empaquetage est reproductible : le même répertoire produit toujours le
même digest, si bien qu'empaqueter deux fois ne transfère rien la seconde
fois. Les horodatages et la propriété ne sont délibérément pas portés —
seuls le sont l'arborescence, ses permissions et ses liens symboliques. Le
répertoire est refusé, l'entrée nommée, dès qu'il contient ce qu'une
extraction de FileSet devrait de toute façon refuser : un lien symbolique
qui sort du répertoire, un bit setuid, un nœud de périphérique ou une
socket, ou un nom réservé par le format de couche d'image.

Deux limites, énoncées parce qu'elles sont le sujet :

- **Un FileSet empaqueté n'est pas signé.** Tobby ne détient aucune clé de
  signature : il est donc enregistré comme import manuel d'origine locale,
  et chaque listing le dit. C'est du contenu dont vous répondez, pas du
  contenu dont répondent les clés de confiance.
- **Le servir est une étape explicite et distincte.** L'empaquetage le met
  dans le store ; rien n'est servi tant qu'il n'est pas nommé sous
  `files.filesets` et que l'instance n'a pas redémarré. La commande imprime
  le bloc exact à ajouter.

La commande lit le système de fichiers de l'hôte avec les droits de qui la
lance. La même opération depuis l'interface et l'API
(`POST /filesets/pack`) est réservée aux administrateurs **et** confinée
aux répertoires que nomme `files.packRoots` — sans aucune entrée
configurée, le formulaire n'est même pas proposé. C'est la seule façon dont
des fichiers locaux entrent dans un store, et ce n'est délibérément pas un
point d'envoi.

<!-- TODO: screenshot: l'écran FileSets — l'inventaire de ce qui est détenu et de ce qui est servi, avec le formulaire d'empaquetage -->

Ensuite : faire entrer du contenu hors de toute recipe —
[imports ponctuels](../../passthrough/one-off-import/) — ou passer à
[l'exploitation dans la durée](../../passthrough/operate/).
