---
title: Gérer les supports dans la durée
description: Identité et fraîcheur des supports, prune aligné sur le Retriever et seuil d'occupation, dimensionnement d'un support qui tourne, la sortie de secours OCI image layout, et la remise à zéro auditée du store.
sidebar:
  order: 4
---

Un support qui traverse l'air gap une fois est un transfert. Un support qui
le traverse tous les mois est une procédure d'exploitation, et il pose des
questions que le premier voyage ne pose pas : est-ce le bon disque, est-ce
celui de ce mois-ci, pourquoi a-t-il grossi, et comment le repartir à zéro.

## Identité et fraîcheur

Chaque store reçoit un **identifiant de support** à sa création. Il reste
stable au fil des re-synchronisations sur le même store et diffère sur un
store neuf : un support est ainsi traçable en tant qu'objet physique.
L'identifiant figure dans le manifeste, dans les journaux des deux côtés, et
dans chaque refus qui concerne le support.

La destination mémorise, **dans son répertoire d'état et jamais dans le
store**, l'identifiant et l'horodatage de résolution du dernier support
qu'elle a importé pour chaque zone. Un support plus ancien que ce registre
est refusé par défaut
([`TBY-MED-007`](../../reference/errors/#tby-med-007)) — l'accident classique
de la réimportation du disque du mois dernier, qui ferait reculer une zone.

Trois propriétés de cette garde méritent d'être dites :

- Le registre vit dans le répertoire d'état parce qu'un registre porté sur
  le support serait réécrit par qui détient le support.
- Il n'avance que sur les **imports terminés**. Une vérification qui a eu
  lieu et un push qui n'a pas eu lieu ne déplacent rien.
- La borne **ne recule jamais**, y compris quand un administrateur lève le
  refus pour restaurer volontairement une livraison plus ancienne.

Ce n'est pas un contrôle de sécurité. Le manifeste est non signé, donc
l'horodatage se forge ; la garde prévient un accident, et c'est pour cela
qu'elle est levable. Idem pour l'identité de zone : **un support, une
zone**. Un store porte l'identité de la zone pour laquelle il a été préparé,
la destination la fait respecter
([`TBY-MED-006`](../../reference/errors/#tby-med-006)), et la règle
d'exploitation honnête est de dimensionner un support par zone et par cycle
plutôt que de partager un disque physique entre zones.

## Garder un support à une taille raisonnable

### Le prune aligné sur le Retriever

Le contenu que le Retriever de la zone ne demande plus est retiré du store
au moment de la synchronisation. **En mode miroir, c'est actif par défaut**
et confirmé au déclenchement : la liste et la taille totale de ce qui
partirait sont affichées avant l'opération, depuis une projection que
l'instance recalcule à chaque affichage plutôt que de la mettre en cache.
`sync.prune` est un réglage de passthrough et est refusé dans une
configuration miroir — un prune qui pourrait tourner sans surveillance n'est
pas un prune qu'une personne a confirmé.

Seul le contenu dont la provenance enregistrée est `recipe` est éligible.
C'est un test positif et non une liste d'exclusion, ce qui protège trois
sortes de contenu par construction plutôt que par bonne mémoire :

- **les imports unitaires** — entrés hors de toute recipe ;
- **la base de vulnérabilités hors-ligne** — elle arrive par la même porte ;
- **tout ce qui est poussé par `/v2/`** par un client standard, le cas de
  l'amorçage.

Deux garde-fous s'ajoutent. Un cycle où une recipe n'a pas pu être résolue
ne prune rien et le dit : le contenu d'une recipe non résolue est
indiscernable du contenu que le Retriever a retiré, et supprimer sur la foi
d'une panne réseau n'est pas un compromis que ce produit accepte. Et chaque
élément retiré est nommé — dépôt, tag, digest, et la recipe qui l'avait
apporté — dans le journal de la course, pas seulement compté.

Le résultat, c'est qu'un support qui tourne reste proportionnel aux besoins
*courants* de la zone plutôt qu'à son historique.

### Surveiller le volume

Posez `storage.occupancyThreshold` et l'instance dit quand le store le
dépasse : un avertissement permanent sur chaque page de l'interface, le même
fait sur l'API, et la métrique `tobby_store_occupancy_exceeded`. Repasser en
dessous rétracte les trois — un avertissement qui apparaît et ne s'efface
jamais est un avertissement que les exploitants apprennent à ignorer.

Il **avertit et ne refuse jamais**. Refuser une synchronisation parce qu'un
store a grossi laisserait une zone en plan, et ce qui refuse légitimement
sur l'espace, c'est le
[pré-vol](../../air-gap/prepare-source/), qui compare une projection précise
à un volume précis.

Non posé signifie **non surveillé**, ce qui est rapporté comme tel et jamais
comme « dans les clous » : Tobby ne peut pas deviner la taille du volume
qu'on lui a donné.

### Dimensionner un cycle

La déduplication par digest rend le deuxième cycle différentiel — un support
dimensionné pour le premier transfert complet est confortable ensuite. Ne
devinez pas : lancez
[`tobby sync --dry-run`](../../reference/cli/#tobby-sync) avant chaque cycle
et lisez la projection. Le dimensionnement se mesure.

## La sortie de secours : OCI image layout

Le store — ou une sélection — peut être écrit au format standard **OCI image
layout**, lisible par `skopeo`, `oras` et `crane`, et réimporté à digests
identiques. C'est délibéré : le contenu appartient à qui l'a stocké, et il
doit être récupérable sans Tobby.

```sh
tobby export --storage-root /var/lib/tobby/storage /media/usb/payload.tar
tobby export --storage-root /var/lib/tobby/storage /media/usb/payload.tar --dry-run --output json
tobby import --storage-root /var/lib/tobby/storage /media/usb/payload.tar
```

Un tar unique non compressé par défaut — un fichier traverse une coupure
physique plus sûrement qu'une arborescence — ou un répertoire avec
`--directory`. `--recipe` et `--repository` restreignent la sélection et
sont répétables ; une sélection par recipe emporte ses ingrédients,
l'artefact de recipe, et les artefacts de signature cosign des deux, dans
l'un ou l'autre des formats que cosign publie. Les signatures voyagent avec
le contenu qu'elles attestent.

Adresser une entrée ensuite dépend de l'outil, parce que les outils ne sont
pas d'accord. Chaque entrée d'index est annotée de son dépôt complet et de
son tag, ce sur quoi `skopeo` s'appuie ; `oras` coupe une référence de
layout sur son dernier deux-points et adresse donc les entrées par digest :

```sh
skopeo copy oci:/media/usb/payload:registry.example.com/apps/harbor:2.15.2 …
oras manifest fetch --oci-layout /media/usb/payload@sha256:…
```

Au retour, le layout est traité comme une **donnée non fiable** : chaque
manifest n'est accepté que si ses octets donnent le digest qui l'adresse,
chaque blob est validé contre le digest que son manifest épingle, et une
archive portant autre chose que `oci-layout`, `index.json` et
`blobs/<algorithme>/<digest>` est refusée avant d'être lue
([`TBY-LAY-002`](../../reference/errors/#tby-lay-002)). Les archives
compressées sont refusées plutôt que décompressées : décompressez d'abord.
Les entrées sont indépendantes — une image qui n'a pas survécu au support
échoue sur sa propre ligne et le reste atterrit quand même.

Un layout produit par `skopeo copy` ne nomme que le tag, qui n'est pas un
emplacement ; donnez `--repository` pour dire où l'archive entière
appartient.

**C'est le format d'interopérabilité, pas le format principal, et la raison
est ce qu'il ne porte pas** : le layout ne contient que des artefacts. Les
journaux, l'état de synchronisation, le graphe de recipes et le manifeste de
média sont des concepts du store et restent derrière. Un OCI layout, c'est
la façon dont du contenu sort de ce produit ; un store, c'est la façon dont
une livraison voyage.

Lancez l'une ou l'autre commande contre une instance **arrêtée**, ou
utilisez `POST /api/v1/oci-layout/export` et `.../import` sur une instance
qui tourne : deux processus qui écrivent un même répertoire de stockage,
c'est un processus de trop. Les mêmes opérations sont sur l'écran
`/admin/oci-layout`, avec l'estimation sans effet de bord d'abord.

<!-- TODO: screenshot: l'écran OCI image layout — une sélection estimée avant export, avec le total projeté et le plus gros fichier -->

## Repartir d'un support propre

`/admin/store` montre ce que le store détient et propose la remise à zéro
qui le vide (FR-046). Elle est réservée aux administrateurs, exige la
confirmation tapée exacte `RESET`, et est consignée à l'audit — y compris la
tentative refusée, parce que quelqu'un qui tape le mauvais mot dans ce champ
est le premier signal de la piste. Sur une instance qui tourne avec la
dérogation d'authentification, la confirmation tapée reste et l'entrée
d'audit enregistre le contexte non authentifié.

La phrase de confirmation est délibérément figée et non traduite : elle est
citée dans la piste d'audit et dans les procédures de site, et une
confirmation qui se lit différemment selon la langue est une confirmation
dont deux personnes ne peuvent pas parler. Un écart donne
[`TBY-STO-006`](../../reference/errors/#tby-sto-006), qui est un refus
distinct d'une requête malformée — rien n'allait mal dans la requête ; on a
demandé à l'exploitant de taper un mot et il ne l'a pas fait.

**Ce qui part :** l'arborescence de contenu et les deux registres de
contenu. **Ce qui reste :** l'historique d'opération, les journaux de tâches
et le marqueur de format du store — une piste qu'une action destructrice
efface n'est pas une piste.

La remise à zéro est aussi disponible en `POST /api/v1/store/reset`. Il n'y
a pas de `tobby store reset` en ligne de commande.

<!-- TODO: screenshot: l'écran d'administration du store — ce que le store détient, et la confirmation tapée qu'exige la remise à zéro -->

## Deux choses qui ne sont pas encore là

**La cohérence d'horloge.** Les comparaisons de fraîcheur supposent des
horloges plausibles. Détecter une horloge implausible au démarrage et à
l'ouverture d'un support — avertir et consigner à l'audit, jamais corriger
en silence — est prévu au jalon 7 (R-32). D'ici là, une destination dont
l'horloge est fausse peut refuser un support frais ou accepter un support
périmé, et la piste d'audit enregistrera fidèlement la mauvaise heure.

**Mettre à jour Tobby lui-même dans la zone.** Au jalon 6, chaque release
est publiée en artefacts OCI prêts à référencer dans une recipe, si bien que
les mises à jour de Tobby traversent le flux miroir standard comme
n'importe quel contenu, signées par la chaîne de qualification de votre
site. Il n'y a pas d'auto-update, jamais (R-25). Aujourd'hui, mettre à jour
une instance isolée, c'est transporter le nouveau binaire comme vous
transportez le reste.

Les deux sont suivis sur la page [État du projet](../../discover/status/).
