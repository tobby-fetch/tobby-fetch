---
title: Renouveler clés et secrets
description: Comment renouveler les clés de signature des recipes, les trust roots des ingrédients, les mots de passe, les tokens d'API, la paire TLS et les identifiants des fournisseurs d'identité et des registries, ce que chaque changement demande pour prendre effet, et ce que voient les utilisateurs pendant ce temps.
sidebar:
  order: 11
---

Une instance dépend de deux sortes de matériel : des **clés publiques**
contre lesquelles elle vérifie (les trust roots) et des **secrets** qu'elle
détient (identifiants, clés privées, hashes de mots de passe). Chacun se
renouvelle différemment, et chacun prend effet à un moment différent. Cette
page donne les procédures et dit, pour chacune, quand le changement est lu, ce
qui casse s'il est fait dans le mauvais ordre, et comment voir qu'il a
fonctionné. L'inventaire de ce qui est secret, et de l'endroit où cela réside,
est sur [Secrets](../secrets/) ; la façon dont trust roots et périmètres
décident est sur [Signatures, trust roots et liste d'autorisation](../content-trust/).

:::tip[En bref]
- Une clé de signature de recipe se renouvelle par recouvrement. Ajoutez la nouvelle clé publique comme second trust root sur chaque instance qui vérifie, en commençant par la zone isolée. Re-signez ensuite sur place les recipes publiées avec la nouvelle clé, et laissez les synchronisations faire descendre la signature. Retirez l'ancien root en dernier, quand une vérification du store sous la nouvelle configuration est propre. [Procédure](#renouveler-une-clé-de-signature-de-recipe)
- Re-signer ne demande aucune nouvelle version. Une signature ajoutée en amont à un digest qu'un store détient déjà atteint ce store à sa synchronisation suivante, et le registry de zone à son push suivant. [Comment](#un-store-suit-les-signatures-de-sa-source)
- Trust roots, identifiants de registry, matériel OIDC et SAML et mot de passe du proxy sont lus au démarrage. Un changement demande un redémarrage, et un redémarrage déconnecte toutes les sessions du navigateur. La paire TLS est relue à la poignée de main suivante. [Tableau](#quand-un-changement-prend-effet)
- Changez mots de passe et tokens sur l'instance en marche (écran ou API). `tobby user add` et `tobby user passwd` refusent de s'exécuter tant qu'une instance tient le répertoire d'état. [Comptes](#mots-de-passe-des-comptes)
:::

## Quand un changement prend effet

Rien ne surveille le fichier de configuration, et il n'existe aucun signal de
rechargement : l'instance ne traite que `SIGTERM` et `SIGINT`, comme un arrêt
propre. Une valeur que l'instance lit au démarrage change quand l'instance
redémarre, pas avant. Les outils en ligne de commande qui ouvrent eux-mêmes un
store (`tobby media verify`, `tobby media import`, `tobby store verify` sans
`--instance`, `tobby sync --dry-run`) relisent le fichier de configuration à
chaque exécution.

| Matériel | Où il réside | Pris en compte | Ce que voient les utilisateurs |
| --- | --- | --- | --- |
| Trust roots des recipes et des ingrédients | `trust.roots`, `trust.scopes` (fichier de configuration) | au redémarrage de l'instance ; à chaque exécution des commandes ci-dessus | une recipe qu'aucun root applicable ne vérifie est refusée, [`TBY-SIG-001`](../../reference/errors/#tby-sig-001) |
| Mot de passe d'un compte | `accounts.yaml` dans `state.root` | immédiatement, par l'écran ou l'API | les autres sessions du compte sont fermées |
| Token d'API | `accounts.yaml` dans `state.root` | immédiatement | un token révoqué est refusé comme un faux ([`TBY-AUTH-002`](../../reference/errors/#tby-auth-002)), un token expiré avec [`TBY-AUTH-013`](../../reference/errors/#tby-auth-013) |
| Paire TLS du serveur | `server.tls.certFile`, `server.tls.keyFile` | la poignée de main TLS suivante, sans redémarrage | le nouveau certificat et sa nouvelle empreinte |
| Identifiants de registry | `registries.credentialsFile` | redémarrage | rien, si l'ancien identifiant fonctionne encore jusque-là |
| Secret client OIDC | `auth.oidc.clientSecretFile` | redémarrage | toutes les sessions déconnectées par le redémarrage |
| Paire du fournisseur de service SAML | `auth.saml.certFile`, `auth.saml.keyFile` | redémarrage | le nouveau certificat dans le descripteur de `/auth/saml/metadata` |
| Mot de passe du proxy | fichier de configuration ou `TOBBY_NETWORK_PROXY_PASSWORD` | redémarrage | rien |
| Sessions de l'interface et jetons anti-falsification | mémoire uniquement | rien à renouveler | un redémarrage déconnecte tout le monde ([`TBY-AUTH-005`](../../reference/errors/#tby-auth-005)) |

Il n'y a **aucune clé de signature de session** à renouveler. Une session est
un identifiant aléatoire tenu dans la mémoire de l'instance, et son jeton
anti-falsification une autre valeur aléatoire tirée pour cette session. Rien
des sessions n'est écrit dans le répertoire d'état ; rien ne survit donc à un
redémarrage : chacun se reconnecte. Planifiez les redémarrages en conséquence.
Sur Kubernetes ou sur une VM, un redémarrage interrompt aussi brièvement le
registry embarqué ; voir [Mettre à jour et revenir en arrière](../../passthrough/upgrade-rollback/).

## Clés de signature des recipes

Tobby ne signe jamais et ne détient aucune clé privée (ADR-0007). Renouveler
une clé de signature, ce sont deux actions distinctes : l'organisation signe
avec une nouvelle clé privée, et chaque instance qui vérifie apprend la clé
publique correspondante comme trust root. Les trust roots forment un
ensemble : les deux clés peuvent être de confiance en même temps. Ce
recouvrement est ce qui rend le renouvellement possible sans assouplir la
vérification à aucun moment.

### Quand une signature est vérifiée

Un renouvellement n'est sûr que si vous savez quelle copie d'une signature
chaque contrôle lit, et contre quels roots.

| Moment | Copie de la signature lue | Roots utilisés |
| --- | --- | --- |
| Synchronisation | celle du cookbook | ceux de l'instance, chargés au démarrage |
| Plan (`tobby sync --dry-run`) | celle du cookbook | le fichier de configuration passé à la commande |
| Avant chaque push vers `destination.registry` | **celle du store** | ceux de l'instance |
| Vérification du store (`tobby store verify`, l'écran Store) | **celle du store** | la configuration passée à la commande, ou celle de l'instance avec `--instance` |
| Vérification et import d'un support, côté destination | **celle du support** | ceux de la destination ; tout ce que porte le support est ignoré |

Deux contrôles n'ont pas lieu :

- Le contenu déjà présent dans un store passthrough n'est **pas** revérifié
  quand il est servi par le registry embarqué (`/v2/`, `/files/`). Retirer un
  trust root ne le retire pas.
- Le contenu déjà poussé dans un registry de zone y reste. Tobby ne revient
  pas dans un registry retirer ce qu'il a promu.

Le verdict enregistré d'une recipe (`verified` dans `GET /api/v1/recipes`,
l'écran Recipes) est celui pris à sa synchronisation. Il n'est pas réévalué
quand les roots changent ; c'est une vérification du store qui répond à cette
question.

### Un store suit les signatures de sa source

Chaque synchronisation aligne la copie que le store détient des signatures
d'une recipe sur celle de la source, dans les deux formats cosign (attaché et
bundle), y compris pour une version de recipe que le store détient déjà au
même digest. Une signature ajoutée en amont à ce digest atteint le store à la
synchronisation suivante, et le registry de zone au push suivant. Les
ingrédients suivent la même règle. Re-signer ne demande donc aucune nouvelle
version : signez avec la nouvelle clé le digest déjà publié, et les
synchronisations font descendre la signature.

Le store reflète sa source. Une signature modifiée localement dans le store
est remplacée par celle de la source à la synchronisation suivante, et la
vérification avant chaque push lit toujours la copie du store, contre les
roots de l'instance.

Une exception : quand le registry de destination détient déjà la même recipe
sous un **digest de manifeste différent** (le même document, publié là par un
autre outil), Tobby n'y pousse aucune signature. Ses signatures nomment un
manifeste que ce registry ne détient pas : ce registry garde les siennes.

### Renouveler une clé de signature de recipe

L'ordre compte : **ajoutez le nouveau root de bas en haut** (la zone isolée
d'abord, les zones qui lisent le cookbook en dernier), et **retirez l'ancien
de haut en bas**.

1. **Générez la nouvelle paire de clés hors de Tobby** et gardez la clé privée
   là où l'organisation garde ses clés de signature. Calculez l'empreinte de
   la clé publique. C'est la valeur qu'emploie chaque rapport de Tobby
   (SHA-256 de l'encodage DER de la clé) :

   ```sh
   openssl pkey -pubin -in release-2026.pub -outform DER | openssl dgst -sha256 -r
   ```

2. **Ajoutez le nouveau root à côté de l'ancien** sur chaque instance qui
   vérifie : chaque instance passthrough, chaque source miroir et chaque
   destination d'un transfert physique.

   ```yaml
   trust:
     roots:
       - name: release-2025
         keyFile: /etc/tobby/keys/release-2025.pub
       - name: release-2026
         keyFile: /etc/tobby/keys/release-2026.pub
   ```

   Redémarrez l'instance. Un root illisible ou mal formé arrête le démarrage,
   en nommant le root :

   ```
   tobby: trust root release-2026: open ./keys/missing.pub: no such file or directory
   ```

   Vérifiez ce avec quoi l'instance tourne désormais :
   `tobby config dump --config <fichier>` liste les roots par nom et par
   source (un chemin, une URL ou la clé en ligne).

3. **Si l'ancienne clé est listée par un périmètre de confiance**, listez-y
   aussi la nouvelle. Un root listé par un périmètre est réservé à ce
   périmètre : un nouveau root ajouté seulement à `trust.roots` appartient à
   l'ensemble par défaut et ne vérifie rien à l'intérieur du périmètre
   ([confiance du contenu](../content-trust/#périmètres-dassouplissement--et-leur-visibilité)).
   La [clé d'urgence](../../recipes/emergency-path/) est le cas habituel :

   ```yaml
   scopes:
     - name: emergency
       repositories: ["registry.example.com/emergency/**"]
       roots: [org-emergency-2025, org-emergency-2026]
   ```

4. **Re-signez les recipes publiées avec la nouvelle clé**, sur place, hors
   ligne comme toujours (`--use-signing-config=false --tlog-upload=false`,
   voir [Signer avec cosign](../../recipes/write-and-publish/#signer-avec-cosign)) :
   `cosign sign` sur le digest déjà publié, à côté de l'ancienne signature.
   Rien n'est republié et les Retrievers ne changent pas. Signez désormais
   les nouvelles versions avec la nouvelle clé.

5. **Synchronisez partout**, de haut en bas. Chaque instance copie la
   nouvelle signature dans son store à sa synchronisation suivante (ou
   `tobby sync --wait`) et la pousse dans son registry de zone, où la zone
   du dessous la récupère à son tour. Pour une zone isolée, synchronisez le
   miroir sur un nouveau support, puis vérifiez-le et importez-le sur la
   destination : la destination lit la signature sur le support.

6. **Répétez le retrait sur chaque store** avant de le faire. Écrivez la
   configuration sans l'ancien root dans un fichier séparé et vérifiez le
   store contre elle. La commande ne fait que lire, et nomme la clé qui a
   vérifié chaque recipe :

   ```
   $ tobby store verify --config proposed.yaml
   verdict: defects — 14 blobs (4134641 bytes), 7 manifests, 2 recipes checked
     recipe alpine@1.0.0  complete, signature refused
     recipe alpine@1.0.1  complete, signature verified (key sha256:f3212501fb7a…)
     DEFECT  TBY-SIG-001 alpine@1.0.0
   ```

   Ici, `alpine@1.0.0` n'a pas encore été re-signée, ou ce store n'a pas
   synchronisé depuis : reprenez les étapes 4 et 5 pour elle.

   Exécutée dans son propre processus, cette forme refuse un store qu'une
   instance sert : lancez-la pendant un court arrêt, ou sur un store miroir
   avant son départ. Avec `--instance`, la vérification s'exécute sur
   l'instance, contre les roots avec lesquels elle tourne, pas ceux proposés.
   Ne comptez pas sur `tobby sync --dry-run` pour ce contrôle : il lit les
   signatures du cookbook, pas celles du store.

7. **Retirez l'ancien root**, de haut en bas : les instances qui lisent le
   cookbook d'abord, les destinations de transferts physiques en dernier,
   quand plus aucun support en circulation ne porte de recipe que seule
   l'ancienne clé vérifie. Redémarrez chaque instance.

Une clé peut aussi être renouvelée à l'intérieur d'un seul root : un
`keyFile`, un `keyURL` ou une `key` peut contenir plusieurs clés publiques PEM,
toutes de confiance sous le nom de ce root. Préférez une clé par root, avec un
nom daté. Les rapports nomment l'empreinte dans les deux cas, mais un
périmètre réserve un root entier, et retirer une clé d'un root partagé est
facile à rater.

### Retirer un root trop tôt

| Où | Ce qui se passe |
| --- | --- |
| Synchronisation | une recipe dont seule la clé retirée vérifie la signature du cookbook est refusée, `TBY-SIG-001` nommant les empreintes essayées. Le passage est incomplet : aucun prune ne s'exécute, et rien de ce qu'il détient n'est supprimé. |
| Push vers le registry de zone | une recipe dont seule la clé retirée vérifie la copie du **store** est refusée avant le push, `TBY-SIG-001`. Le refus est compté dans `tobby_promotion_refusals_total{code="TBY-SIG-001"}`. |
| Plan | la recipe affiche `refused` dans la colonne `SIGNATURE` ; code de sortie `4`. |
| Vérification du store | `DEFECT TBY-SIG-001` sur la recipe ; code de sortie `4`. Le store n'est pas modifié. |
| Destination d'un support | la recipe est `BLOCKED`, le support est `partial`. Voir plus bas. |
| Déjà servi ou poussé | rien ne change : le contenu d'un store passthrough reste servi, le contenu du registry de zone y reste. |

Un périmètre `allowUnsigned` n'adoucit rien de tout cela. Il admet les recipes
qui ne portent **aucune** signature. Une recipe qui porte une signature
qu'aucun root applicable ne vérifie est refusée dans un tel périmètre
exactement comme en dehors.

Pour revenir en arrière, remettez l'ancien root et redémarrez. Les recipes
refusées se vérifient à nouveau à la synchronisation ou à la vérification
suivante ; rien n'a été supprimé.

### La zone isolée

La destination d'un transfert physique vérifie chaque recipe contre **ses
propres** trust roots ; une clé qui voyage sur le support est ignorée
([sécurité des supports](../../air-gap/media-security/)). La nouvelle clé
publique doit donc être configurée sur la destination, et la destination
redémarrée, **avant l'arrivée du premier support signé avec elle**. Un support
arrivé avant est refusé pour cette recipe. Rejoué, avec une destination qui ne
fait encore confiance qu'à `release-2025` :

```
$ tobby media verify --config destination.yaml
verdict: partial (47 checked files, 4141453 bytes)
  pushable  alpine@1.0.0 (29 files, 4134878 bytes)
  BLOCKED   alpine@1.0.1 — TBY-SIG-001 at 127.0.0.1:5001/cookbook/alpine
```

Le code de sortie est `4`. Deux conséquences découlent de la façon dont une
destination sert :

- **Une seule recipe invérifiable arrête tout service depuis le support.** Le
  registry et le serveur de fichiers propres à la destination ne servent un
  support que si sa vérification en sort entière. Un support `partial` ne sert
  rien, pour aucune recipe, avec
  [`TBY-MED-032`](../../reference/errors/#tby-med-032). Les recipes
  poussables peuvent toujours être importées dans le registry de zone, qui les
  sert ensuite. Le texte du refus suggère de recopier le support ; quand la
  cause est un trust root manquant, c'est l'ajout du root qui corrige.
- **Le redémarrage qui ajoute le root ferme la barrière de service.** Elle se
  referme à chaque redémarrage ([`TBY-MED-030`](../../reference/errors/#tby-med-030))
  jusqu'à ce que le support soit vérifié de nouveau : planifiez le redémarrage
  avec la vérification qui le suit, sur l'écran Média ou avec
  `POST /api/v1/media/verify`.

`tobby media verify` relit le fichier de configuration à chaque exécution et
n'écrit rien : une configuration de destination proposée peut donc être
confrontée au support avant de redémarrer l'instance avec elle. Voir
[un support non vérifié ne sert rien](../../air-gap/import-destination/#un-support-non-vérifié-ne-sert-rien).

### Roots récupérés depuis une URL

Un root `keyURL` est téléchargé au chargement de la configuration (au
démarrage, et à chaque exécution des commandes ci-dessus) et mis en cache dans
`state.root/trust-cache/<nom du root>.pub`. Si l'URL ne répond pas, l'instance
vérifie avec la copie en cache et journalise une ligne `WARN`,
`trust root key URL unreachable: verifying with the cached copy`, avec l'URL,
l'erreur, le chemin du cache et l'âge de la copie (`cache_age`). Retirer une
ancienne clé du fichier servi à l'URL n'a donc aucun effet sur une instance
qui ne joint pas l'URL à son prochain démarrage : surveillez cette ligne. Pour un renouvellement que vous devez voir se
produire, utilisez `keyFile` ou `key`, ou vérifiez la clé en usage par une
vérification du store.

### Où le changement se voit

- **Configuration** : `tobby config dump` liste les roots par nom et par
  source, pas par empreinte. Aucun écran ne liste les trust roots configurés,
  l'écran de politique (`/admin/policy`) non plus.
- **Par recipe** : `tobby store verify` et `tobby media verify` (et leurs
  formes `--output json`, `key_fingerprint` et `keyFingerprint`) nomment la
  clé qui a vérifié chaque recipe ; l'écran Média l'affiche à côté de chaque
  recipe. Un refus nomme chaque empreinte essayée.
- **Audit** : un changement de trust root est un changement de configuration,
  et aucun événement d'audit ne l'enregistre comme tel. Gardez le fichier de
  configuration sous contrôle des changements. Le redémarrage qui l'a appliqué
  est un événement `instance.start`, et chaque vérification du store un
  événement `store.verify` ([journal d'audit](../audit-log/)).

## Signatures des ingrédients

`trust.requireIngredientSignatures` existe, et est désactivé par défaut. Tant
qu'il l'est, la signature propre d'un ingrédient est copiée, vérifiée pour
information et consignée ; elle ne bloque jamais, si bien qu'un renouvellement
de clé par l'éditeur de l'ingrédient ne change que ce qui est consigné
([confiance du contenu](../content-trust/#signatures-des-ingrédients--consignées-exigées-seulement-sur-demande)).

Quand il est activé, chaque ingrédient doit porter une signature qu'un trust
root applicable à l'ingrédient vérifie (périmètres appliqués au dépôt
canonique de l'ingrédient lui-même), faute de quoi sa **recipe est bloquée
entière** avec `TBY-SIG-001` (FR-033). Les clés en jeu sont en général celles
de l'éditeur amont : le renouvellement est annoncé par l'éditeur, pas décidé
par vous.

1. Ajoutez la nouvelle clé publique de l'éditeur comme root, à côté de
   l'ancienne, sur chaque instance qui vérifie, et dans le même périmètre que
   l'ancienne si un périmètre la liste. Redémarrez.
2. Synchronisez : un éditeur qui re-signe un digest existant avec sa nouvelle
   clé ne demande aucune nouvelle version de recipe, le store et le registry
   de zone reçoivent la nouvelle signature comme pour les recipes
   ([plus haut](#un-store-suit-les-signatures-de-sa-source)).
3. Répétez avec `tobby store verify --config proposed.yaml`, puis retirez
   l'ancien root.

Un ingrédient situé dans un périmètre `allowUnsigned` est dispensé de
l'exigence.

## Mots de passe des comptes

Les mots de passe sont stockés sous forme de hashes argon2id dans
`accounts.yaml`, dans le répertoire d'état (FR-073). L'instance en marche
garde les comptes en mémoire et réécrit le fichier entier à chaque changement
qu'elle fait, y compris l'heure de dernière connexion de chaque connexion par
mot de passe.

- **Votre propre mot de passe** : l'écran du compte (`/account`) ou
  `POST /api/v1/account/password`, avec le mot de passe actuel (FR-077). Le
  changement prend effet immédiatement, et les autres sessions du compte sont
  fermées. La session du navigateur qui a fait le changement reste ouverte.
  L'événement est `account.password_change`.
- **Le mot de passe d'un autre compte** : ni l'écran ni l'API ne le
  définissent. Sur une instance en marche, un administrateur peut supprimer le
  compte puis le recréer avec un nouveau mot de passe sur `/admin/accounts`.
  Le dernier administrateur ne peut pas être supprimé
  ([`TBY-AUTH-011`](../../reference/errors/#tby-auth-011)). Les tokens ne sont
  pas rattachés aux comptes et ne sont pas touchés.
- **Sur l'hôte** : `tobby user passwd <nom>` et `tobby user add` écrivent
  directement `accounts.yaml`, qu'une instance en marche écraserait avec sa
  propre copie. Ils refusent donc avec
  [`TBY-CFG-001`](../../reference/errors/#tby-cfg-001), sans rien écrire,
  tant qu'une instance tient le répertoire d'état. L'instance tient un verrou
  du système d'exploitation sur `state.root/serving.json` pendant toute sa
  vie, quelle que soit sa configuration TLS, et le verrou disparaît avec le
  processus s'il meurt. Le message nomme l'alternative : pour `user add`,
  l'écran des comptes (`/admin/accounts`) ou `POST /api/v1/accounts` ; pour
  `user passwd`, arrêter l'instance et lancer la commande, ou laisser le
  titulaire du compte changer son mot de passe sur `/account`.

## Tokens d'API

Un token porte son propre nom et son propre rôle, n'est montré qu'une fois à
la création et est stocké sous forme de hash (FR-072). Renouvelez par
recouvrement :

1. Créez le remplaçant sur `/admin/accounts` ou avec `POST /api/v1/tokens`.
   Le nom d'un token actif ne peut pas être réutilisé (`409`,
   [`TBY-AUTH-009`](../../reference/errors/#tby-auth-009)) : donnez-lui un
   nouveau nom (`ci-2026`). Une expiration passée est refusée (`422`,
   [`TBY-VAL-001`](../../reference/errors/#tby-val-001) sur `expires`). Une valeur `expires`, ou `auth.tokens.defaultLifetime`,
   fait que le renouvellement suivant arrive de lui-même.
2. Faites passer les clients au nouveau secret.
3. Révoquez l'ancien token (`POST /api/v1/tokens/{name}/revoke`). La
   révocation est immédiate et définitive ; le nom redevient libre.

Un client qui présente encore le token révoqué est refusé avec
[`TBY-AUTH-002`](../../reference/errors/#tby-auth-002), et chaque refus est un
échec `auth.authenticate` dans le journal d'audit, sous le nom du token. Un
token expiré est refusé avec
[`TBY-AUTH-013`](../../reference/errors/#tby-auth-013) et un événement
`token.expire`. Les deux comptent comme des tentatives échouées pour l'adresse
réseau du client : au-delà de dix, cette adresse est freinée
([`TBY-AUTH-012`](../../reference/errors/#tby-auth-012), nouvel essai après 30
secondes), **y compris pour les clients valides derrière la même adresse**.
Retrouvez le client oublié dans le journal d'audit avant qu'il ne bloque ses
voisins.

## Certificat TLS du serveur

L'écouteur relit `server.tls.certFile` et `server.tls.keyFile` dès que leur
date de modification ou leur taille change, à la poignée de main suivante,
sans redémarrage (FR-082). Remplacer les fichiers suffit. Rejoué : l'empreinte
servie est passée de celle de l'ancienne paire à celle de la nouvelle à la
poignée de main qui a suivi la copie. Une paire saisie à moitié écrite n'est
pas chargée ; le certificat précédent reste servi jusqu'à ce que la paire soit
cohérente. Un remplacement de fichiers ne laisse aucun événement d'audit ; un
remplacement fait sur l'écran Réseau ou avec `PUT /api/v1/network/certificate`
est un événement `config.server_certificate`. Surveillez
`tobby_server_certificate_expiry_timestamp_seconds` pour renouveler avant la
fin de validité ([réseau](../../passthrough/network/)).

:::note[Une instance démarrée en auto-signé]
Sur une instance qui tourne avec le certificat de repli généré, l'écran Réseau
écrit la nouvelle paire dans `state.root/tls/server.crt` et `server.key` et
l'adopte aussitôt. Au démarrage suivant, l'instance sert de nouveau cette
paire tant qu'elle se charge et n'a pas expiré, et la relit à chaque
changement comme une paire configurée. Une paire expirée ou illisible est
écartée : l'instance revient au certificat généré, et le journal de démarrage
dit pourquoi. La voie déclarative reste disponible, et c'est celle à préférer
pour une paire que vous gérez de l'extérieur : faites pointer
`server.tls.certFile` et `server.tls.keyFile` vers les fichiers. La forme API
du remplacement (`PUT /api/v1/network/certificate`) refuse une instance qui
tourne sur le repli.
:::

## Identifiants de registry

`registries.credentialsFile` est lu une fois, au démarrage, pour les registries
sources, la destination et la publication de recipes. Une instance en marche
garde les identifiants qu'elle a chargés. Pour en renouveler un :

1. Créez le nouvel identifiant sur le registry pendant que l'ancien fonctionne
   encore.
2. Écrivez-le dans le fichier.
3. Redémarrez l'instance.
4. Révoquez l'ancien identifiant sur le registry.

Si le registry révoque l'ancien identifiant avant le redémarrage, les
synchronisations et les push échouent jusqu'au redémarrage de l'instance. Les
commandes `tobby media import` et `tobby recipe push` relisent le fichier à
chaque exécution.

## Fournisseurs d'identité

Le secret client OIDC (`auth.oidc.clientSecretFile`) et la paire du
fournisseur de service SAML (`auth.saml.certFile`, `auth.saml.keyFile`) sont
lus au démarrage de l'instance (FR-070, FR-071). Chaque renouvellement demande
donc un redémarrage, et le redémarrage déconnecte tout le monde. Les comptes
locaux et les tokens d'API continuent de fonctionner pendant ce temps
([identité d'entreprise](../enterprise-identity/)).

- **Secret client OIDC** : si le fournisseur permet deux secrets pour un même
  client, ajoutez-y le nouveau, écrivez-le dans le fichier, redémarrez, puis
  retirez l'ancien chez le fournisseur. S'il n'en permet qu'un, la connexion
  fédérée échoue avec [`TBY-AUTH-014`](../../reference/errors/#tby-auth-014)
  entre le changement chez le fournisseur et le redémarrage. Les clés de
  signature du fournisseur lui-même ne demandent rien : elles sont lues dans
  son jeu de clés publié, récupéré de nouveau quand un token nomme une clé
  inconnue.
- **Paire du fournisseur de service SAML** : Tobby détient une seule paire. Il
  signe les demandes d'authentification avec cette clé et publie le certificat
  dans son descripteur (`/auth/saml/metadata`). Enregistrez le nouveau
  certificat chez le fournisseur (à côté de l'ancien, si le fournisseur en
  accepte deux), installez la nouvelle paire, redémarrez, puis retirez l'ancien
  certificat chez le fournisseur.
- **Certificat de signature SAML du fournisseur** : `idpCertificateFile`
  épingle exactement un certificat, le premier du fichier. Quand il est
  renseigné, le renouvellement chez le fournisseur est une bascule : les
  connexions échouent avec [`TBY-AUTH-015`](../../reference/errors/#tby-auth-015)
  dès que le fournisseur signe avec la nouvelle clé, jusqu'à ce que le fichier
  soit remplacé et l'instance redémarrée. Sans `idpCertificateFile`, ce sont
  les certificats de signature du `idpMetadataFile` épinglé qui s'appliquent :
  un fichier de métadonnées qui liste l'ancien et le nouveau certificat permet
  un recouvrement.

## Mot de passe du proxy

`network.proxy.password` provient du fichier de configuration ou de
`TOBBY_NETWORK_PROXY_PASSWORD`, et est lu au démarrage. Changez la valeur et
redémarrez. Tous les chemins sortants passent par un seul transport : il n'y a
pas de seconde copie à mettre à jour.

## Limites

- **Retirer un trust root ne retire rien de ce qui a déjà été livré.** Cela
  arrête les synchronisations, les push et les imports de supports à venir ; le
  contenu déjà servi par une instance passthrough ou poussé dans un registry de
  zone y reste.
- **Un root `keyURL` retombe** sur sa clé en cache quand son URL ne répond
  pas, avec un `WARN` au journal, et continue de vérifier avec elle.
- **La garde des clés appartient à l'organisation.** Tobby n'a ni liste de
  révocation ni expiration de clé. Une clé de signature compromise cesse d'être
  de confiance seulement quand son root est retiré de chaque instance, et
  seulement pour ce qui est vérifié ensuite.
- **La plupart des changements demandent un redémarrage**, et un redémarrage
  déconnecte toutes les sessions du navigateur et ferme la barrière de service
  d'une destination jusqu'à ce que le support soit vérifié de nouveau.
