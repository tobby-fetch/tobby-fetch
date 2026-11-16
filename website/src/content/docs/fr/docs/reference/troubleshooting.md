---
title: Dépannage
description: Partez de ce que vous observez — l'instance ne démarre pas, une connexion échoue, une recipe est refusée, un média est bloqué, une analyse se dégrade — et retrouvez le code TBY, la cause probable et l'action corrective.
sidebar:
  order: 5
---

Cette page est organisée par **symptôme** : ce que vous voyez, pas le nom que
Tobby lui donne. Chaque ligne renvoie au code `TBY-*` qui l'explique, avec
l'action corrective en une ligne. La [référence des erreurs](../errors/) est le
catalogue complet (ce qui s'est passé, cause probable, résolution possible hors
ligne, ce qui est bloqué) ; le même texte est disponible sur l'instance
elle-même à `/help#TBY-SIG-001`, si bien qu'un opérateur en zone isolée n'a pas
besoin de ce site.

## D'abord, quatre réflexes à avoir à chaque fois

1. **Lire le code.** Toute erreur visible, dans l'interface, la CLI et l'API
   (sous forme de document de problème RFC 9457), porte un
   `TBY-<domaine>-<nnn>` stable. Le code ne change jamais de sens.
2. **Lire le code de sortie** de la commande. `3` est un refus de politique
   (liste d'autorisation, rôle, refus de démarrage sécurisé par défaut, tag
   immuable), `4` un échec de vérification (signature, digest, somme de
   contrôle) qu'aucune dérogation ne rouvre, `1` un échec opérationnel, `2` une
   erreur d'usage, `5` « changements planifiés » avec `tobby sync --dry-run`.
   Voir les [codes de sortie](../cli/#codes-de-sortie).
3. **Suivre l'identifiant de run.** Une synchronisation, un import ou une
   vérification de média porte un même identifiant à travers les journaux, la
   tâche et son rapport. Filtrez les journaux JSON dessus.
4. **Vérifier ce que l'instance croit être sa configuration :**
   `tobby config dump` affiche la configuration effective (secrets masqués),
   après combinaison des options, des variables `TOBBY_*` et du fichier.

## L'instance ne démarre pas

| Vous voyez | Code | À faire |
| --- | --- | --- |
| « aucun compte local n'est configuré » | [`TBY-AUTH-001`](../errors/#tby-auth-001) | `tobby user add --role admin <nom>` sur l'hôte, puis redémarrer. Tobby ne démarre jamais ouvert. |
| « la configuration est invalide » | [`TBY-CFG-001`](../errors/#tby-cfg-001) | Corriger le réglage nommé dans le message ; vérifier avec `tobby config dump` ; redémarrer. |
| « un fichier secret est configuré dans le store transportable » | [`TBY-CFG-002`](../errors/#tby-cfg-002) | Déplacer `state.root`, `registries.credentialsFile` ou `server.tls.keyFile` hors de `storage.root` (les liens symboliques sont résolus). |
| « la configuration du proxy sortant est inutilisable » | [`TBY-NET-001`](../errors/#tby-net-001) | Utiliser `http://hôte:port` ; mettre les identifiants dans `network.proxy.username` et `network.proxy.password`, pas dans l'URL. |
| « une autorité de certification configurée n'a pas pu être chargée » | [`TBY-NET-002`](../errors/#tby-net-002) | Le fichier doit exister, être lisible et contenir un `CERTIFICATE` PEM. Aucun réglage ne désactive la vérification TLS. |
| « le certificat du listener est inutilisable » | [`TBY-NET-003`](../errors/#tby-net-003) | `server.tls.certFile` et `keyFile` doivent former une paire PEM lisible et cohérente ; retirer les deux pour obtenir un certificat auto-signé généré. |

## Je ne peux pas me connecter, ou une action est refusée

| Vous voyez | Code | À faire |
| --- | --- | --- |
| Identifiants incorrects | [`TBY-AUTH-002`](../errors/#tby-auth-002) | Vérifier le nom et le mot de passe. Le message ne dit jamais lequel est faux. |
| « action non autorisée pour votre rôle » | [`TBY-AUTH-003`](../errors/#tby-auth-003) | Se connecter avec un compte qui détient le rôle indiqué, ou demander à un administrateur. |
| Un formulaire est rejeté, « n'a pas pu être soumis en sécurité » | [`TBY-AUTH-004`](../errors/#tby-auth-004) | La page est restée ouverte trop longtemps. Le formulaire a été rechargé avec un jeton neuf : le soumettre à nouveau. |
| « votre session a expiré » | [`TBY-AUTH-005`](../errors/#tby-auth-005) | Délai d'inactivité (`auth.session.idleTimeout`), durée de vie de la session (`auth.sessionTTL`) ou redémarrage de l'instance. Se reconnecter. |
| HTTP 429, « trop de tentatives d'authentification échouées » | [`TBY-AUTH-012`](../errors/#tby-auth-012) | Patienter, puis vérifier l'identifiant. Une adresse de sortie partagée peut masquer un autre client mal configuré. |
| Un jeton d'API est refusé comme expiré | [`TBY-AUTH-013`](../errors/#tby-auth-013) | Demander un nouveau jeton à un administrateur et le remplacer dans le client. |
| La connexion unique par OIDC échoue | [`TBY-AUTH-014`](../errors/#tby-auth-014) | Relancer la connexion depuis la page de connexion. L'administrateur trouve le contrôle en échec dans le journal, sous le même identifiant de corrélation (state, nonce, signature, émetteur, audience, URL de callback). |
| La connexion unique par SAML échoue | [`TBY-AUTH-015`](../errors/#tby-auth-015) | Relancer depuis la page de connexion : Tobby n'accepte que les connexions qu'il a lui-même initiées. Vérifier les horloges et l'audience. |
| Le fournisseur d'identité est injoignable | [`TBY-AUTH-016`](../errors/#tby-auth-016) | Utiliser la connexion locale, qui reste disponible et est auditée. Vérifier le chemin réseau (proxy, CA privée). |
| Connecté chez le fournisseur, mais sans rôle | [`TBY-AUTH-017`](../errors/#tby-auth-017) | Faire correspondre l'un de vos groupes dans `roles.viewer`, `roles.operator` ou `roles.admin`, dans la section du fournisseur. |
| « trop de connexions initiées » | [`TBY-AUTH-018`](../errors/#tby-auth-018) | Patienter quelques secondes et ne se connecter qu'une fois. |
| Un nom de compte ou de jeton est refusé | [`TBY-AUTH-019`](../errors/#tby-auth-019) | `:` est réservé aux identités fédérées ; choisir un autre nom. |
| Le dernier administrateur ne peut être ni supprimé ni rétrogradé | [`TBY-AUTH-011`](../errors/#tby-auth-011) | Créer d'abord un second administrateur. |
| Le changement de mot de passe échoue | [`TBY-AUTH-006`](../errors/#tby-auth-006), [`TBY-AUTH-007`](../errors/#tby-auth-007) | Ressaisir le mot de passe actuel ; le nouveau doit être non vide, différent et confirmé. Un administrateur peut le réinitialiser avec `tobby user passwd <nom>`. |

## Une recipe est refusée

| Vous voyez | Code | À faire |
| --- | --- | --- |
| « la signature de la recipe n'a pas pu être vérifiée » | [`TBY-SIG-001`](../errors/#tby-sig-001) | Le message liste les empreintes essayées et, le cas échéant, le périmètre. Vérifier que la moitié publique de la clé de signature est une trust root configurée, que le périmètre concerné la liste dans `roots`, et que la recipe relève bien du périmètre attendu. Une recipe non signée ou mal signée doit être re-signée en amont. Jamais contournable. |
| « un digest épinglé ne correspond pas au contenu récupéré » | [`TBY-SIG-002`](../errors/#tby-sig-002) | Ne pas forcer. Vérifier la source et la recipe ; un changement légitime exige une recipe re-signée qui épingle le nouveau digest. |
| « le type de l'artefact ne correspond pas » | [`TBY-SIG-003`](../errors/#tby-sig-003) | Vérifier le dépôt source ; si le changement de type est légitime, mettre à jour et re-signer, sinon considérer la source comme compromise. |
| « le fichier recipe ou retriever est invalide » | [`TBY-VAL-001`](../errors/#tby-val-001) | Le message nomme le fichier et le chemin du champ. Contrôler avec `recipe lint`. |
| « cette version de recipe est déjà publiée, avec un contenu différent » | [`TBY-POL-004`](../errors/#tby-pol-004) | Publier sous un nouveau `metadata.version`. Une version publiée est immuable. |
| Une entrée de cookbook ne se résout pas | [`TBY-REG-006`](../errors/#tby-reg-006) | Aucune version publiée ne satisfait la contrainte. Tobby ne se rabat jamais sur une version voisine. Vérifier les tags réellement publiés. |

## Un transfert est refusé avant de commencer

| Vous voyez | Code | À faire |
| --- | --- | --- |
| « le registre n'est pas dans la liste d'autorisation » | [`TBY-POL-001`](../errors/#tby-pol-001) | Si légitime, ajouter l'hôte à `registries.allowlist` (changement audité). La liste s'applique à l'hôte réellement contacté, après substitution. |
| « le registre de destination ne peut pas stocker cette référence » | [`TBY-DST-001`](../errors/#tby-dst-001) | Raccourcir le chemin relocalisé avec `destination.basePath` ou `storage.basePrefix`. Rien n'a été poussé. |
| « espace libre insuffisant » | [`TBY-STO-004`](../errors/#tby-sto-004) | Libérer le nombre d'octets indiqué, utiliser un volume plus grand, ou supprimer le contenu non référencé. |
| « le système de fichiers cible ne peut pas contenir un fichier aussi gros » | [`TBY-STO-005`](../errors/#tby-sto-005) | Reformater en exFAT, NTFS, ext4 ou XFS, ou scinder le transfert. |
| « il manque une dépendance embarquée au chart Helm » | [`TBY-CHT-001`](../errors/#tby-cht-001) | Reconditionner avec `helm dependency build` puis `helm package`, et publier à nouveau. |
| Suppression individuelle refusée | [`TBY-POL-002`](../errors/#tby-pol-002), [`TBY-POL-003`](../errors/#tby-pol-003) | Seul le contenu importé à l'unité est supprimable individuellement. Supprimer plutôt la recipe qui le gère ; le contenu semé se gère avec l'outil qui l'a poussé. |

## Un registre source ne répond pas

| Vous voyez | Code | À faire |
| --- | --- | --- |
| La référence ne peut pas être analysée | [`TBY-REG-001`](../errors/#tby-reg-001) | Utiliser `registre/dépôt:tag` ou `registre/dépôt@sha256:…`, avec un hôte de registre. |
| Le registre est injoignable | [`TBY-REG-002`](../errors/#tby-reg-002) | Vérifier la connectivité depuis l'hôte de l'instance et les réglages du proxy. La tâche est rejouée avec un backoff borné. |
| Authentification refusée | [`TBY-REG-003`](../errors/#tby-reg-003) | Ajouter les identifiants de cet hôte dans `registries.credentialsFile`. |
| L'inspection a expiré | [`TBY-REG-004`](../errors/#tby-reg-004) | Vérifier le registre et le chemin jusqu'à lui ; n'augmenter `import.inspectTimeout` que si le lien est connu pour être lent. |
| « n'existe pas » | [`TBY-REG-005`](../errors/#tby-reg-005) | Vérifier le nom du dépôt et le tag. |
| Un téléchargement partiel est inutilisable | [`TBY-REG-007`](../errors/#tby-reg-007) | Réessayer. Si cela se reproduit sur la même source, positionner `transfer.resumeThreshold: 0` et vérifier tout proxy cache. |
| Une plateforme manque dans l'index | [`TBY-REG-008`](../errors/#tby-reg-008) | Confronter `platforms` à ce que la source publie ; Tobby n'abandonne jamais silencieusement une plateforme demandée. |
| Erreurs de certificat sur un registre interne | [`TBY-NET-002`](../errors/#tby-net-002) | Faire confiance à l'autorité privée avec `network.tls.caFiles` ou `network.tls.ca` (voir la [configuration](../configuration/)) ; il n'existe aucun moyen de sauter la vérification. |
| L'écriture du téléchargement partiel échoue | [`TBY-STO-003`](../errors/#tby-sto-003) | Vérifier l'espace libre et les permissions du répertoire d'état. |

## L'analyse de vulnérabilités est dégradée, ou refuse quelque chose

L'analyse ne se désactive jamais silencieusement : un état dégradé se voit dans
un bandeau, un rapport et une métrique.

| Vous voyez | Code | À faire |
| --- | --- | --- |
| « l'analyseur est indisponible » | [`TBY-SCN-001`](../errors/#tby-scn-001) | Image de conteneur : il est fourni. Hôte Linux ou Windows : synchroniser la recipe `tobby-system`. macOS : installer `trivy` dans le `PATH`. Sous `scan.mode: block`, rien de non analysé n'est poussé entre-temps. |
| « le binaire de l'analyseur ne correspond pas à son digest épinglé » | [`TBY-SCN-002`](../errors/#tby-scn-002) | Le fichier a été remplacé ou endommagé après son épinglage et n'a pas été exécuté. Le restaurer en synchronisant à nouveau `tobby-system`. |
| « aucune base de vulnérabilités n'est disponible » | [`TBY-SCN-003`](../errors/#tby-scn-003) | Synchroniser la recipe `tobby-system`, par le média en zone isolée. La vérification d'un média n'a pas besoin de la base. |
| « la base est trop ancienne » | [`TBY-SCN-004`](../errors/#tby-scn-004) | Apporter une base fraîche. Avec `scan.maxDbAgeAction: block`, les éléments analysés avec une base périmée ne sont pas poussés. |
| « la base se déclare construite dans le futur » | [`TBY-SCN-008`](../errors/#tby-scn-008) | L'horloge de l'hôte est en retard, ou les métadonnées de la base sont fausses. Elle est traitée comme périmée. |
| « viole la politique de vulnérabilités et n'a pas été poussé » | [`TBY-SCN-005`](../errors/#tby-scn-005) | Corriger en amont et mettre la recipe à jour, ou déclarer la CVE en dérogation, avec justification, dans `spec.scanPolicy` ou `scan.exceptions`. |
| Idem, mais poussé | [`TBY-SCN-006`](../errors/#tby-scn-006) | Mode consultatif : le push a eu lieu et la violation est consignée. Passer en `block` pour refuser. |
| « n'a pas pu être analysé » | [`TBY-SCN-007`](../errors/#tby-scn-007) | L'analyseur s'est arrêté sur une erreur ; le détail est dans le message et le journal. |
| Un seuil de recipe a été ignoré | [`TBY-SCN-020`](../errors/#tby-scn-020) | Une recipe peut abaisser le seuil, jamais le relever. |
| Une dérogation ne s'applique plus | [`TBY-SCN-021`](../errors/#tby-scn-021) | Elle a expiré. La renouveler délibérément ou corriger la vulnérabilité. |
| Des vulnérabilités sans sévérité | [`TBY-SCN-022`](../errors/#tby-scn-022) | Les sources d'avis ne donnent aucun score ; la politique ne peut pas les comparer au seuil. |
| Un ingrédient a été analysé comme une image alors qu'il n'est pas déclaré comme tel | [`TBY-SCN-023`](../errors/#tby-scn-023) | Normal : ce qui est analysé est décidé par le contenu. |
| Une dérogation « ne couvre pas » un élément | [`TBY-SCN-026`](../errors/#tby-scn-026) | Sa portée (digest, dépôt, recipe) exclut l'élément. Élargir la portée si l'élément devait être couvert ; `tobby scan exceptions` montre chaque portée. |
| L'instance refuse de démarrer sur un document OpenVEX | [`TBY-SCN-027`](../errors/#tby-scn-027) | Le fichier listé sous `scan.vex` est illisible ou n'est pas un document OpenVEX v0.2.0 valide ; le détail nomme le champ. Le corriger ou le retirer. |
| Une dérogation de Retriever « n'a pas d'échéance » | [`TBY-SCN-028`](../errors/#tby-scn-028) | Elle s'applique, sans limite. Demander au propriétaire du Retriever d'ajouter `expires` et une justification. |
| L'instance refuse de démarrer sur `scan.exceptions[i]` | [`TBY-CFG-001`](../errors/#tby-cfg-001) | Depuis v1.0.0-rc.1, chaque dérogation d'instance exige `expires` et `justification` ; le message nomme l'entrée. |

## Un média amovible est bloqué

Côté destination, rien n'est poussé ni servi tant que le média n'a pas été
vérifié. Ouvrez l'écran **Média** et lancez *Vérifier* ; le rapport nomme
chaque recipe bloquée et le fichier en cause.

| Vous voyez | Code | À faire |
| --- | --- | --- |
| `/v2/` ou `/files/` répondent `403` | [`TBY-MED-030`](../errors/#tby-med-030) | Lancer *Vérifier* sur l'écran Média ou `POST /api/v1/media/verify`. `tobby media verify` aboutit à un verdict mais n'ouvre pas les surfaces d'une instance en cours d'exécution. |
| Une vérification est déjà en cours | [`TBY-MED-031`](../errors/#tby-med-031) | Attendre qu'elle se termine. |
| Le média n'est pas ressorti intègre | [`TBY-MED-032`](../errors/#tby-med-032) | Lire le rapport. Recopier le média et vérifier à nouveau ; les recipes intactes peuvent quand même être poussées. |
| « ne porte aucun manifeste de média », ou il est illisible | [`TBY-MED-001`](../errors/#tby-med-001), [`TBY-MED-002`](../errors/#tby-med-002) | Le store n'a pas été écrit comme un média, ou le fichier est tronqué. Recopier depuis la source. |
| Version de format non prise en charge | [`TBY-MED-003`](../errors/#tby-med-003), [`TBY-MED-004`](../errors/#tby-med-004) | Le média et l'instance sont sur des versions majeures différentes. Mettre à jour la destination ([mettre à jour Tobby](../../air-gap/update-tobby/)). |
| « adressé à une autre zone » | [`TBY-MED-006`](../errors/#tby-med-006) | Vérifier que c'est le bon média. Un administrateur peut passer outre ; la dérogation est auditée. |
| « plus ancien que le dernier importé » | [`TBY-MED-007`](../errors/#tby-med-007) | Vérifier que c'est le média courant. Un administrateur peut passer outre, par exemple pour restaurer volontairement une livraison plus ancienne. |
| Un fichier est absent, de mauvaise taille, ou ne correspond pas à son digest | [`TBY-MED-010`](../errors/#tby-med-010), [`TBY-MED-011`](../errors/#tby-med-011), [`TBY-MED-012`](../errors/#tby-med-012), [`TBY-MED-015`](../errors/#tby-med-015) | Recopier le store depuis la source. La recipe qui atteint le fichier est bloquée ; aucune dérogation n'existe. |
| Un fichier ou un manifeste n'est pas couvert, ou illisible | [`TBY-MED-013`](../errors/#tby-med-013), [`TBY-MED-014`](../errors/#tby-med-014), [`TBY-MED-005`](../errors/#tby-med-005) | L'inventaire ne correspond pas au contenu : recopier depuis la source. |
| Le graphe de recipes du support liste un ingrédient que la recipe signée n'épingle pas | [`TBY-MED-016`](../errors/#tby-med-016) | La comptabilité a été modifiée : n'importez pas ; reproduisez le support depuis la source. Aucune dérogation n'existe. |
| Un fichier qu'aucune recipe n'atteint | [`TBY-MED-020`](../errors/#tby-med-020), [`TBY-MED-021`](../errors/#tby-med-021), [`TBY-MED-022`](../errors/#tby-med-022) | Signalé, jamais poussé. Le plus souvent des restes d'une livraison précédente : [repartir d'un média propre](../../air-gap/manage-media/). |

## Le store semble endommagé

| Vous voyez | Code | À faire |
| --- | --- | --- |
| Un blob ne correspond pas à son digest | [`TBY-INT-001`](../errors/#tby-int-001) | La vérification ne modifie rien. Resynchroniser les recipes qui l'atteignent, ou restaurer le store depuis une copie saine ; contrôler le disque. |
| Un manifeste est incomplet dans le store | [`TBY-INT-002`](../errors/#tby-int-002) | Idem. |
| Contenu atteint par aucune recipe | [`TBY-INT-003`](../errors/#tby-int-003) | Signalé à titre d'information : lancer un prune, ou le laisser. |
| Une vérification est déjà en cours, ou a été annulée | [`TBY-INT-004`](../errors/#tby-int-004), [`TBY-INT-006`](../errors/#tby-int-006) | Attendre celle en cours ; une vérification annulée ne dit rien du store, la relancer. |
| Le store ne peut pas être lu ou écrit | [`TBY-STO-001`](../errors/#tby-sto-001), [`TBY-STO-002`](../errors/#tby-sto-002) | Volume monté, permissions, espace libre. |
| Une réinitialisation n'a pas eu lieu | [`TBY-STO-006`](../errors/#tby-sto-006) | La confirmation saisie ne correspondait pas. |

Voir [vérifier le store](../../passthrough/verify-store/) pour ce que la
vérification contrôle.

## Petits outils et cas limites

| Vous voyez | Code | À faire |
| --- | --- | --- |
| Un layout OCI est illisible, une entrée d'archive est refusée, la destination d'export existe | [`TBY-LAY-001`](../errors/#tby-lay-001), [`TBY-LAY-002`](../errors/#tby-lay-002), [`TBY-LAY-003`](../errors/#tby-lay-003) | Vérifier le chemin, l'archive (les entrées qui sortent de la racine sont refusées), ou choisir une nouvelle destination. |
| Un file set ne peut pas être empaqueté | [`TBY-FIL-001`](../errors/#tby-fil-001), [`TBY-FIL-002`](../errors/#tby-fil-002), [`TBY-FIL-003`](../errors/#tby-fil-003) | Un lien symbolique pointant hors du répertoire est refusé ; depuis l'interface et l'API, seuls les répertoires sous `files.packRoots` peuvent être empaquetés. |
| « la tâche n'existe pas » | [`TBY-TSK-001`](../errors/#tby-tsk-001) | Ouvrir la liste des tâches et suivre une tâche existante. |
| « une erreur interne s'est produite » | [`TBY-SRV-001`](../errors/#tby-srv-001) | La signaler avec l'identifiant de run et les lignes de journal qui l'entourent. |
| « cette ressource n'existe pas » | [`TBY-SRV-002`](../errors/#tby-srv-002) | L'adresse est erronée ou le contenu a été supprimé. |
| La page indique que l'instance est injoignable | [`TBY-SRV-003`](../errors/#tby-srv-003) | Condition côté client ; vérifier la liaison réseau. |

## Signaler un problème que vous ne parvenez pas à résoudre

Communiquez aux mainteneurs le code d'erreur, le code de sortie, la version
(`tobby version`), l'identifiant de run et les lignes de journal qui le
portent, ainsi que la sortie de `tobby config dump` expurgée de tout élément
sensible. Ne joignez ni secrets ni store. Pour une vulnérabilité suspectée,
suivez la [politique de sécurité](https://github.com/tobby-fetch/tobby-fetch/blob/main/SECURITY.md)
plutôt que d'ouvrir un ticket public.
