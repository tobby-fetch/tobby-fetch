---
title: Sauvegarder et reconstruire une instance
description: Ce qui fait qu'une instance est cette instance, le seul répertoire à sauvegarder en plus du store, le contrôle que Tobby lui applique au démarrage, et une procédure de reconstruction testée de bout en bout.
sidebar:
  order: 9
---

Une instance, ce sont deux choses : son **contenu** et son **identité**. Le
contenu, c'est le store, et tout ce qu'il contient peut être récupéré de
nouveau. L'identité, c'est ce qu'aucune synchronisation ne recrée — les
comptes, les jetons, le certificat que les clients ont épinglé, le registre
qui empêche d'importer une seconde fois le support du mois dernier. Tobby garde
cette identité à un seul endroit, le **répertoire d'état** (`state.root`), dont
le layout est documenté, versionné et contrôlé à chaque démarrage. Cette page
dit quoi sauvegarder, ce que le démarrage refuse, et comment reconstruire.

## Ce que contient le répertoire d'état

Chaque entrée est soit **identité** — une reconstruction en a besoin, sauvegardez-la —
soit **jetable** — un cache ou un spool, reconstruit à la demande, inutile à
emporter. Rien d'autre n'y est écrit.

| Entrée | Classe | Secrète | Ce qu'elle contient | Si elle est perdue |
|---|---|---|---|---|
| `layout.json` | identité | non | La version de layout du répertoire. | Recréée au démarrage suivant. |
| `accounts.yaml` | identité | **oui** | Les comptes locaux avec leurs empreintes de mot de passe, et les jetons statiques sous forme d'empreintes. | Tous les comptes et jetons disparaissent ; l'instance refuse de démarrer sans aucun compte. |
| `tls/` | identité | **oui** | Le certificat auto-signé généré et sa clé privée, ou une paire adoptée. | Un nouveau certificat est généré et les clients voient une **nouvelle empreinte**. |
| `schedule.json` | identité | non | L'intervalle de rafraîchissement fixé à chaud par un administrateur. | L'intervalle configuré s'applique de nouveau. |
| `media-imports.json` | identité | non | Par zone, l'heure de résolution et l'identifiant du dernier support importé. | La garde de fraîcheur oublie le dernier import : un support plus ancien est de nouveau accepté. |
| `trust-cache/` | jetable | non | Les clés de confiance récupérées par des URL HTTPS à la lecture de la configuration. | Récupérées de nouveau. |
| `partials/` | jetable | non | Les téléchargements partiels reprenables. | Un blob repart de zéro. |
| `scanner/` | jetable | non | Le répertoire de travail privé du scanner de vulnérabilités. | Reconstruit à partir de la livraison. |
| `serving.json` | jetable | non | Marque l'instance qui sert le store, pour la ligne de commande locale. | Recréé au démarrage suivant. |

Le répertoire d'état vit hors du store, jamais dedans (NFR-020) : le store
quitte le site sur un support, l'état non.

## Quoi sauvegarder

Quatre choses font une instance. Ensemble, c'est toute la sauvegarde :

1. **Le répertoire d'état**, ses entrées d'identité (les jetables peuvent être
   copiées ou ignorées).
2. **Le fichier de configuration** (et l'environnement `TOBBY_*` que votre
   déploiement injecte, le cas échéant).
3. **Les fichiers secrets que la configuration référence** :
   `registries.credentialsFile`, `server.tls.keyFile` et son certificat quand
   vous fournissez votre propre paire, `auth.oidc.clientSecretFile`,
   `auth.saml.keyFile`. Ils sont hors du store et hors du répertoire d'état par
   construction.
4. **Le store**, ou de quoi le reconstruire : sa propre copie, ou une
   resynchronisation (voir plus bas).

`tobby state inspect` liste le répertoire avec la classe, la taille et les
permissions de chaque entrée — des noms et des tailles, jamais des contenus —
et se lance sans risque à côté d'une instance en marche :

```sh
tobby state inspect --config /etc/tobby/config.yaml
tobby state inspect --state-root /var/lib/tobby/state --output json
```

Le même inventaire est sur l'écran d'administration **État** (`/admin/state`) et
à `GET /api/v1/state`, tous deux pour les administrateurs, et il est consigné
dans le [bundle de diagnostic](../diagnostics/). Sur Kubernetes, le chart de
référence garde l'état sur son propre volume (`helm.sh/resource-policy: keep`) :
faites un snapshot de ce volume.

## Les secrets au repos

Tobby protège ses entrées secrètes et dit jusqu'où :

- Les entrées secrètes sont des fichiers créés en **mode 0600** (une liste
  d'accès réservée au propriétaire sous Windows). Les identifiants que Tobby
  vérifie sont stockés **hachés**, jamais en clair : mots de passe en argon2id,
  jetons sous forme d'empreintes. Les exceptions sont la clé privée du
  certificat du listener, qui doit être utilisable, et les fichiers vers
  lesquels vous pointez la configuration.
- Tobby ne **chiffre pas** ces entrées. Le chiffrement au repos relève du
  volume — disque chiffré, volume persistant chiffré, BitLocker. Une sauvegarde
  du répertoire d'état emporte les mêmes empreintes et la même clé privée :
  conservez-la avec la protection que vous donnez au volume.
- Un bundle de sauvegarde chiffré produit par Tobby lui-même est hors de la
  version 1.

## Ce que le démarrage contrôle

Au démarrage, avant que rien n'y lise, Tobby contrôle le répertoire d'état :

| Condition | Résultat |
|---|---|
| La version de layout est inconnue de ce build, ou le fichier de layout est illisible | L'instance **refuse de démarrer** : [`TBY-STA-001`](../../reference/errors/#tby-sta-001). |
| Une entrée secrète est lisible par **tous les utilisateurs de l'hôte** | L'instance **refuse de démarrer**, en nommant le fichier : [`TBY-STA-002`](../../reference/errors/#tby-sta-002). |
| Une entrée d'identité dont cette configuration dépend manque dans un répertoire qui a déjà servi | L'instance démarre et **le dit** : un enregistrement d'avertissement par entrée, [`TBY-STA-003`](../../reference/errors/#tby-sta-003). |
| Une entrée jetable manque | Recréée silencieusement. |

« Lisible par tous » veut dire toute permission pour *other* sous Unix, et une
liste d'accès accordant Everyone, BUILTIN\Users ou Authenticated Users sous
Windows. **L'accès du groupe est toléré** : un `fsGroup` Kubernetes, comme dans
le chart de référence, attribue les fichiers du volume au groupe du pod, et le
refuser reviendrait à refuser tout déploiement réel. Le contrôle porte sur les
fichiers de clé dans `tls/`, pas sur le certificat à côté, qui est public.

Un répertoire neuf est **adopté** : Tobby enregistre sa version de layout et
démarre. Un répertoire antérieur à ce contrôle est adopté de la même façon,
après le contrôle des permissions.

## Reconstruire une instance

Sur un hôte où l'instance n'a jamais tourné (ou après une perte) :

1. Installez la **même version de Tobby** — ou une plus récente : le layout est
   versionné, des entrées peuvent être ajoutées mais jamais renommées ni
   détournées, et une version qui ne connaît pas le layout refuse plutôt que de
   deviner.
2. Restaurez le **répertoire d'état** avec ses permissions : `tar -xpf`,
   `rsync -a`, `cp -a`, un snapshot de volume ; sous Windows `robocopy /COPYALL`
   ou un snapshot. Une restauration qui a perdu les permissions n'a rien de
   mystérieux : le démarrage refuse et nomme le fichier. Corrigez
   (`chmod o-rwx` sous Unix ; retirez Everyone et Users de la liste d'accès sous
   Windows), vérifiez qui aurait pu lire le fichier entre-temps, puis
   redémarrez.
3. Restaurez la **configuration** et les **fichiers secrets** qu'elle référence,
   aux chemins qu'elle nomme.
4. Remettez le **store** : restaurez sa propre copie, ou démarrez à vide et
   resynchronisez. Un store miroir est un support ; un store passthrough est un
   cache. Rien dans l'état ne dépend du contenu du store.
5. Démarrez l'instance. Lisez les enregistrements de démarrage : un
   avertissement `TBY-STA-003` nomme une entrée d'identité que la sauvegarde ne
   portait pas.

Ce que vous retrouvez, et qui est testé : les comptes d'origine se connectent
avec leurs mots de passe ; un jeton émis avant la perte s'authentifie encore ;
le listener présente la **même empreinte de certificat** ; le registre d'imports
refuse le même support périmé ; la surcharge d'intervalle tient. La suite de
bout en bout du dépôt fait exactement cela — arrêter une instance, copier l'état,
la configuration et le store vers un endroit neuf, y démarrer, comparer — et le
scénario crucible `b-rebuild` le rejoue entre deux nœuds sans volume partagé.

Si vous fournissez votre propre certificat (`server.tls.certFile`, `keyFile`),
l'empreinte dépend de ces fichiers, pas du répertoire d'état : ils sont le point
3 ci-dessus.

## Limites

- Le contrôle porte sur les **entrées secrètes**. Il ne regarde ni les
  permissions du répertoire qui contient le répertoire d'état, ni le store, ni
  les fichiers que vous référencez depuis la configuration.
- Les fichiers non documentés trouvés dans le répertoire d'état sont listés par
  `tobby state inspect` et laissés tels quels.
- Les versions plus anciennes ne lisent pas un répertoire écrit avec un layout
  plus récent : restaurez la version qui l'a écrit, ou une sauvegarde faite avec
  la version que vous exécutez.
