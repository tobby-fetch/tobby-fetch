---
title: Retriever de zone et cascade
description: Le document d'état désiré qui pilote une zone, et la façon dont les zones s'enchaînent — l'aval qui récupère depuis l'amont sans qu'une seule recipe ne change.
sidebar:
  order: 4
---

Une zone est pilotée par un seul document : son **Retriever**. Il liste,
par nom et par contrainte de version, les recipes que la zone doit
détenir, et nomme le cookbook où les résoudre. L'instance le relit à
chaque synchronisation ; changer ce que contient une zone, c'est changer
ce document — ou publier une nouvelle version de recipe qu'une contrainte
existante couvre déjà. Le format du document est normatif sur le site
recipe-spec : voir la
[spécification du Retriever](https://tobby-fetch.github.io/recipe-spec/)
(le kind `Retriever`, `recipe.tobby.dev/v1alpha1`). Un exemple complet et
commenté est livré dans le dépôt, sous
[`examples/retriever.yaml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/examples/retriever.yaml).

## Trois sources

`retriever.source` accepte trois formes (FR-010) :

| Forme | Exemple | Quand |
| --- | --- | --- |
| Fichier local | `/etc/tobby/retriever.yaml` | Le document est géré avec la configuration de l'instance. |
| URL HTTP(S) | `https://git.example.com/platform/retriever.yaml` | Le document vit dans un dépôt Git ou sur n'importe quel serveur web — le montage GitOps habituel. |
| Référence OCI | `oci://registry.example.com/config/retriever:v1` | Le document voyage comme le contenu qu'il décrit — y compris d'une zone à l'autre, porté par Tobby lui-même. |

La source configurée est affichée telle quelle sur l'écran
d'administration **Retriever** (`/admin/retriever`, rôle admin) et sur
son miroir d'API `GET /api/v1/retriever` — aux côtés des périmètres de
confiance relâchés déclarés et de l'intervalle de synchronisation
effectif. La surcharge d'intervalle vit sur le même écran
(`PUT /api/v1/retriever/interval`, et `DELETE` pour revenir à la valeur
configurée) ; elle persiste dans le répertoire d'état, survit aux
redémarrages, l'emporte sur `sync.interval`, et est auditée comme un
changement de configuration sensible (FR-094).

![L'écran d'administration Retriever : source configurée, intervalle et sa surcharge à chaud](../../../../../assets/docs/admin-retriever.png)

À chaque cycle, l'instance résout depuis le cookbook chaque recipe
listée, vérifie sa signature contre les trust roots configurées
(FR-033), puis réconcilie. Les contraintes de version sont résolues à
chaque passe ; si aucune version publiée ne satisfait une contrainte,
cette entrée échoue et le dit — les autres entrées poursuivent. Une seule
recipe non résolvable ne bloque jamais la zone.

## La cascade : connectée → restreinte → plus restreinte

<svg viewBox="0 0 640 226" role="img" aria-label="Trois zones enchaînées, chacune avec une instance Tobby qui re-vérifie à l'entrée et promeut dans son registre de zone ; les mêmes recipes signées circulent de la zone A à la zone B puis à la zone C, et le chemin relocalisé est identique dans chaque zone" style="width:100%;max-width:640px;height:auto;display:block;margin:1rem auto;font-family:var(--sl-font);">
  <defs>
    <marker id="rc-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="var(--sl-color-gray-3)" />
    </marker>
  </defs>
  <!-- zones -->
  <rect x="8" y="30" width="196" height="150" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <rect x="222" y="30" width="196" height="150" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <rect x="436" y="30" width="196" height="150" rx="10" fill="none" stroke="var(--sl-color-gray-5)" stroke-dasharray="5 4" />
  <text x="106" y="20" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Zone A — amont</text>
  <text x="320" y="20" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Zone B — aval</text>
  <text x="534" y="20" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-2)">Zone C — plus en aval</text>
  <!-- instances tobby -->
  <rect x="38" y="44" width="136" height="42" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="106" y="61" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="106" y="76" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">re-vérifie à l'entrée</text>
  <rect x="252" y="44" width="136" height="42" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="320" y="61" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="320" y="76" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">re-vérifie à l'entrée</text>
  <rect x="466" y="44" width="136" height="42" rx="8" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-accent)" stroke-width="1.5" />
  <text x="534" y="61" text-anchor="middle" font-size="12" font-weight="600" fill="var(--sl-color-gray-1)">Tobby</text>
  <text x="534" y="76" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">re-vérifie à l'entrée</text>
  <!-- registres de zone -->
  <rect x="28" y="116" width="156" height="44" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="106" y="133" text-anchor="middle" font-size="10.5" fill="var(--sl-color-gray-1)">registre de zone</text>
  <text x="106" y="148" text-anchor="middle" font-size="8.5" font-family="monospace" fill="var(--sl-color-gray-3)">…/docker.io/bitnami/wordpress</text>
  <rect x="242" y="116" width="156" height="44" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="320" y="133" text-anchor="middle" font-size="10.5" fill="var(--sl-color-gray-1)">registre de zone</text>
  <text x="320" y="148" text-anchor="middle" font-size="8.5" font-family="monospace" fill="var(--sl-color-gray-3)">…/docker.io/bitnami/wordpress</text>
  <rect x="456" y="116" width="156" height="44" rx="6" fill="var(--sl-color-gray-6)" stroke="var(--sl-color-gray-5)" />
  <text x="534" y="133" text-anchor="middle" font-size="10.5" fill="var(--sl-color-gray-1)">registre de zone</text>
  <text x="534" y="148" text-anchor="middle" font-size="8.5" font-family="monospace" fill="var(--sl-color-gray-3)">…/docker.io/bitnami/wordpress</text>
  <!-- flux -->
  <line x1="106" y1="86" x2="106" y2="112" stroke="var(--sl-color-gray-3)" marker-end="url(#rc-arrow)" />
  <line x1="320" y1="86" x2="320" y2="112" stroke="var(--sl-color-gray-3)" marker-end="url(#rc-arrow)" />
  <line x1="534" y1="86" x2="534" y2="112" stroke="var(--sl-color-gray-3)" marker-end="url(#rc-arrow)" />
  <line x1="174" y1="65" x2="248" y2="65" stroke="var(--sl-color-gray-3)" marker-end="url(#rc-arrow)" />
  <text x="211" y="57" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">recipes</text>
  <line x1="388" y1="65" x2="462" y2="65" stroke="var(--sl-color-gray-3)" marker-end="url(#rc-arrow)" />
  <text x="425" y="57" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">recipes</text>
  <!-- légendes -->
  <text x="320" y="202" text-anchor="middle" font-size="10.5" fill="var(--sl-color-gray-2)">Les mêmes recipes signées descendent sans modification — chaque instance re-vérifie contre ses propres trust roots</text>
  <text x="320" y="219" text-anchor="middle" font-size="9.5" fill="var(--sl-color-gray-3)">Les chemins relocalisés sont invariants : le même chemin …/docker.io/… dans chaque zone, quel que soit le nombre de sauts</text>
</svg>

Les topologies réelles enchaînent les zones. La zone amont promeut dans
son registre ; le Tobby de la zone aval récupère **depuis ce registre**,
alors même que les recipes — immuables, signées, au bit près —
continuent de nommer les hôtes d'origine (`docker.io/...`). Le pont,
c'est la **substitution de source** (FR-036) :

```yaml
# Instance aval
registries:
  substitutions:
    docker.io: registry.upstream.example/docker.io
    ghcr.io: registry.upstream.example/ghcr.io

retriever:
  source: oci://registry.upstream.example/cookbook/retriever:v1
```

La substitution change **uniquement le point de terminaison réseau
contacté — jamais le chemin de destination calculé** (FR-035). C'est
cette invariance qui rend la cascade composable :
`docker.io/bitnami/wordpress` se relocalise en
`<registry>/docker.io/bitnami/wordpress` dans *chaque* zone, quel que
soit le nombre de sauts franchis, et ne dégénère jamais en
`reg.zone2/reg.zone1/docker.io/...`. Le registre de chaque zone détient
les mêmes chemins relocalisés sous son propre hôte, et le cookbook de
chaque zone — alimenté par la propagation des recipes (FR-034) — est ce
que désigne le Retriever de la zone suivante. La règle et sa
justification sont dans [ADR-0013](../../reference/srs-adr/) ; la
grammaire normative (forme canonique des hôtes, encodage des ports,
sémantique de substitution) est dans
[RECIPE-SPEC §11.5](https://tobby-fetch.github.io/recipe-spec/).

Deux politiques lisent délibérément des références différentes :

- La **liste blanche de registries** (FR-030) et la **recherche de
  credentials** portent sur l'hôte *effectif* réellement contacté — le
  substitut. C'est de là que viennent les octets, c'est donc ce que la
  politique réseau doit nommer.
- Les **périmètres des trust roots** (FR-033) s'appliquent au `ref`
  *nominal* écrit dans la recipe. Une provenance signée ne change pas
  parce que le contenu a été récupéré depuis une copie plus proche.

Les journaux enregistrent la correspondance nominal→effectif à chaque
récupération substituée : un audit peut donc toujours répondre aux deux
questions — « quel était ce contenu » et « d'où viennent ces octets ».

## Credentials entre instances

Une instance aval s'authentifie auprès de son amont comme n'importe quel
client de registre. Créez sur l'instance amont un compte (ou un jeton)
dédié — voir [Authentification et RBAC](../../security/auth-rbac/) —
dont le seul besoin est la lecture, et transmettez-le à l'instance aval
par son fichier de credentials (FR-004) :

```yaml
registries:
  credentialsFile: /etc/tobby-credentials/config.json
```

Le fichier est une charge `dockerconfigjson` standard ; les entrées sont
recherchées par hôte **effectif**, l'entrée nomme donc le registre amont
— `registry.upstream.example` — et non `docker.io`. Il doit vivre en
dehors du store (les secrets ne voyagent jamais sur un média
transportable), et sur Kubernetes c'est un Secret monté que le chart
câble pour vous. Les credentials en écriture suivent le même chemin : le
push vers le registre de destination de la zone utilise le même fichier
de credentials, indexé par l'hôte de destination.

Le versant destination de la même instance a sa propre section de
configuration — `destination.registry`, plus `destination.basePath` et
`destination.cookbook` — délibérément séparée des substitutions : l'une
répond à « où est-ce que je lis », l'autre à « où est-ce que je
promeus », et appliquer une réécriture côté lecture à une écriture
publierait dans un registre que personne n'a nommé.

Ensuite : vos clusters et vos hôtes consomment ce qui est arrivé —
[Brancher vos clients](../../passthrough/connect-clients/).
