---
title: Signatures, trust roots et liste d'autorisation
description: Comment les trust roots sont configurés, ce que les périmètres d'assouplissement autorisent réellement, les deux formats cosign acceptés et la sémantique de la liste d'autorisation des registries.
sidebar:
  order: 3
---

Tobby vérifie les signatures cosign **à clé**, entièrement hors ligne : ni
Fulcio, ni Rekor, aucun service en ligne (FR-033, ADR-0007 — livré en
v0.3.0). La signature sans clé (keyless) dépend de services inaccessibles
depuis les zones restreintes ; ce n'est donc pas le modèle retenu, celui-ci
repose sur les clés de l'organisation.
La vérification a lieu deux fois pour chaque élément : à l'import dans le
store, puis de nouveau avant tout push vers une destination. Une altération
au repos ou en transit est ainsi arrêtée à la dernière barrière, pas
seulement à la première. Côté destination d'un transfert physique, les mêmes
contrôles sont rejoués sur le store transporté (FR-052).

Tobby ne signe jamais. La signature a lieu dans la chaîne de qualification de
l'organisation ; le [guide de publication de recipe-spec](https://tobby-fetch.github.io/recipe-spec/guides/publishing-recipes/)
donne les commandes cosign 3.x vérifiées.

## Trust roots

Un trust root est une clé publique de confiance, nommée, configurée sur
l'instance (`trust.roots`, fichier de configuration uniquement). Chaque root
prend exactement une des trois formes :

```yaml
trust:
  roots:
    - name: org-release-key
      keyFile: /etc/tobby/keys/org-release.pub   # file on disk
    - name: platform-team
      key: |                                     # inline PEM
        -----BEGIN PUBLIC KEY-----
        ...
        -----END PUBLIC KEY-----
    - name: partner
      keyURL: https://pki.example.com/partner.pub # fetched at configuration
                                                  # time, never at verification time
```

Les instances isolées utilisent les formes en ligne ou fichier. Un `keyURL`
est résolu et mis en cache au chargement de la configuration : la
vérification elle-même ne touche jamais le réseau (FR-033).

**Rotation par recouvrement** (ADR-0007) : les roots forment un ensemble.
Pour renouveler une clé, ajoutez le nouveau root à côté de l'ancien, re-signez
les recipes en amont, puis retirez l'ancien root quand plus rien de vérifié
par lui n'est en cours. Il n'y a pas de bascule instantanée pendant laquelle
la vérification devrait être assouplie.

## Périmètres d'assouplissement — et leur visibilité

La vérification est active par défaut pour toute recipe. Il n'existe **aucun
contournement global**. Le seul assouplissement est un périmètre déclaré
(`trust.scopes`) : nommé, limité à des motifs de dépôts explicites, évalué dans
l'ordre de déclaration, le premier qui correspond l'emporte ; sans
correspondance, c'est le défaut strict — une signature vérifiée contre
l'ensemble par défaut (ci-dessous).

```yaml
trust:
  scopes:
    - name: lab-experiments
      repositories: ["lab.example.com_5000/cookbook/*"]
      allowUnsigned: true          # reported on every surface, never silent
    - name: partner-cookbook
      repositories: ["partner.example.com/cookbook/**"]
      roots: [partner]             # restrict WHICH roots may verify here
```

Les motifs s'appliquent au chemin de cookbook *nominal* de la recipe, sous
forme canonique : hôte en minuscules, alias Docker Hub ramenés à `docker.io`,
le `:` d'un port écrit `_` (un deux-points ne peut pas figurer dans un chemin
de dépôt). `*` reste dans un segment de chemin, `**` traverse les
séparateurs. La confiance suit la référence nominale, jamais un point d'accès
substitué (FR-036).

Un périmètre qui admet du contenu non signé n'est pas un réglage discret : il
est signalé dans la configuration effective, dans les journaux et dans chaque
rapport de tâche qui l'a utilisé (principe de FR-075 — les assouplissements de
sécurité sont visibles par construction). Ce qu'il a admis reste visible lui
aussi, élément par élément : le contenu admis sous un périmètre assoupli porte
un [marqueur d'assurance réduite](../reduced-assurance/) sur chaque surface
qui le rapporte.

**Les `roots` d'un périmètre forment un compartiment.** Une recipe qui
correspond à un périmètre listant des `roots` n'est vérifiée que contre ces
roots. Une clé qui est un trust root valide ailleurs dans la même
configuration, mais que le périmètre ne liste pas, ne la vérifie pas, et le
refus nomme le périmètre : `TBY-SIG-001` porte un paramètre `scope`, de sorte
que vous lisez quel périmètre a refusé la recipe au lieu de parcourir tous les
roots ([référence des erreurs](../../reference/errors/#tby-sig-001)). Il en va
de même lors d'une synchronisation, de la revérification de signature avant un
push, dans un plan, et à la vérification d'un support transporté côté
destination.

**Un root listé par un périmètre est réservé à ce périmètre.** Il vérifie dans
les périmètres qui le listent, et nulle part ailleurs. Ce qui s'applique hors
de tout périmètre — et dans un périmètre qui ne liste aucun `roots`, comme un
périmètre `allowUnsigned` seul — est l'**ensemble par défaut** : les roots
qu'aucun périmètre ne liste. Dans l'exemple ci-dessus, `partner` vérifie les
recipes de `partner.example.com` et rien d'autre, tandis que
`org-release-key` et `platform-team` vérifient tout ce qui est hors de ce
périmètre. La réservation suit la clé elle-même : la même clé publique
configurée une seconde fois sous un nom qu'aucun périmètre ne liste reste
réservée. Quand chaque root configuré est listé par un périmètre, l'ensemble
par défaut est vide et toute recipe hors des périmètres est refusée, le
message le disant.

Quand une signature refusée est pourtant valide pour un root qui ne
s'applique pas ici, `TBY-SIG-001` dit lequel : le nom et l'empreinte du root
(paramètre `reserved`) et le périmètre auquel il est réservé (`reservedto`),
de sorte que la correction — élargir ce périmètre, ou re-signer avec une clé
qui s'applique — se lit dans le refus lui-même.

:::caution[Changé en v1.0.0-rc.1 — rupture]
Jusqu'à la v0.6.x, le défaut strict vérifiait contre **tous** les roots
configurés, listés ou non par un périmètre : une clé listée par un périmètre
vérifiait aussi toute recipe hors de celui-ci (B-057). Une configuration qui
s'appuyait sur ce comportement — une recipe hors de tout périmètre signée par
une clé que seul un périmètre liste — est désormais refusée avec
`TBY-SIG-001`. Ajoutez un root qu'aucun périmètre ne liste pour ce contenu, ou
étendez les `repositories` du périmètre pour le couvrir. Le contournement
précédent, un périmètre fourre-tout `**` ne listant que les clés normales,
n'est plus nécessaire et peut être retiré.
:::

:::note[À venir — provenance, contenu par contenu]
Une évolution reste sur la feuille de route : la **provenance visible contenu
par contenu** — vérifié par recipe, import unitaire, dépôt direct (R-17 — jalon
7). Suivez-la sur la page [état du projet](../../discover/status/).
:::

Un périmètre peut aussi **restreindre** plutôt qu'assouplir : ses `roots`
cantonnent une clé dédiée, comme la clé d'urgence de l'organisation, à un seul
cookbook — et, la clé étant réservée, à ce seul cookbook. C'est le mécanisme de la [voie d'urgence](../../recipes/emergency-path/)
documentée.

## Les deux formats de signature acceptés

Les deux dispositions cosign sont vérifiées, si bien que des recipes signées à
des années d'écart coexistent (RECIPE-SPEC §12.2) :

- **Attachée (historique)** — la signature de `<repo>@sha256:<hex>` est un
  manifeste OCI du même dépôt, sous le tag `sha256-<hex>.sig`, portant des
  charges SimpleSigning qui épinglent le digest du sujet.
- **Sigstore bundle (défaut de cosign 3.x)** — un artefact OCI dont
  l'`artifactType` est le media type du bundle
  (`application/vnd.dev.sigstore.bundle…`), qui référence le manifeste sujet,
  avec une seule couche contenant le JSON du bundle.

Dans les deux cas, la signature voyage avec le contenu — à travers les
registries, les cascades et sur les supports — ce qui permet à la destination
de tout revérifier à partir de ses propres trust roots. Les trust roots
trouvés sur un support sont ignorés : ils sont arrivés avec le contenu dont
ils se porteraient garants.

## Liste d'autorisation des registries

La liste d'autorisation borne les registries que l'instance peut contacter,
comme source ou comme destination, évaluée **avant tout transfert** (FR-030 —
livré en v0.4.0) :

```yaml
registries:
  allowlist:
    - registry.example.com
    - "*.internal.example.com"    # * within one DNS label, ** across labels
    - lab.example.com:5000
```

Deux sémantiques comptent pour un auditeur :

- **Absent n'est pas vide.** Sans clé `allowlist`, aucune restriction — signalé
  partout comme *non déclaré*, jamais comme une politique satisfaite.
  `allowlist: []` signifie que rien n'est autorisé. La distinction est voulue
  (même forme qu'une NetworkPolicy Kubernetes).
- **C'est la référence effective qui est contrôlée** (FR-036). Quand la
  substitution de source réécrit l'endroit d'où une référence est récupérée,
  la liste d'autorisation et les identifiants s'appliquent à l'hôte *réellement
  contacté* ; les périmètres de confiance s'appliquent à la référence nominale.
  Les journaux consignent la correspondance nominale→effective.

Un refus est l'erreur dédiée `TBY-POL-001` (HTTP 403, qui nomme l'hôte — voir
les [erreurs](../../reference/errors/)) ; il est journalisé et compté dans les
familles de métriques `tobby_policy_rejections_total` et
`tobby_promotion_refusals_total`, par code de taxonomie
([métriques et journaux](../../reference/metrics-logs/)).
