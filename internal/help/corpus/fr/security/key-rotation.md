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
- Une clé de signature de recipe se renouvelle par recouvrement. Ajoutez la nouvelle clé publique comme second trust root sur chaque instance qui vérifie, en commençant par la zone isolée. Signez ensuite avec la nouvelle clé. Retirez l'ancien root en dernier, quand une vérification du store sous la nouvelle configuration est propre. [Procédure](#renouveler-une-clé-de-signature-de-recipe)
- Signez de **nouvelles versions de recipes** avec la nouvelle clé. Une signature ajoutée après coup à une version qu'un store détient déjà n'atteint jamais ce store : l'ancienne clé continue d'y décider de cette version. [Pourquoi](#un-store-garde-les-signatures-de-sa-première-copie)
- Trust roots, identifiants de registry, matériel OIDC et SAML et mot de passe du proxy sont lus au démarrage. Un changement demande un redémarrage, et un redémarrage déconnecte toutes les sessions du navigateur. La paire TLS est relue à la poignée de main suivante. [Tableau](#quand-un-changement-prend-effet)
- Changez mots de passe et tokens sur l'instance en marche (écran ou API). `tobby user add` et `tobby user passwd` ne sont sûrs que lorsque l'instance est arrêtée. [Comptes](#mots-de-passe-des-comptes)
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

### Un store garde les signatures de sa première copie

Une synchronisation copie les signatures d'une recipe dans le store quand elle
y range cette version de recipe pour la première fois : les deux formats
cosign, attaché et bundle. Lors d'une synchronisation suivante, une version
que le store détient déjà au même digest est sautée comme à jour, et ses
signatures ne sont **pas recopiées**. Il en va de même pour un ingrédient que
le store détient déjà.

La conséquence pour un renouvellement : signer une seconde fois, avec la
nouvelle clé, une version de recipe **déjà publiée** n'ajoute cette signature
qu'au cookbook. Chaque store qui détenait déjà la version ne garde que
l'ancienne signature, comme les supports et le registry de zone remplis à
partir de lui. Une fois l'ancien root retiré, cette version est refusée au push
suivant, à la vérification de store suivante et côté destination, alors que le
plan la dit toujours vérifiée, parce que le plan lit le cookbook.

Ceci a été rejoué en local : la version `1.0.0` signée avec `release-2025` et
promue, puis signée une seconde fois avec `release-2026`, puis l'instance
redémarrée avec `release-2026` comme seul root.

```
$ tobby sync --wait
task tsk_dac2a194b92b on http://127.0.0.1:18090
  type:      sync
  reference: ./retriever.yaml
  status:    failed
  duration:  1s
  items:     3 (0 done, 2 skipped, 1 failed)
  failed   push/alpine@1.0.0/recipe — TBY-SIG-001
  principal: TBY-SIG-001 (verification)
TBY-SIG-001 — The recipe signature could not be verified.
  cause:  No trust root of the default set validates the signature of alpine@1.0.0 (keys tried: sha256:f3212501fb7a…). …
```

Le code de sortie est `4`. Avec la même configuration, `tobby sync --dry-run`
indique `alpine 1.0.0 … verified` et sort en `0`.

**Signez de nouvelles versions avec la nouvelle clé.** Un nouveau
`metadata.version` est un nouveau digest de recipe, rangé pour la première
fois, avec les signatures présentes dans le cookbook à ce moment. Les versions
publiées sont immuables : c'est aussi la seule façon de changer ce qu'une
version épingle. Une recipe qui n'a encore été synchronisée nulle part peut
simplement être signée avec les deux clés.

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

4. **Signez de nouvelles versions de recipes avec la nouvelle clé**, hors
   ligne comme toujours (`--use-signing-config=false --tlog-upload=false`,
   voir [Signer avec cosign](../../recipes/write-and-publish/#signer-avec-cosign)).
   Faites passer les Retrievers à ces versions. Elles sont synchronisées,
   transportées et poussées avec la nouvelle signature.

5. **Répétez le retrait sur chaque store** avant de le faire. Écrivez la
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

   Exécutée dans son propre processus, cette forme refuse un store qu'une
   instance sert : lancez-la pendant un court arrêt, ou sur un store miroir
   avant son départ. Avec `--instance`, la vérification s'exécute sur
   l'instance, contre les roots avec lesquels elle tourne, pas ceux proposés.
   Ne comptez pas sur `tobby sync --dry-run` pour ce contrôle : il lit les
   signatures du cookbook, pas celles du store.

6. **Retirez l'ancien root**, de haut en bas : les instances qui lisent le
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
| Push vers le registry de zone | une recipe dont seule la clé retirée vérifie la copie du **store** est refusée avant le push, `TBY-SIG-001`, même quand le cookbook porte une signature plus récente. Le refus est compté dans `tobby_promotion_refusals_total{code="TBY-SIG-001"}`. |
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
utilise la copie en cache, **sans le dire** : aucune ligne de journal
n'indique que la copie en cache a servi. Retirer une ancienne clé du fichier
servi à l'URL n'a donc aucun effet sur une instance qui ne joint pas l'URL à
son prochain démarrage. Pour un renouvellement que vous devez voir se
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
2. Faites épingler par les recipes des digests d'ingrédients que l'éditeur a
   signés avec la nouvelle clé, dans de nouvelles versions de recipes.
3. Répétez avec `tobby store verify --config proposed.yaml`, puis retirez
   l'ancien root.

La limite du store vaut aussi pour les ingrédients : un ingrédient que le store
détient déjà garde les signatures de sa première copie, et le contrôle de
l'ingrédient lit cette copie. Un éditeur qui re-signe un digest existant avec
sa nouvelle clé ne change pas ce que détient le store. Un ingrédient situé
dans un périmètre `allowUnsigned` est dispensé de l'exigence.

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
  directement `accounts.yaml`, et une instance en marche ne le relit pas.
  Lancez-les **uniquement quand l'instance est arrêtée**. Lancés contre une
  instance en marche, le changement n'est pas vu, et la prochaine écriture de
  l'instance remet sa propre copie en place. Rejoué : après
  `tobby user passwd admin` sur une instance en marche, le nouveau mot de passe
  est refusé (`401`), l'ancien accepté, et après un redémarrage c'est toujours
  l'ancien qui fonctionne.

## Tokens d'API

Un token porte son propre nom et son propre rôle, n'est montré qu'une fois à
la création et est stocké sous forme de hash (FR-072). Renouvelez par
recouvrement :

1. Créez le remplaçant sur `/admin/accounts` ou avec `POST /api/v1/tokens`.
   Le nom d'un token actif ne peut pas être réutilisé : donnez-lui un nouveau
   nom (`ci-2026`). Une valeur `expires`, ou `auth.tokens.defaultLifetime`,
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
l'adopte aussitôt. La configuration ne nomme toujours aucune paire : **au
redémarrage suivant, l'instance sert de nouveau le repli généré** (rejoué).
Après avoir adopté une paire depuis l'écran, faites-la pointer par la
configuration pour qu'elle survive au redémarrage :

```yaml
server:
  tls:
    certFile: /var/lib/tobby/state/tls/server.crt
    keyFile: /var/lib/tobby/state/tls/server.key
```

Dès lors, les fichiers sont relus à chaque changement comme toute paire
configurée. La forme API du remplacement (`PUT /api/v1/network/certificate`)
refuse une instance qui tourne sur le repli.
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

- **Re-signer une version déjà rangée n'atteint pas les stores qui la
  détiennent.** La procédure ci-dessus le contourne par de nouvelles versions ;
  un store garde les signatures de sa première copie, pour les recipes comme
  pour les ingrédients.
- **Retirer un trust root ne retire rien de ce qui a déjà été livré.** Cela
  arrête les synchronisations, les push et les imports de supports à venir ; le
  contenu déjà servi par une instance passthrough ou poussé dans un registry de
  zone y reste.
- **Un root `keyURL` peut retomber sans bruit** sur une clé en cache quand son
  URL ne répond pas.
- **La garde des clés appartient à l'organisation.** Tobby n'a ni liste de
  révocation ni expiration de clé. Une clé de signature compromise cesse d'être
  de confiance seulement quand son root est retiré de chaque instance, et
  seulement pour ce qui est vérifié ensuite.
- **La plupart des changements demandent un redémarrage**, et un redémarrage
  déconnecte toutes les sessions du navigateur et ferme la barrière de service
  d'une destination jusqu'à ce que le support soit vérifié de nouveau.
