---
title: L'air-gap sur un seul poste
description: Le parcours média rejoué sur un seul poste — une instance miroir remplit un dossier qui tient lieu de clé USB, une seconde instance le vérifie avant de servir quoi que ce soit.
sidebar:
  order: 3
---

**Pour aller plus loin, après l'[étape 2](../first-promotion/).** La même
recipe signée traverse maintenant un air gap : une instance l'écrit sur un
support amovible, une seconde — qui tient lieu de zone isolée — vérifie le
support avant de servir le moindre octet. Tout se passe sur ce poste, et
la « clé USB » est un simple dossier.

:::note[Il vous faut]
- Ce que l'étape 2 a laissé dans `tobby-demo` : `cosign.pub`, et le
  registre `cookbook` toujours en marche sur `:5001` avec la recipe
  `alpine` signée dedans (`docker ps` le montre ; si vous l'avez supprimé,
  refaites les sections 1 à 4 de l'[étape 2](../first-promotion/)).
- L'instance de l'étape 2 **arrêtée** (`Ctrl-C`) : cette page démarre ses
  deux propres instances, sur les ports `8081` et `8082`.
- Deux terminaux dans `tobby-demo`, comme avant, et l'accès sortant à
  Docker Hub de l'étape 2 : l'instance source récupère à nouveau l'image.
:::

## Ce que le mode miroir change

À l'étape 2 l'instance tournait en mode *passthrough* : un service qui
réconcilie à intervalle régulier et pousse par le réseau. Là où aucun
chemin réseau n'existe, Tobby tourne en mode **miroir**. Rien ne tourne
sans surveillance : un opérateur déclenche une synchronisation, qui
remplit un store transportable ; le store est porté à la main ; de l'autre
côté, une seconde instance revérifie tout contre **ses propres** racines
de confiance.

| Élément | Rôle |
|---|---|
| Instance source (`:8081`) | Mode miroir, côté connecté. Récupère, vérifie, et écrit le store sur le support. |
| `./usb/tobby-store` | Le support. Sur une vraie clé ce serait `/media/usb/tobby-store` ou `E:/tobby-store`. |
| Instance destination (`:8082`) | Mode miroir, côté isolé. Ouvre le même store, refuse de le servir tant qu'elle ne l'a pas vérifié. |

## 1. Adresser le support à une zone

Un support est adressé à une zone : le nom du Retriever qui l'a rempli.
Écrivez `retriever-isolated.yaml` :

```yaml
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever

metadata:
  name: isolated-zone        # la zone à laquelle le support est adressé

spec:
  cookbook: 127.0.0.1:5001/cookbook
  recipes:
    - name: alpine
      version: "3.24.4"
```

## 2. L'instance source

Écrivez `source.yaml`. La racine du store pointe **sur le support** : en
mode miroir, le store est ce qui voyage.

```yaml
mode: mirror
storage:
  root: ./usb/tobby-store      # le store EST le support
state:
  root: ./state-source         # les comptes restent sur ce poste, jamais sur le support
server:
  addr: 127.0.0.1:8081
retriever:
  source: ./retriever-isolated.yaml
trust:
  roots:
    - name: demo-signing-key
      keyFile: ./cosign.pub
registries:
  insecure: ["127.0.0.1:5001"]
```

Dans le premier terminal, créez son compte admin — la commande lit le mot
de passe sur une ligne de stdin — et démarrez-la :

```sh
echo 'choisissez-un-mot-de-passe' | tobby user add admin --state-root ./state-source --password-stdin
tobby serve --config source.yaml
```

`user add` répond `first account of this instance: role admin`, et le
journal de l'instance se termine par `http server listening`.

## 3. Remplir le support

Une instance miroir ne synchronise jamais d'elle-même. Dans le second
terminal, déclenchez-la — ou utilisez l'action de synchronisation de
l'écran **Recipes** sur `http://127.0.0.1:8081` (connectez-vous en `admin`, avec le mot de passe
que vous venez de choisir) :

```sh
curl -u admin -X POST http://127.0.0.1:8081/api/v1/sync
```

La réponse est la tâche créée, comme à l'étape 2. Suivez-la sur l'écran
**Tâches** : elle se termine en `done` avec deux items, l'image et la
recipe. Rien n'est poussé nulle part — il n'y a pas de destination de ce
côté.

Le support contient maintenant un store complet, et son inventaire, écrit
en dernier :

```sh
ls usb/tobby-store/meta/media.json
```

L'écran **Média** montre la fiche de remise : la zone à laquelle le
support est adressé, les recipes qu'il porte, sa taille.

Arrêtez l'instance source avec `Ctrl-C` **avant** de toucher au support.
On ne débranche jamais une clé pendant qu'une instance la tient ouverte.

## 4. Le côté isolé

Dans la réalité c'est une autre machine, sans route vers l'extérieur, qui
détient **sa propre** copie de `cosign.pub` — une clé qui aurait voyagé
sur le support serait ignorée. Ici le même dossier joue cette machine.

Écrivez `destination.yaml`. Il n'y a ni Retriever ni registre source : le
contenu arrive sur le support. On dit à l'instance quelle zone elle sert :

```yaml
mode: mirror
zone: isolated-zone            # refuse un support adressé à une autre zone
storage:
  root: ./usb/tobby-store
state:
  root: ./state-destination
server:
  addr: 127.0.0.1:8082
trust:
  roots:
    - name: demo-signing-key
      keyFile: ./cosign.pub
```

Créez son compte et démarrez-la, dans le premier terminal :

```sh
echo 'choisissez-un-mot-de-passe' | tobby user add admin --state-root ./state-destination --password-stdin
tobby serve --config destination.yaml
```

## 5. Rien n'est servi avant vérification

Le contenu est sur le disque, et l'instance tourne. Demandez-lui les tags
de l'image, dans le second terminal :

```sh
curl -u admin http://127.0.0.1:8082/v2/docker.io/library/alpine/tags/list
```

Elle refuse — HTTP 403, avec le code `TBY-MED-030` et la marche à
suivre : le support est là, mais personne ne l'a encore vérifié de ce
côté.

Vérifiez-le : ouvrez `http://127.0.0.1:8082`, connectez-vous en `admin`, allez sur l'écran **Média**
et lancez la vérification du support — ou appelez l'API :

```sh
curl -u admin -X POST http://127.0.0.1:8082/api/v1/media/verify
```

L'instance contrôle, dans cet ordre, l'inventaire, le digest de chaque
fichier, puis la signature de chaque recipe contre la racine de confiance
de la destination. Le rapport commence par le verdict :

```
{"verdict":"pushable","media":{"mediaId":"…","zone":"isolated-zone", …
```

Redemandez les tags :

```sh
curl -u admin http://127.0.0.1:8082/v2/docker.io/library/alpine/tags/list
```

```
{"name":"docker.io/library/alpine","tags":["3.24.4"]}
```

Le verrou s'est ouvert de lui-même dès que le support a passé la
vérification. Redémarrez l'instance et il se referme : une vérification
vaut pour le support tel qu'il a été lu à ce moment-là, pas pour ce qui
sera branché ensuite.

## 6. Le même contrôle en ligne de commande

Arrêtez l'instance destination (`Ctrl-C` dans le premier terminal). Alors
`tobby media verify` rend le même verdict
dans son propre processus, sans rien écrire sur le support :

```sh
tobby media verify --config destination.yaml
```

```
medium … addressed to zone isolated-zone, resolved …, produced by tobby v1.0.0-rc.1
verdict: pushable (135 checked files, 30315074 bytes)
  pushable  alpine@3.24.4 (129 files, 30311807 bytes)
```

Son code de sortie est `0` (`echo $?` juste après la commande l'affiche ;
sous PowerShell, `$LASTEXITCODE`). Présentez maintenant le même support à la
mauvaise zone :

```sh
tobby media verify --config destination.yaml --zone another-zone
```

```
verdict: blocked (1 checked files, 2196 bytes)
  TBY-MED-006 blocking (an administrator may override it)
```

Le code de sortie est `3` : refusé par la politique. Le message complet
suit sur la sortie d'erreur : il nomme la cause, l'action, et l'entrée de
la [référence des erreurs](../../reference/errors/#tby-med-006).

:::note[Si une étape refuse]
Chaque refus de cette page nomme un code `TBY-MED-…`.
[Dépannage — un média amovible est bloqué](../../reference/troubleshooting/#un-média-amovible-est-bloqué)
part du symptôme.
:::

## Ce que cette page a laissé de côté

Une vraie zone isolée a en général son propre registre, et la dernière
étape du parcours est la **poussée** : avec `destination.registry` posé
sur l'instance destination, l'action de poussée de l'écran Média — ou
`tobby media import` — pousse ce que la vérification a laissé passer,
exactement comme l'instance passthrough à l'étape 2. Cela, les contrôles
de pré-vol avant de remplir une clé, et ce que devient un support au fil
de plusieurs voyages sont dans la section air-gap :

- [Le parcours média](../../air-gap/media-workflow/) — les cinq étapes et
  qui fait quoi.
- [Préparer un support côté source](../../air-gap/prepare-source/).
- [Importer côté zone isolée](../../air-gap/import-destination/).

Pour ranger : `Ctrl-C` sur toute instance encore en marche,
`docker rm -f cookbook zone`, puis supprimez le répertoire `tobby-demo`.
