---
title: Référence de configuration
description: Chaque clé de configuration avec son type, sa valeur par défaut et son rôle, la superposition flags > env > fichier, la correspondance TOBBY_* et un exemple de production complet et annoté.
sidebar:
  order: 2
---

Cette page documente la configuration en couches telle qu'elle est implémentée
dans la série v0.5.x actuelle (`internal/config/`). Chaque clé ci-dessous
existe dans le code aujourd'hui.

## Superposition des couches

Ordre de priorité, du plus fort au plus faible (SRS FR-003, vérifié dans
`config.LoadFor`) :

1. **Les flags de la ligne de commande** — voir la
   [référence CLI](../../reference/cli/).
2. **Les variables d'environnement `TOBBY_*`.**
3. **Le fichier de configuration YAML** — `/etc/tobby/config.yaml` par défaut,
   ou le chemin donné par `--config`. L'emplacement par défaut est optionnel ;
   un chemin donné explicitement doit exister.
4. **Les valeurs par défaut intégrées.**

La validation porte sur le résultat fusionné, par périmètre de commande : ce
qui est renseigné doit toujours être cohérent, ce qui est absent n'est une
erreur que si la commande en a besoin. Une configuration contradictoire (un
préfixe de base sans destination, des identifiants sans proxy, la moitié d'une
paire TLS) est **refusée au démarrage** plutôt qu'ignorée en silence : un
réglage qui a l'air de fonctionner doit fonctionner.

## Correspondance avec les variables d'environnement

La règle est mécanique : `TOBBY_` suivi du chemin de configuration en
majuscules avec des soulignés — `network.proxy.url` devient
`TOBBY_NETWORK_PROXY_URL`. Chaque variable prise en charge est déclarée
explicitement dans le code (`internal/config/env.go`) ; la liste complète
figure dans les tableaux de clés ci-dessous.

Deux variables `TOBBY_*` ne sont **pas** des clés de configuration et
n'apparaissent dans aucun tableau : `TOBBY_INSTANCE_URL` et `TOBBY_API_TOKEN`,
qui indiquent à une commande *quelle instance en cours d'exécution piloter* et
*avec quel identifiant* — voir [`tobby sync`](../../reference/cli/#tobby-sync).
Elles configurent un client, pas une instance : une instance ne les lit jamais.

Syntaxe des valeurs dans l'environnement :

- **Booléens** : `true`/`1` et `false`/`0`.
- **Durées** : syntaxe Go — `30s`, `15m`, `12h`.
- **Tailles** : un suffixe d'unité (`64MiB`, `512kB`, insensible à la casse) ou
  un nombre d'octets brut.
- **Listes** : séparées par des virgules
  (`TOBBY_REGISTRIES_INSECURE=reg1:5000,reg2`). Seules trois clés de type liste
  ont une forme d'environnement ; toute autre clé structurée ou de type liste
  (racines de confiance, périmètres, substitutions, FileSets…) se renseigne
  **uniquement dans le fichier de configuration**.

## Clés

### Niveau supérieur et répertoires

| Clé | Type | Défaut | Env | Rôle |
|---|---|---|---|---|
| `mode` | `passthrough` \| `mirror` | *(aucun — requis pour servir)* | `TOBBY_MODE` | Le mode de fonctionnement. Il n'y a volontairement pas de défaut : une instance doit déclarer ce qu'elle est. |
| `zone` | chaîne | *(aucun)* | `TOBBY_ZONE` | L'identité de zone de cette instance — le `metadata.name` du Retriever qui sert la zone. Une instance côté source la lit dans le Retriever qu'elle résout et n'a rien à renseigner ici ; une instance **côté destination** n'a pas de Retriever, son contenu arrive sur un support, et sans cette clé elle ne peut pas savoir si un support lui est adressé. `tobby media verify` et `tobby media import` refusent de s'exécuter sans elle. |
| `storage.root` | chemin | *(aucun — requis pour servir)* | `TOBBY_STORAGE_ROOT` | Le store autonome : artefacts, recipes, historique des tâches et journaux vivent tous dessous. |
| `storage.basePrefix` | chemin de dépôt | *(aucun)* | `TOBBY_STORAGE_BASE_PREFIX` | Préfixe de base de relocalisation, optionnel, appliqué à l'identique à chaque ingrédient de l'instance. |
| `storage.occupancyThreshold` | taille | *(aucun — non surveillé)* | `TOBBY_STORAGE_OCCUPANCY_THRESHOLD` | Empreinte disque au-delà de laquelle le store lève un **avertissement persistant** sur chaque page de l'interface, indique `exceeded` dans l'API et fait basculer la métrique `tobby_store_occupancy_exceeded`. Les deux franchissements sont visibles : repasser sous le seuil retire l'avertissement et la métrique. Non renseigné signifie non surveillé, jamais « dans les limites » — Tobby ne peut pas deviner la taille du volume qu'on lui a donné. |
| `state.root` | chemin | *(aucun — requis sauf si l'authentification est désactivée)* | `TOBBY_STATE_ROOT` | L'état de l'instance : comptes, tokens, cache des racines de confiance, téléchargements partiels. **Strictement hors du store** — les secrets ne voyagent jamais sur les supports, et ce répertoire est l'unique cible de sauvegarde. Une racine d'état à l'intérieur de la racine de stockage (ou l'inverse) est refusée. |

### Serveur

| Clé | Type | Défaut | Env | Rôle |
|---|---|---|---|---|
| `server.addr` | hôte:port | `:8080` | `TOBBY_SERVER_ADDR` | Adresse d'écoute de l'unique listener (interface, API, registry, sondes, métriques). |
| `server.secureCookies` | booléen | `false` | `TOBBY_SERVER_SECURE_COOKIES` | Marque les cookies de l'interface `Secure` lorsqu'un reverse proxy termine le TLS devant un listener en HTTP simple. Déclaré explicitement par l'opérateur : Tobby refuse de se fier à des en-têtes de transfert falsifiables pour des attributs de sécurité. Quand le listener sert lui-même du TLS, les cookies sont Secure dans tous les cas. |
| `server.tls.enabled` | booléen | `false` | `TOBBY_SERVER_TLS_ENABLED` | Sert du TLS avec un certificat de repli **auto-signé** (empreinte journalisée au démarrage). Fournir une paire de certificats implique le TLS sans ce flag. |
| `server.tls.certFile` | chemin | *(aucun)* | `TOBBY_SERVER_TLS_CERT_FILE` | Certificat PEM fourni par l'administrateur. Les deux éléments de la paire, ou aucun. Relu quand les fichiers changent sur le disque : les remplacer remplace le certificat servi sans redémarrage. |
| `server.tls.keyFile` | chemin | *(aucun)* | `TOBBY_SERVER_TLS_KEY_FILE` | La clé privée PEM correspondante. |
| `server.tls.hosts` | liste | *(aucun)* | *(fichier uniquement)* | Noms alternatifs de sujet supplémentaires pour le certificat auto-signé généré. Refusé lorsqu'un certificat est fourni — il ne ferait silencieusement rien. |

### Authentification

| Clé | Type | Défaut | Env | Rôle |
|---|---|---|---|---|
| `auth.disabled` | booléen | `false` | `TOBBY_AUTH_DISABLED` | Désactive l'authentification sur toutes les surfaces. Un choix délibéré — réglable dans le fichier ou l'environnement, **jamais par flag** — audité au démarrage et signalé en permanence par un bandeau dans l'interface. Voir [Authentification, comptes et RBAC](../../security/auth-rbac/). |
| `auth.sessionTTL` | durée | `12h` | `TOBBY_AUTH_SESSION_TTL` | Durée de vie d'une session de l'interface. Les sessions résident en mémoire : un redémarrage déconnecte tout le monde. |
| `auth.session.idleTimeout` | durée | `30m` | *(fichier uniquement)* | Met fin à une session de l'interface après cette durée sans activité de l'utilisateur : les navigations et les actions la renouvellent, les sondages en arrière-plan de la page non. Doit être strictement positive. `auth.sessionTTL` borne toujours la vie entière de la session. Chaque expiration est auditée sous `session.expire` (R-14). |
| `auth.tokens.defaultLifetime` | durée | *(aucun)* | *(fichier uniquement)* | Donne aux tokens d'API créés sans expiration explicite une échéance située à cette distance dans le futur. Non renseigné, le token reste de longue durée. Un token expiré est refusé avec `TBY-AUTH-013` et le refus est audité sous `token.expire` (R-14). |
| `auth.oidc.enabled` | booléen | `false` | *(fichier uniquement)* | Connexion OpenID Connect (FR-070), en plus des méthodes locales. Voir [Identité d'entreprise](../../security/enterprise-identity/). |
| `auth.oidc.issuer` | URL | *(aucun)* | *(fichier uniquement)* | L'émetteur (issuer) du fournisseur, en `https://` uniquement ; la découverte se fait depuis lui et nulle part ailleurs, et le `iss` des tokens doit lui correspondre exactement. |
| `auth.oidc.clientID` | chaîne | *(aucun)* | *(fichier uniquement)* | L'identifiant client de cette instance — l'audience que ses ID tokens doivent nommer. |
| `auth.oidc.clientSecretFile` | chemin | *(aucun)* | *(fichier uniquement)* | Le secret client, dans un fichier hors du store (NFR-020) ; jamais la valeur elle-même. |
| `auth.oidc.redirectURL` | URL | *(aucun)* | *(fichier uniquement)* | Le callback enregistré chez le fournisseur ; son chemin doit être `/auth/oidc/callback`. |
| `auth.oidc.scopes` | liste | *(aucun)* | *(fichier uniquement)* | Scopes demandés en plus de `openid`. |
| `auth.oidc.groupsClaim` | chaîne | `groups` | *(fichier uniquement)* | Le claim de l'ID token qui porte les groupes. |
| `auth.oidc.nameClaim` | chaîne | `preferred_username` | *(fichier uniquement)* | Le claim affiché comme nom de la personne, à l'écran et sous `actor_name` dans les journaux et l'audit. Affichage seulement : la clé d'identité est toujours `sub`. |
| `auth.oidc.signingAlgorithms` | liste | tous les algorithmes asymétriques | *(fichier uniquement)* | Liste d'autorisation des signatures d'ID token. `none` et `HS*` sont refusés. |
| `auth.oidc.displayName` | chaîne | `OpenID Connect` | *(fichier uniquement)* | Libellé du bouton de connexion. |
| `auth.oidc.roles` | table | *(aucun)* | *(fichier uniquement)* | `viewer`, `operator`, `admin` : les groupes qui accordent chaque rôle. Le plus élevé l'emporte ; aucune correspondance est un refus. Non vide obligatoire quand la méthode est activée. |
| `auth.saml.enabled` | booléen | `false` | *(fichier uniquement)* | Connexion SAML 2.0, initiée par le fournisseur de service uniquement (FR-071). |
| `auth.saml.entityID` | chaîne | *(aucun)* | *(fichier uniquement)* | L'entity ID de ce fournisseur de service — l'audience que les assertions doivent nommer. |
| `auth.saml.acsURL` | URL | *(aucun)* | *(fichier uniquement)* | L'assertion consumer service ; son chemin doit être `/auth/saml/acs`. Les réponses doivent le nommer comme Destination. |
| `auth.saml.idpMetadataFile` | chemin | *(aucun)* | *(fichier uniquement)* | Les métadonnées du fournisseur d'identité, épinglées. Exactement un entre cette clé et `idpMetadataURL`. |
| `auth.saml.idpMetadataURL` | URL | *(aucun)* | *(fichier uniquement)* | Récupère les métadonnées en `https://` à la place — une connexion sortante explicite (NFR-019). Exige `idpCertificateFile`. |
| `auth.saml.idpCertificateFile` | chemin | *(aucun)* | *(fichier uniquement)* | Le certificat de signature du fournisseur (PEM), épinglé. Obligatoire avec `idpMetadataURL` ; avec un fichier de métadonnées, il remplace les certificats que celui-ci porte. |
| `auth.saml.certFile`, `auth.saml.keyFile` | chemins | *(aucun)* | *(fichier uniquement)* | La paire de clés du fournisseur de service, qui signe ses requêtes. La clé est un chemin secret (NFR-020). |
| `auth.saml.groupsAttribute` | chaîne | `groups` | *(fichier uniquement)* | L'attribut d'assertion (Name ou FriendlyName) qui porte les groupes. |
| `auth.saml.nameAttribute` | chaîne | *(aucun)* | *(fichier uniquement)* | Un attribut affiché comme nom de la personne, à l'écran et sous `actor_name` dans les journaux et l'audit. Affichage seulement : la clé d'identité est toujours le NameID persistant. Sans lui, le NameID est affiché. |
| `auth.saml.clockSkew` | durée | `90s` | *(fichier uniquement)* | Écart d'horloge toléré avec le fournisseur, dans les deux sens ; au plus `5m`. |
| `auth.saml.displayName` | chaîne | `SAML` | *(fichier uniquement)* | Libellé du bouton de connexion. |
| `auth.saml.roles` | table | *(aucun)* | *(fichier uniquement)* | Comme `auth.oidc.roles`. |

### Réseau sortant

| Clé | Type | Défaut | Env | Rôle |
|---|---|---|---|---|
| `network.proxy.url` | URL | *(aucun)* | `TOBBY_NETWORK_PROXY_URL` | Proxy sortant pour **tout** le trafic sortant — un réglage, un transport, tous les chemins. Les proxys `https://` sont acceptés. Les identifiants dans l'URL sont refusés : ils ont leurs propres champs. |
| `network.proxy.httpsURL` | URL | *(aucun)* | `TOBBY_NETWORK_PROXY_HTTPS_URL` | Proxy distinct pour les destinations `https://` lorsqu'elles empruntent une autre route. Vide, `url` sert les deux. |
| `network.proxy.noProxy` | liste | *(aucun)* | `TOBBY_NETWORK_PROXY_NO_PROXY` | Destinations jointes directement : un hôte, un `.suffixe`, un bloc CIDR ou `*`. |
| `network.proxy.username` | chaîne | *(aucun)* | `TOBBY_NETWORK_PROXY_USERNAME` | Identifiant du proxy. |
| `network.proxy.password` | secret | *(aucun)* | `TOBBY_NETWORK_PROXY_PASSWORD` | Mot de passe du proxy. Jamais en flag (visible dans la table des processus) ; masqué par construction dans les journaux, les erreurs et `config dump`. |
| `network.tls.caFiles` | liste de chemins | *(aucun)* | `TOBBY_NETWORK_TLS_CA_FILES` | Bundles PEM d'autorités de certification privées approuvées **en plus** des autorités publiques — pour les registries, les dépôts Helm, le retriever, les URL de racines de confiance et le saut vers le proxy. |
| `network.tls.ca` | chaîne PEM | *(aucun)* | *(fichier uniquement)* | Bundle PEM en ligne, pour les déploiements qui injectent la configuration mais ne peuvent pas monter de fichier. |
| `network.tls.exclusiveTrust` | booléen | `false` | *(fichier uniquement)* | Écarte le magasin de racines publiques de l'hôte et ne laisse que les autorités configurées. Ne fait que restreindre la confiance. Il n'existe **aucun réglage, nulle part, qui désactive la vérification TLS**. |

### Registries

| Clé | Type | Défaut | Env | Rôle |
|---|---|---|---|---|
| `registries.insecure` | liste d'hôtes | *(aucun)* | `TOBBY_REGISTRIES_INSECURE` | Hôtes joints en HTTP simple. Hôte par hôte et explicite, jamais un interrupteur global. Ce n'est pas la réponse à une PKI privée — c'est `network.tls`. |
| `registries.substitutions` | table hôte → base | *(aucun)* | *(fichier uniquement)* | Substitution de source à la récupération : une zone aval récupère `docker.io/…` depuis la registry de sa zone amont sans modifier les recipes. Ne change que le point de contact, jamais le chemin relocalisé. La base est un hôte, plus au plus le chemin de base de l'amont ; le chemin canonique complet, hôte d'origine compris, lui est ajouté — voir [ce que doit contenir le registre substitué](../../passthrough/retriever-cascade/#ce-que-doit-contenir-le-registre-substitué). |
| `registries.allowlist` | liste de motifs d'hôtes | *(absent = sans restriction)* | *(fichier uniquement)* | Borne les registries que l'instance peut contacter, évaluée sur l'hôte effectivement joint. Absent et `[]` sont deux déclarations différentes : absent signifie aucune restriction (signalé comme non déclaré), `[]` signifie que rien n'est autorisé. Globs : `*` à l'intérieur d'un label DNS, `**` à travers les labels. |
| `registries.credentialsFile` | chemin | *(aucun)* | *(fichier uniquement)* | Un contenu `kubernetes.io/dockerconfigjson` ; les identifiants sont recherchés par l'hôte effectivement contacté, quel que soit le sens de circulation des octets. Doit se trouver hors du store transportable. |

### Aides à la consommation

Fichier de configuration uniquement. Voir les [aides à la consommation](../../passthrough/consumption-aids/).

| Clé | Type | Défaut | Rôle |
|---|---|---|---|
| `consumption.registry` | hôte nu | *(aucun : `destination.registry`)* | L'adresse depuis laquelle les clients de la zone tirent, quand ce n'est pas `destination.registry` : une instance qui sert elle-même son `/v2/` à la zone (UC3), ou une registry de zone jointe sous un autre nom. Un hôte nu ou `hôte:port`, jamais une URL. |
| `consumption.caFile` | chemin absolu | *(aucun)* | Chemin, **sur les nœuds du cluster**, du certificat de l'autorité qui signe cette registry. Écrit tel quel dans `configs.<hôte>.tls.ca_file` de l'extrait ; jamais lu par Tobby. |

### Flux de contenu

| Clé | Type | Défaut | Env | Rôle |
|---|---|---|---|---|
| `retriever.source` | chemin de fichier, URL HTTPS ou référence OCI | *(aucun)* | `TOBBY_RETRIEVER_SOURCE` | D'où vient le document d'état désiré. Voir [Retriever de zone et cascade](../../passthrough/retriever-cascade/). |
| `destination.registry` | hôte nu | *(aucun — vide, rien n'est promu)* | `TOBBY_DESTINATION_REGISTRY` | La registry de zone dans laquelle cette instance promeut. Un hôte nu, jamais une URL ni un chemin : le schéma suit `registries.insecure`, le chemin est calculé par la convention de relocalisation. |
| `destination.basePath` | chemin de dépôt | *(aucun)* | `TOBBY_DESTINATION_BASE_PATH` | Préfixe de chemin sous lequel les ingrédients relocalisés atterrissent à la destination. Distinct de `storage.basePrefix`, volontairement. Refusé sans destination. |
| `destination.cookbook` | chemin de dépôt | `cookbook` | `TOBBY_DESTINATION_COOKBOOK` | Où les recipes de la zone sont republiées avec leurs signatures. |
| `sync.parallelism` | entier | `3` | `TOBBY_SYNC_PARALLELISM` | Plafonne les transferts d'ingrédients concurrents. |
| `sync.retries` | entier | `3` | `TOBBY_SYNC_RETRIES` | Tentatives par ingrédient en cas d'échec transitoire (attente progressive bornée). |
| `sync.interval` | durée | `15m` | `TOBBY_SYNC_INTERVAL` | Cadence de réconciliation, **mode passthrough uniquement** (la synchronisation en mode mirror est manuelle par exigence). `0` désactive la boucle, ce qui est signalé au démarrage. Une surcharge de l'opérateur persistée dans le répertoire d'état l'emporte sur cette valeur et est auditée. |
| `sync.prune` | booléen | `false` | `TOBBY_SYNC_PRUNE` | **Passthrough uniquement.** Supprime, à la fin de chaque cycle de réconciliation, le contenu géré par recipe que le Retriever résolu ne référence plus (FR-045). Désactivé par défaut : un store de transit n'est pas une unité de livraison, et le rafraîchir n'implique jamais de le réduire. Les imports unitaires, la base de vulnérabilités hors ligne et le contenu déposé via `/v2/` ne sont jamais éligibles. Chaque suppression est listée dans le journal d'exécution. En mode mirror, le réglage est refusé — le prune y est actif par défaut et confirmé au déclenchement, sur la liste projetée et la taille totale. |
| `import.inspectTimeout` | durée | `20s` | `TOBBY_IMPORT_INSPECT_TIMEOUT` | Délai d'une inspection distante sur les écrans d'import unitaire ; son dépassement correspond à l'erreur dédiée [`TBY-REG-004`](../../reference/errors/#tby-reg-004). |
| `transfer.resumeThreshold` | taille | `64MiB` | `TOBBY_TRANSFER_RESUME_THRESHOLD` | Taille de blob à partir de laquelle un téléchargement devient reprenable à l'intérieur du blob (mis en file dans le répertoire d'état, repris par HTTP Range). `0` désactive la reprise intra-blob et diffuse chaque blob directement vers le store. |
| `preflight.safetyMarginPercent` | entier 0–99 | `10` | `TOBBY_PREFLIGHT_SAFETY_MARGIN_PERCENT` | Part de l'espace libre de la cible mise de côté par le contrôle préalable : une synchronisation est refusée avant tout transfert lorsque la projection dépasse l'espace libre moins cette marge, avec l'indication du déficit ([`TBY-STO-004`](../../reference/errors/#tby-sto-004)). La marge existe parce que le store n'est jamais le seul à écrire sur son volume. `0` rétablit le défaut — une clé absente ne doit pas signifier silencieusement « remplir le volume ». |
| `preflight.disabled` | booléen | `false` | `TOBBY_PREFLIGHT_DISABLED` | Transforme la barrière préalable en simple rapport : les volumes et les verdicts de système de fichiers sont toujours calculés et affichés, mais ne refusent plus une synchronisation. Un retrait explicite et annoncé d'un contrôle de sécurité — journalisé au démarrage, puis à chaque fois qu'il laisse passer un refus. |
| `tasks.keepFinished` | entier | `500` | `TOBBY_TASKS_KEEP_FINISHED` | Tâches terminées conservées (les plus récentes d'abord) ; les plus anciennes sont purgées avec leurs fichiers de journal. Les tâches en attente ou en cours ne sont jamais purgées. `0` conserve tout l'historique. |

### Confiance

Fichier de configuration uniquement. La vérification est active par défaut
pour chaque recipe ; l'assouplissement n'existe que sous forme de périmètres
explicitement déclarés, jamais comme contournement global. Le modèle complet
est décrit dans
[Signatures, racines de confiance et liste d'autorisation](../../security/content-trust/).

| Clé | Type | Rôle |
|---|---|---|
| `trust.roots[]` | liste | Clés publiques de confiance (cosign, à base de clés). Chaque racine a un `name` et exactement une clé parmi `key` (PEM en ligne), `keyFile`, `keyURL` (`https://` uniquement, récupérée et mise en cache au moment de la configuration — jamais au moment de la vérification). Plusieurs racines permettent la rotation par chevauchement. |
| `trust.scopes[]` | liste | Périmètres d'assouplissement déclarés, évalués dans l'ordre, le premier qui correspond l'emporte. Chaque périmètre a un `name`, des motifs glob `repositories` sur le chemin de cookbook **nominal canonique** de la recipe (`*` à l'intérieur d'un segment, `**` à travers ; le `:` d'un port s'écrit `_`), et doit changer quelque chose : `allowUnsigned: true` et/ou une restriction `roots`. Les périmètres assouplis sont signalés sur toutes les surfaces, jamais en silence. |

### Analyse de vulnérabilités

Chaque image est analysée à la récupération — une image de conteneur déclarée,
et tout ingrédient dont le contenu en est une, quel que soit son type déclaré
(ADR-0017) — en mode passthrough comme en mode mirror (FR-031, ADR-0008). Il
n'y a volontairement pas d'interrupteur `enabled` : l'analyse s'exécute
toujours et ses résultats sont toujours visibles — le mode **consultatif** par
défaut signale les violations sur toutes les surfaces et laisse passer le
contenu, avec un bandeau permanent qui le dit. Le `spec.scanPolicy` du
Retriever et l'annotation de recipe `recipe.tobby.dev/scan-threshold` affinent
le niveau de l'instance ; le modèle est décrit dans
[Analyse de vulnérabilités](../../security/vulnerability-scanning/).

| Clé | Type | Défaut | Environnement | Rôle |
|---|---|---|---|---|
| `scan.mode` | chaîne | `warn` | `TOBBY_SCAN_MODE` | `warn` (consultatif) ou `block` : un élément en infraction est alors refusé — jamais poussé, jamais écrit sur un support — de même qu'un élément qui n'a pas pu être analysé. Un seuil posé par le Retriever ou une recipe rend la politique bloquante pour son périmètre même sous `warn`. |
| `scan.threshold` | nombre | `7.0` | `TOBBY_SCAN_THRESHOLD` | Score de base CVSS à partir duquel un constat enfreint la politique, inclus, un nombre fini de `0` à `10` (`NaN` et les infinis sont refusés, dans le fichier comme dans la variable). Un constat sans score CVSS se place à la borne inférieure de sa bande de sévérité (`LOW` 0.1, `MEDIUM` 4.0, `HIGH` 7.0, `CRITICAL` 9.0) ; un constat `UNKNOWN` est signalé comme non noté, jamais comparé. |
| `scan.exceptions[]` | liste | aucune | — | Identifiants CVE tolérés au-delà du seuil : `id`, `justification` optionnelle, `expires` optionnel (RFC 3339). S'ajoutent à la liste propre du Retriever. Une exception échue ne s'applique plus et est signalée comme échue. Fichier de configuration uniquement. |
| `scan.maxDbAge` | durée | `168h` | `TOBBY_SCAN_MAX_DB_AGE` | Âge maximal de la base de vulnérabilités, mesuré depuis l'horodatage de construction qu'elle enregistre (R-13). Doit être strictement positif. |
| `scan.maxDbAgeAction` | chaîne | `warn` | `TOBBY_SCAN_MAX_DB_AGE_ACTION` | Au-delà de `maxDbAge` : `warn` signale l'obsolescence dans le rapport, l'interface et les métriques ; `block` refuse les éléments analysés avec une base obsolète (`TBY-SCN-004`), quel que soit `scan.mode`. |
| `scan.timeout` | durée | `10m` | `TOBBY_SCAN_TIMEOUT` | Borne d'une analyse, processus de l'analyseur compris. Un élément qui dépasse le délai est signalé non analysé. |
| `scan.dbRepository` | chaîne | relocalisation de `ghcr.io/aquasecurity/trivy-db` | `TOBBY_SCAN_DB_REPOSITORY` | Dépôt de ce store qui contient la base, lorsqu'il ne s'agit pas de la relocalisation de celui d'origine que porte la recipe `tobby-system`. |
| `scan.trivy.path` | chaîne | aucun | `TOBBY_SCAN_TRIVY_PATH` | Un binaire d'analyseur explicite, en chemin absolu. Sans `scan.trivy.digest`, il n'est accepté que s'il s'agit de la version que ce build épingle (Trivy 0.76.2) ; sur macOS, tout `trivy` est accepté, non épinglé. |
| `scan.trivy.digest` | chaîne | aucun | `TOBBY_SCAN_TRIVY_DIGEST` | Épinglage `sha256:<hex>` de `scan.trivy.path`, vérifié avant chaque exécution. |
| `scan.trivy.ref` | chaîne | `ghcr.io/aquasecurity/trivy` | `TOBBY_SCAN_TRIVY_REF` | Référence nominale du FileSet `tobby-system` qui porte le binaire sur les hôtes Linux et Windows. Exclusif avec `scan.trivy.path`. |

D'où vient le binaire, selon la plateforme : l'image de conteneur embarque la
version épinglée dans `/usr/libexec/tobby/trivy` ; un hôte Linux ou Windows
l'obtient par le canal `tobby-system` ; macOS prend le premier `trivy` du
`PATH`. Absent ou invérifiable, on entre en **mode dégradé** : un
enregistrement de journal au démarrage, un bandeau de danger,
`tobby_scan_scanner_available 0`, et chaque élément signalé non analysé avec
la raison — refusé sous une politique bloquante.

### FileSets

Fichier de configuration uniquement. Le service est désactivé par défaut :
seuls les FileSets listés ici sont servis sous `/files/<name>/`.

| Clé | Type | Rôle |
|---|---|---|
| `files.packRoots` | liste de chemins absolus | Confine l'empaquetage de FileSets **depuis l'interface web et l'API** (`POST /filesets/pack`, `POST /api/v1/filesets/pack`) : ces surfaces ne peuvent empaqueter un répertoire que s'il se trouve sous l'un de ces chemins. Le défaut — aucune entrée — refuse tout chemin et masque le formulaire, car lire un répertoire arbitraire de l'hôte à la demande est une capacité qu'une instance doit se voir *donner*, pas une capacité qu'elle doit avoir. Un chemin relatif est refusé au démarrage. [`tobby fileset pack`](../../reference/cli/#tobby-fileset-pack) sur l'hôte lui-même n'est pas concerné : celui qui l'exécute détient déjà les droits propres du système de fichiers. |
| `files.filesets[].name` | segment d'URL | Sert sous `/files/<name>/`. |
| `files.filesets[].ref` | hôte + dépôt | La référence nominale d'ingrédient du FileSet — sans tag ni digest ; le contenu servi est le digest vérifié que détient le store. |
| `files.filesets[].version` | tag | Épingle le tag servi ; vide, sert le tag semver le plus élevé présent localement. |
| `files.filesets[].platform` | p. ex. `linux/amd64` | Sélectionne le manifeste de plateforme d'un FileSet multi-plateforme ; un index ambigu sans cette clé est refusé. |
| `files.filesets[].anonymous` | booléen | Autorise pour ce FileSet les lectures non authentifiées (amorçage d'un hôte nu) — signalé, jamais silencieux. |

### Interface, journalisation, arrêt

| Clé | Type | Défaut | Env | Rôle |
|---|---|---|---|---|
| `ui.themeOverride` | chemin | *(aucun)* | `TOBBY_UI_THEME_OVERRIDE` | Feuille de style de l'opérateur servie après les tokens de design intégrés : changer l'identité visuelle sans reconstruire. Les tokens par défaut respectent WCAG AA ; les surcharges portent cette responsabilité. Les jetons surchargeables sont listés dans [Thème](../theming/). |
| `ui.showUpcoming` | booléen | `false` | `TOBBY_UI_SHOW_UPCOMING` | Affiche les entrées de navigation des jalons futurs comme des emplacements inertes et étiquetés (mode démonstration). La navigation de production ne montre que ce qui fonctionne. |
| `logging.media.file` | chemin relatif au store | `_tobby/logs/operations.log` | *(fichier uniquement)* | Où le journal des opérations est écrit **sur le support de transport** (mode mirror uniquement) : le canal d'audit de retour, pour que le destinataire du support puisse lire ce qui en a été fait. Le chemin doit se trouver hors de la couverture du manifeste du média — sous `_tobby/` — et l'instance refuse de démarrer sinon : un journal dans la couverture invalide, ligne après ligne, l'inventaire que la destination vérifie. |
| `logging.media.maxSize` | taille | `10MiB` | *(fichier uniquement)* | Seuil de rotation par taille de ce journal. |
| `logging.media.keep` | entier | `3` | *(fichier uniquement)* | Nombre de générations archivées à conserver. Borne le journal à `maxSize × (keep + 1)` sur un support dont l'objet même est de transporter des gigaoctets de contenu. |
| `logging.media.disabled` | booléen | `false` | *(fichier uniquement)* | Désactive le journal du support. Explicite et jamais par défaut : un support qui arrive sans journal ne peut pas être audité par son destinataire. |
| `logging.level` | chaîne | `info` | `TOBBY_LOGGING_LEVEL` | `debug`, `info`, `warn` ou `error`. |
| `shutdown.gracePeriod` | durée | `30s` | `TOBBY_SHUTDOWN_GRACE_PERIOD` | Délai de vidange après SIGTERM/SIGINT. |

Il n'existe aucune clé de configuration pour la langue de l'interface : la CLI
suit la locale de l'hôte, l'interface web négocie sa langue par utilisateur.

## Vérifier la configuration effective

`tobby config dump` affiche sur la sortie standard le résultat fusionné des
quatre couches, en YAML — la configuration exacte avec laquelle l'instance
s'exécuterait. **Les secrets sont masqués par construction** : les valeurs
sensibles sont portées par un type qui ne peut pas sérialiser son contenu, de
sorte qu'un identifiant qui atteindrait le dump (ou une ligne de journal, ou un
message d'erreur) est impossible plutôt que vérifié après coup. Un secret
configuré s'affiche `REDACTED` ; un secret absent, une chaîne vide.

Utilisez-le comme outil de contrôle : après toute modification, comparez
`tobby config dump` à ce que vous visiez, puis redémarrez.

## Exemple de production complet

Une instance passthrough annotée qui promeut vers une registry de zone,
derrière un proxy d'entreprise, avec une CA privée et un périmètre de registries
borné :

```yaml
# /etc/tobby/config.yaml — instance passthrough de la zone « prod ».
mode: passthrough

storage:
  root: /var/lib/tobby/storage        # le store autonome
state:
  root: /var/lib/tobby/state          # comptes, tokens — la cible de sauvegarde,
                                      # jamais à l'intérieur du store

server:
  addr: :8080
  secureCookies: true                 # le TLS se termine sur l'ingress devant
                                      # ce listener

network:
  proxy:
    url: http://proxy.example.com:3128
    noProxy: [registry.zone.example.com, .cluster.local]
    username: tobby-egress
    # password : à fournir par TOBBY_NETWORK_PROXY_PASSWORD — jamais en flag,
    # jamais affiché nulle part.
  tls:
    caFiles: [/etc/tobby/pki/corporate-ca.pem]   # CA privée, la vérification reste active

registries:
  credentialsFile: /etc/tobby/dockerconfig.json  # kubernetes.io/dockerconfigjson
  allowlist:                          # tout le reste est refusé avant transfert
    - registry.upstream.example.com
    - registry.zone.example.com
  substitutions:                      # récupérer le contenu docker.io depuis la zone amont
    docker.io: registry.upstream.example.com   # un hôte : le chemin d'origine est ajouté

retriever:
  source: https://registry.upstream.example.com/zone/retriever.yaml

destination:
  registry: registry.zone.example.com # hôte nu — le chemin vient de la relocalisation
  cookbook: cookbook                  # où les recipes sont republiées, signées

trust:
  roots:
    - name: qualification-2026
      keyFile: /etc/tobby/pki/cosign-qualification-2026.pub
    - name: qualification-2025        # rotation par chevauchement
      keyFile: /etc/tobby/pki/cosign-qualification-2025.pub

sync:
  parallelism: 3
  retries: 3
  interval: 15m

preflight:
  safetyMarginPercent: 10           # refuser avant de remplir le volume (FR-055)

scan:
  mode: block                       # refuser ce qui enfreint la politique (FR-031)
  threshold: 7.0                    # CVSS, inclus
  exceptions:
    - id: CVE-2024-12345
      justification: parseur vulnérable désactivé dans notre build
      expires: 2027-03-31T00:00:00Z
  maxDbAge: 168h                    # la base doit avoir été mise à jour cette semaine
  maxDbAgeAction: warn

logging:
  level: info
shutdown:
  gracePeriod: 30s
```

Les variantes propres à chaque déploiement (valeurs Helm, unité systemd,
paquets hors ligne) se trouvent dans
[Déployer : Kubernetes et VM](../../passthrough/deploy/).
