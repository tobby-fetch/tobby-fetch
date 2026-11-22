---
title: La voie d'urgence
description: Une recipe écrite localement, signée avec la clé d'urgence de l'organisation et admise sous un périmètre de confiance « emergency » déclaré — la façon documentée et visible de livrer ce que la chaîne normale ne peut pas livrer à temps.
sidebar:
  order: 3
---

Il arrive que la chaîne de qualification ne puisse pas produire à temps ce
dont une zone a besoin : une image corrigée construite en interne, un lot de
configuration qui doit changer ce soir. La mauvaise réponse est d'assouplir la
vérification. La bonne est une **seconde porte, plus étroite**, déclarée à
l'avance : une recipe écrite localement, signée avec une clé que l'organisation
garde exactement pour cela, admise sous un périmètre de confiance qui le dit
sur toutes les surfaces.

Cette page n'ajoute aucun interrupteur à Tobby. Elle assemble quatre
mécanismes existants — le format de recipe, `tobby recipe push`, la
vérification cosign et les [périmètres de confiance](../../security/content-trust/)
— en une procédure que vous pouvez répéter avant le jour où vous en aurez
besoin.

## Ce qu'est la voie d'urgence, et ce qu'elle n'est pas

- **Elle est signée.** Tobby ne signe rien, ici comme partout : vous signez
  avec cosign, hors de Tobby, avec une clé que l'organisation maîtrise. La
  recipe est vérifiée comme toute autre, contre un trust root.
- **Elle est cloisonnée, par la configuration.** La clé d'urgence n'est de
  confiance que pour un seul chemin de cookbook : un root listé par un
  périmètre est réservé à ce périmètre (étape 2).
- **Elle est visible.** Le nom du périmètre accompagne la recipe dans l'API,
  les rapports de tâche, le plan et l'écran Média, et le périmètre fait
  partie de la configuration effective.
- **Ce n'est pas un contournement.** Les mêmes contrôles s'appliquent : les
  digests épinglés sont vérifiés, les registries doivent figurer sur la liste
  d'autorisation, le contenu est analysé selon la
  [politique de vulnérabilités](../../security/vulnerability-scanning/) de
  l'instance, et un support qui porte la recipe est revérifié côté destination
  contre les trust roots propres à la destination.
- **« emergency » est un nom, pas un mode.** Tobby ne l'interprète pas : tout
  nom de périmètre convient. Le mot est une convention qui rend les rapports
  lisibles par quelqu'un qui n'était pas là.

## À préparer une fois, avant l'urgence

### 1. Une paire de clés d'urgence, gardée à part

Générez une paire de clés cosign dédiée et gardez la clé privée là où
l'organisation garde ses secrets les plus restreints, hors de tout hôte Tobby :

```sh
cosign generate-key-pair      # écrit cosign.key et cosign.pub
mv cosign.key emergency.key
mv cosign.pub emergency.pub
```

Seule `emergency.pub` est un jour remise à Tobby. La façon dont la clé privée
est conservée et qui peut l'utiliser (garde partagée, jeton matériel,
procédure « bris de glace ») relève de l'organisation et sort volontairement
du périmètre de Tobby (ADR-0007).

### 2. Un cookbook d'urgence, et un périmètre qui le clôt

Choisissez un chemin de cookbook réservé aux urgences, par exemple
`registry.example.com/emergency`. Déclarez ensuite, dans le fichier de
configuration (`trust.*` est une valeur de type liste, donc fichier de
configuration uniquement) :

```yaml
trust:
  roots:
    - name: org-release-key
      keyFile: /etc/tobby/keys/org-release.pub
    - name: org-emergency-key
      keyFile: /etc/tobby/keys/emergency.pub

  scopes:
    # La porte d'urgence : ce cookbook, vérifié contre la seule clé
    # d'urgence. La lister ici la RÉSERVE aussi : elle ne vérifie rien
    # hors de ce périmètre.
    - name: emergency
      repositories: ["registry.example.com/emergency/**"]
      roots: [org-emergency-key]
```

Un seul périmètre suffit. Un root listé par un périmètre est réservé à ce
périmètre : la clé d'urgence vérifie le cookbook d'urgence et rien d'autre,
et tout ce qui est hors du périmètre est vérifié contre l'**ensemble par
défaut** — les roots qu'aucun périmètre ne liste, ici `org-release-key` seul.
Une recipe du cookbook normal signée avec la clé d'urgence est refusée avec
[`TBY-SIG-001`](../../reference/errors/#tby-sig-001), et le message nomme
`org-emergency-key` et le périmètre `emergency` auquel elle est réservée.

:::caution[Changé en v1.0.0-rc.1]
Jusqu'à la v0.6.x, un dépôt qui ne correspondait à aucun périmètre était
vérifié contre **tous** les roots configurés, clé d'urgence comprise (B-057),
et cette page recommandait un second périmètre fourre-tout
(`repositories: ["**"]`, `roots: [org-release-key]`) pour cantonner la clé
d'urgence. Ce périmètre n'est plus nécessaire. Il fonctionne toujours, la
première correspondance l'emportant après `emergency`, et peut être retiré.
:::

Les motifs s'appliquent au chemin nominal du cookbook de la recipe, sous forme
canonique : hôte en minuscules, le `:` d'un port écrit `_`
(`registry.example.com_5000/…`). Voir
[Signatures, trust roots et liste d'autorisation](../../security/content-trust/#périmètres-dassouplissement--et-leur-visibilité).
Le périmètre ne positionne pas `allowUnsigned` : la voie d'urgence
n'admet jamais de contenu non signé, elle ne porte donc aucun
[marqueur d'assurance réduite](../../security/reduced-assurance/) et ne
déclenche aucun bandeau. Sa visibilité, c'est le nom du périmètre, ci-dessous.

Ajoutez le registry d'urgence à `registries.allowlist` si l'instance en a une,
et donnez au Retriever un cookbook par entrée (étape 6). Redémarrez l'instance
et vérifiez le résultat :

```sh
tobby config dump --config /etc/tobby/config.yaml | grep -A12 '^trust:'
```

Sans `--config`, `tobby config dump` lit `/etc/tobby/config.yaml`. Passez le
fichier avec lequel votre instance tourne réellement.

Répétez toute la procédure une fois sur une zone de test. Découvrir une faute
de frappe dans un motif de périmètre coûte bien moins cher un jour calme.

## Le jour venu

### 3. Écrire la recipe

Écrivez la recipe comme n'importe quelle autre — voir
[Écrire, publier et signer une recipe](../write-and-publish/) — contre les
[schémas JSON](https://github.com/tobby-fetch/recipe-spec/tree/main/schemas)
publiés ou le SDK Go (`github.com/tobby-fetch/recipe-spec/recipe/v1alpha1`,
`ParseRecipe`). Une recipe d'urgence est une recipe **cuisinée** (*cooked*) :
chaque ingrédient a une `version` exacte et un `digest`.

```yaml
apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe
metadata:
  name: hotfix-gateway
  version: 1.4.2-hotfix.1
  description: Passerelle reconstruite avec la bibliothèque TLS corrigée.
  annotations:
    example.com/ticket: INC-2041     # pourquoi elle existe, pour le prochain lecteur
spec:
  ingredients:
    - name: gateway
      kind: ContainerImage
      ref: registry.build.example.com/platform/gateway
      version: 1.4.2-hotfix.1
      digest: sha256:REMPLACER-PAR-LE-DIGEST-DE-L-INDEX
      platforms: ["linux/amd64"]
```

Le nom et la version de la recipe sont liés à son emplacement de publication :
`registry.example.com/emergency/hotfix-gateway:1.4.2-hotfix.1`. Mettez la
raison dans `metadata.annotations` ; la personne qui lira le rapport en aura
besoin.

### 4. Prendre les digests dans le navigateur de contenu

Un ingrédient est épinglé par le digest de son **manifeste de premier
niveau** (pour une image multi-plateforme, l'index). Si le contenu est déjà
détenu par une instance, prenez le digest dans l'écran **Content** : ouvrez le
dépôt, trouvez le tag et utilisez le bouton de copie à côté de son digest (la
même valeur figure dans `GET /api/v1/content`). S'il n'est que dans un
registry, lisez-le là-bas avec `crane digest <ref>` ou
`oras manifest fetch --descriptor`. Ne retapez jamais un digest, et n'en
prenez jamais un à partir d'un tag que vous n'avez pas regardé deux fois : le
tag est ce qui bouge, le digest est le contenu.

Validez avant de publier :

```sh
recipe lint --profile cooked hotfix-gateway.yaml
```

### 5. Publier, puis signer par digest

```sh
DIGEST=$(tobby recipe push hotfix-gateway.yaml \
  registry.example.com/emergency/hotfix-gateway:1.4.2-hotfix.1)

cosign sign --key emergency.key --yes \
  --use-signing-config=false --tlog-upload=false \
  "registry.example.com/emergency/hotfix-gateway@${DIGEST}"
```

`tobby recipe push` refuse tout ce qu'une zone refuserait plus tard
(ingrédients non épinglés, nom ou version qui contredit la référence) et
imprime le seul digest sur la sortie standard. Les deux options de cosign
gardent la signature vérifiable entièrement hors ligne et hors du journal
public Rekor ; elles ne sont pas facultatives pour une zone restreinte (voir
[Signer avec cosign](../write-and-publish/#signer-avec-cosign)). Tobby ne
détient aucune clé à aucun moment de cette étape.

Vérifiez ce que vous venez de signer avant que quiconque s'y fie :

```sh
cosign verify --key emergency.pub --insecure-ignore-tlog=true \
  "registry.example.com/emergency/hotfix-gateway@${DIGEST}"
```

### 6. L'admettre : une entrée de Retriever avec son propre cookbook

Le Retriever est l'état désiré de la zone. Ajoutez la recipe avec un
`cookbook` par entrée qui pointe vers le cookbook d'urgence (RECIPE-SPEC
§10.2), de sorte que l'entrée soit encadrée par le périmètre `emergency` et
que le reste du Retriever continue à se résoudre depuis le cookbook normal :

```yaml
spec:
  cookbook: registry.example.com/cookbook
  recipes:
    # … les entrées habituelles de la zone …
    - name: hotfix-gateway
      version: "1.4.2-hotfix.1"
      cookbook: registry.example.com/emergency
```

Planifiez avant de promouvoir. Un plan ne change rien et montre le verdict de
signature de chaque recipe et, dans sa colonne `SCOPE`, le périmètre de
confiance qui l'a décidé — `emergency` sur cette entrée, `-` sur les autres :

```sh
tobby sync --dry-run --retriever ./retriever.yaml
```

Déclenchez ensuite la synchronisation (jeton `operator`, `TOBBY_API_TOKEN`) :

```sh
tobby sync --wait
```

Si la signature ne se vérifie pas contre `org-emergency-key`, la recipe est
refusée avec [`TBY-SIG-001`](../../reference/errors/#tby-sig-001), et le
message nomme le périmètre qui a refusé. Une signature faite avec la clé
normale `org-release-key` ne se vérifie pas non plus ici : les `roots` d'un
périmètre forment un compartiment.

## Où l'urgence reste visible

La recipe est enregistrée avec le verdict et le périmètre qui l'a admise.
Rien n'est caché parce que la signature est valide.

| Surface | Ce que vous voyez |
| --- | --- |
| Configuration effective | `trust.scopes` avec `emergency`, ses dépôts et ses roots (`tobby config dump`, et la configuration affichée par l'instance). |
| Recipes | `GET /api/v1/recipes` : `verified: true` et `trustScope: "emergency"` sur la recipe enregistrée. L'écran Recipes n'affiche que le badge « verified » pour une recipe vérifiée ; il imprime le périmètre à côté du verdict lorsque la recipe n'est pas vérifiée. |
| Rapports de tâche | `trust_scope: "emergency"` sur la ligne de la recipe dans `resolutions` de la tâche de synchronisation (`GET /api/v1/tasks/{id}`). |
| Plan | Le périmètre de confiance qui a décidé du verdict de chaque recipe : la colonne `SCOPE` de `tobby sync --dry-run`, `trust_scope` dans son rapport `--output json`, et l'écran de plan. |
| Média | Le périmètre est affiché à côté de la recipe sur l'écran Média d'un support qui la porte ; la destination rend son **propre** verdict à partir de ses propres trust roots, elle doit donc déclarer le même périmètre d'urgence et le même root pour admettre la recipe. |
| Journal d'audit | La publication par l'interface ou l'API est un événement `recipe.publish` ; une synchronisation est `sync.create`. Voir le [journal d'audit](../../security/audit-log/). |

:::note
Le bandeau permanent et le [marqueur d'assurance réduite](../../security/reduced-assurance/)
sont réservés aux périmètres qui renoncent à quelque chose (`allowUnsigned`).
Un périmètre qui se contente de **restreindre** les clés autorisées à vérifier,
comme `emergency`, ne renonce à rien et ne marque donc rien ; sa visibilité est
le nom du périmètre porté par les surfaces ci-dessus.
:::

## Après l'urgence

La voie d'urgence sert à livrer, pas à s'installer. Refermez-la proprement :

1. Faites qualifier, cuisiner et signer le même changement par la chaîne
   normale dans le cookbook standard, puis remplacez l'entrée du Retriever et
   supprimez la surcharge `cookbook`.
2. Refermez la porte : retirez de `trust` le périmètre `emergency` **et** le
   root `org-emergency-key`, ensemble, puis redémarrez l'instance. Il reste
   la configuration d'une zone sans voie d'urgence :

   ```yaml
   trust:
     roots:
       - name: org-release-key
         keyFile: /etc/tobby/keys/org-release.pub
   ```

   La recipe d'urgence est alors refusée avec
   [`TBY-SIG-001`](../../reference/errors/#tby-sig-001), comme toute recipe
   qu'aucun root configuré ne vérifie.
3. Renouvelez la clé d'urgence si elle est sortie de sa garde. La rotation se
   fait par recouvrement, comme pour tout trust root : ajoutez le nouveau
   root, re-signez ce qui est encore utilisé, puis retirez l'ancien.
4. Conservez la référence du ticket dans les annotations de la recipe et la
   synchronisation dans vos archives : le rapport de tâche et le journal
   d'audit sont la preuve de qui a admis quoi, et quand.

:::danger[Ne retirez jamais le périmètre seul]
C'est le périmètre qui enclôt la clé d'urgence. Un root qu'aucun périmètre
ne liste appartient à l'**ensemble par défaut**, qui vérifie tout cookbook
hors périmètre. Retirez `emergency` de `trust.scopes` en laissant
`org-emergency-key` dans `trust.roots`, et la clé d'urgence n'est plus
enclose : elle rejoint l'ensemble par défaut et vérifie tous les cookbooks. La recipe d'urgence est toujours admise — sous
aucun périmètre — et avec elle tout ce qui est signé de cette clé, dans
n'importe quel cookbook. Retirez les deux, ou aucun.
:::

Les versions de recipe publiées sont immuables. Corriger la recipe d'urgence
passe par une nouvelle `metadata.version`, jamais par un nouveau push sur le
même tag.

## Pages liées

- [Signatures, trust roots et liste d'autorisation](../../security/content-trust/) : roots, périmètres, les deux formats de signature cosign.
- [Écrire, publier et signer une recipe](../write-and-publish/) : le chemin d'écriture que suit cette procédure.
- [Le modèle de sécurité](../../security/model/) : comment cela s'inscrit dans les autres garanties.
- [Dépannage](../../reference/troubleshooting/) : `TBY-SIG-001`, `TBY-POL-001` et les autres.
