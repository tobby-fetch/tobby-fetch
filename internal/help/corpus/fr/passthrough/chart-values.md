---
title: Valeurs du chart Helm
description: Toutes les valeurs du chart de référence regroupées par usage, avec leur défaut et leur effet, les clés que le chart possède et refuse, et ce que le dépôt énonce sur le dimensionnement.
sidebar:
  order: 16
---

Le chart de référence est la façon prise en charge de faire tourner Tobby sur
Kubernetes. Cette page liste ses valeurs : ce que fait chacune, son défaut, et
les quelques clés que le chart refuse de vous laisser poser. Pour l'installer,
partez de [Déployer](../deploy/#avec-helm) ; la source du chart est
[`deploy/charts/tobby/`](https://github.com/tobby-fetch/tobby-fetch/tree/main/deploy/charts/tobby),
et son `values.yaml` porte les mêmes informations en commentaires.

:::tip[En bref]
- `config` devient le fichier de configuration de Tobby ; `env` et `extraArgs`
  s'y superposent. Quatre clés de ce fichier appartiennent au chart, qui refuse
  de se rendre si vous les posez — [clés possédées](#les-clés-que-le-chart-possède-et-refuse).
- Le store et l'état sont deux claims distincts (100Gi et 20Gi par défaut),
  conservés quand la release est retirée — [persistance](#persistance).
- L'ingress, le ServiceMonitor, les règles d'alerte et le tableau de bord sont
  tous **désactivés** par défaut — [service et ingress](#service-et-ingress),
  [métriques et alertes](#métriques-alertes-et-tableau-de-bord).
- Le pod tourne sans privilèges, sur un système de fichiers racine en lecture
  seule, toutes capabilities retirées —
  [sécurité](#contexte-de-sécurité-et-compte-de-service).
- Le dépôt ne mesure aucun chiffre de mémoire ou de disque ; il donne des
  défauts et une règle empirique — [dimensionnement](#dimensionnement).
:::

## Comment les valeurs atteignent Tobby

Tobby lit ses réglages dans trois couches, de la moins prioritaire à la plus
prioritaire. Le chart pilote les trois :

| Valeur | Devient | Priorité |
|---|---|---|
| `config` | `/etc/tobby/config.yaml`, rendu dans un Secret et monté en lecture seule | la plus basse |
| `env` | variables d'environnement `TOBBY_*` du conteneur | intermédiaire |
| `extraArgs` | options ajoutées après `serve` | la plus haute |

Les clés sous `config` reprennent exactement la configuration de Tobby — la
[référence de configuration](../../reference/configuration/) les liste toutes —
et le binaire lit le fichier strictement : une clé qu'il ne connaît pas arrête
le démarrage. Tout ce qui est sous `config` est rendu tel quel ; cette page ne
liste donc que les clés que le `values.yaml` du chart documente lui-même.

Pour apporter votre propre fichier, posez `existingConfigSecret` au nom d'un
Secret qui porte une clé `config.yaml`. Le chart ne rend alors aucun Secret de
configuration et ignore le contenu de `config` — mais il contrôle quand même
`config` au regard des refus ci-dessous, et l'empreinte qui redémarre le pod est
calculée à partir de `config`, pas de votre Secret : si vous changez ce Secret,
redémarrez vous-même le pod.

## Les clés que le chart possède et refuse

Le chart dérive quatre réglages du fichier de configuration des valeurs qui
pilotent aussi les montages de volumes et le listener. Une configuration qui
contredit son propre pod est refusée au moment du `helm template`, pas déboguée
à l'exécution.

| Clé de configuration | Dérivée de | Refusée quand |
|---|---|---|
| `storage.root` | `persistence.storage.mountPath` | vous posez `config.storage.root` |
| `state.root` | `persistence.state.mountPath` | vous posez `config.state.root` |
| `server.addr` | `containerPort` (sous la forme `:<port>`) | vous posez `config.server.addr` |
| `registries.credentialsFile` | le montage des identifiants de registre | vous la posez **alors que le Secret d'identifiants est branché** (voir ci-dessous) |

Les refus, tels que Helm les affiche :

```text
config.storage.root is set by the chart: change persistence.storage.mountPath instead
config.state.root is set by the chart: change persistence.state.mountPath instead
config.server.addr is set by the chart: change containerPort instead
config.registries.credentialsFile is set by the chart while registryCredentials is enabled: configure registryCredentials.existingSecret or registryCredentials.dockerconfigjson instead
```

Deux autres refus protègent la forme de l'instance :

```text
config.mode is required: set "passthrough" or "mirror"
persistence.storage.mountPath and persistence.state.mountPath must differ: the instance state never travels on the transportable store
```

`registries.credentialsFile` est le seul cas conditionnel. Le Secret
d'identifiants est considéré comme branché quand
`registryCredentials.existingSecret` est posé, ou quand
`registryCredentials.enabled` vaut vrai **et** que
`registryCredentials.dockerconfigjson` n'est pas vide. `enabled: true` seul ne
branche rien. Tant qu'il n'est pas branché, le chart ne gère aucun fichier de ce
genre : un `config.registries.credentialsFile` de votre fait — un fichier que
vous montez par `extraVolumes` — est conservé tel quel.

Coller la sortie de `tobby config dump` dans `config` fonctionne une fois ces
quatre clés retirées.

## Image et charge de travail

| Clé | Défaut | Effet |
|---|---|---|
| `image.repository` | `ghcr.io/tobby-fetch/tobby-fetch` | L'image de conteneur. |
| `image.tag` | `""` | Tag de l'image. Vide signifie `v` suivi de l'`appVersion` du chart : la version du chart **est** la version de Tobby. |
| `image.digest` | `""` | Quand il est posé, la référence d'image est `repository@digest` et le tag est ignoré. Le digest est ce sur quoi portent la signature et la provenance de la version : épinglez-le en production. |
| `image.pullPolicy` | `IfNotPresent` | Politique de tirage Kubernetes. |
| `imagePullSecrets` | `[]` | Secrets que le kubelet utilise pour tirer l'image de Tobby. Sans rapport avec `registryCredentials`, qui sert à Tobby pour joindre les registres entre lesquels il déplace du contenu. |
| `nameOverride`, `fullnameOverride` | `""` | Renomment le chart ou les ressources qu'il crée. Les ressources s'appellent `<release>-tobby` (ou le seul nom de la release quand il contient déjà `tobby`), avec les suffixes `-config`, `-store`, `-state`, `-registry-credentials`, `-alert-rules`, `-dashboard`. |
| `containerPort` | `8080` | Le port d'écoute du processus. Le chart en dérive `server.addr`. |
| `extraArgs` | `[]` | Options ajoutées après `serve`. |
| `env` | `[]` | Variables d'environnement du conteneur (`name`/`value`, ou `valueFrom`). |
| `envFrom` | `[]` | Des ensembles entiers de variables venant de ConfigMaps ou de Secrets. Le moyen de fournir `TOBBY_NETWORK_PROXY_PASSWORD` sans l'écrire dans un fichier de valeurs. |
| `terminationGracePeriodSeconds` | `60` | Délai que le kubelet attend après SIGTERM. À garder au-dessus de `config.shutdown.gracePeriod`. |
| `podAnnotations`, `podLabels` | `{}` | Ajoutés au pod. |
| `nodeSelector`, `tolerations`, `affinity`, `topologySpreadConstraints` | `{}` / `[]` | Transmis au pod tels quels. |
| `priorityClassName`, `runtimeClassName`, `dnsPolicy` | `""` | Posés sur le pod quand ils ne sont pas vides. |
| `dnsConfig`, `hostAliases` | `{}` / `[]` | Posés sur le pod quand ils ne sont pas vides. |
| `extraVolumes`, `extraVolumeMounts` | `[]` | Volumes et montages supplémentaires : un bundle d'autorités privées, des racines de confiance, une surcharge de thème, une configuration de proxy. Montez-les en lecture seule. |
| `extraContainers`, `initContainers` | `[]` | Conteneurs annexes (un expéditeur de journaux, par exemple) et conteneurs d'initialisation. |

Le déploiement est **un seul réplica en stratégie `Recreate`**, et aucun des
deux n'est une valeur : le store demande un seul écrivain, donc l'ancien pod
libère les volumes avant que le nouveau démarre. Un changement de la
configuration rendue redémarre le pod (son empreinte est une annotation du pod).
Voir [Mettre à jour et revenir en arrière](../upgrade-rollback/#chart-helm).

## Clés de configuration documentées par le chart

Toutes se trouvent sous `config`. Les défauts sont ceux du chart ; la
[référence de configuration](../../reference/configuration/) est la liste
complète, avec les variables d'environnement et la signification.

| Clé | Défaut | Effet |
|---|---|---|
| `config.mode` | `passthrough` | `passthrough` est un service durable entre deux zones connectées ; `mirror` pilote un store transportable. Une valeur vide est refusée. Posez-la explicitement. |
| `config.logging.level` | `info` | `debug`, `info`, `warn` ou `error`. |
| `config.auth.disabled` | `false` | `true` ouvre toutes les surfaces et affiche une bannière d'avertissement permanente dans l'interface. Ne le laissez jamais activé. |
| `config.auth.sessionTTL` | `12h` | Durée de vie d'une session d'interface. Les sessions vivent en mémoire : un redémarrage déconnecte tout le monde. |
| `config.shutdown.gracePeriod` | `30s` | Délai laissé aux transferts en vol pour finir ou poser un point de reprise après SIGTERM. |
| `config.sync.parallelism` | `3` | Transferts d'ingrédients simultanés. |
| `config.sync.retries` | `3` | Nouvelles tentatives par ingrédient sur échec transitoire. |
| `config.sync.interval` | `15m` | Fréquence à laquelle une instance passthrough relit le Retriever et réconcilie. `0` arrête la boucle ; le déclenchement manuel fonctionne toujours. La surcharge d'un administrateur à l'exécution est écrite dans le répertoire d'état : une mise à jour du chart ne la reprend pas. Ignoré en mode mirror. |
| `config.transfer.resumeThreshold` | `64MiB` | Taille de blob à partir de laquelle un téléchargement reprend à l'intérieur du blob. Un tel blob transite par le volume d'état : il y coûte sa propre taille. `0` désactive. |
| `config.retriever.source` | `""` | D'où est récupéré le document d'état désiré : une URL HTTP(S), une référence OCI, ou un chemin dans le conteneur. |
| `config.destination.registry` | `""` | Le registre de zone dans lequel cette instance promeut, en hôte nu. Vide : rien n'est promu. |
| `config.destination.basePath` | *(non posé)* | Préfixe de chemin facultatif sous lequel les ingrédients relocalisés atterrissent à la destination. |
| `config.destination.cookbook` | `cookbook` | Dépôt sous lequel les recettes sont republiées avec leurs signatures. |
| `config.network` | `{}` | Proxy (`proxy.url`, `httpsURL`, `noProxy`, `username`) et confiance supplémentaire (`tls.caFiles`, `tls.exclusiveTrust`). Voir [Réseau d'entreprise](../network/). Aucune clé ne permet de désactiver la vérification des certificats. |
| `config.server.secureCookies` | *(non posé)* | À `true` quand le TLS se termine sur un ingress ou un maillage, pour que le cookie de session soit marqué `Secure`. |
| `config.server.tls.*` | *(non posé)* | Le certificat que présente le listener. Voir la limite [plus bas](#tls-sur-le-listener-et-les-sondes). |
| `config.registries.insecure` | *(non posé)* | Hôtes joignables en HTTP clair, un par un. |
| `config.registries.substitutions` | *(non posé)* | Récupérer un registre nominal depuis un point d'accès de substitution. Voir [la cascade](../retriever-cascade/). |
| `config.ui.showUpcoming` | `false` | Rend les entrées de navigation des fonctions futures comme des emplacements inertes. Désactivé en production. |
| `config.ui.themeOverride` | `""` | Chemin d'une feuille de style d'opérateur. Montez-la par `extraVolumes`. |
| `config.trust` | `{}` | Racines de confiance et périmètres. |
| `config.files` | `{}` | FileSets servis sous `/files/<nom>/`. Désactivé tant qu'il n'est pas listé. |

## Identifiants de registre

Les identifiants de registre sont un Secret `kubernetes.io/dockerconfigjson`
séparé, monté en lecture seule dans `/etc/tobby-registries`. Ils n'entrent
jamais dans le fichier de configuration ni dans le store. Les entrées sont
cherchées par l'hôte réellement contacté.

| Clé | Défaut | Effet |
|---|---|---|
| `registryCredentials.enabled` | `false` | Avec `dockerconfigjson` posé, branche une charge utile en ligne. Seul, ne branche rien. |
| `registryCredentials.existingSecret` | `""` | Nom d'un Secret que vous gérez, de type `kubernetes.io/dockerconfigjson`. Prime sur `dockerconfigjson`, et branche les identifiants à lui seul. Le chart ne crée rien. |
| `registryCredentials.dockerconfigjson` | `""` | Une charge utile en ligne, rendue dans un Secret géré par le chart. Acceptable pour un labo : Helm conserve les valeurs rendues dans le Secret de la release, préférez `existingSecret`. |

Une fois branché, le chart pose lui-même `registries.credentialsFile` à
`/etc/tobby-registries/.dockerconfigjson`.

## Persistance

Le store et l'état ne partagent jamais un volume, ni un chemin de montage (le
chart refuse deux chemins égaux ; Tobby refuse de démarrer quand une racine est
dans l'autre). Voir [Déployer](../deploy/#store-et-état-ne-sont-pas-le-même-volume).

| Clé | Défaut | Effet |
|---|---|---|
| `persistence.storage.enabled` | `true` | `false` retombe sur un `emptyDir` : tout ce qui a été récupéré est perdu au remplacement du pod. |
| `persistence.storage.mountPath` | `/var/lib/tobby/store` | Où le store est monté. Devient `storage.root`. |
| `persistence.storage.existingClaim` | `""` | Utilise un claim qui vous appartient. Le chart n'en crée alors aucun. |
| `persistence.storage.accessModes` | `[ReadWriteOnce]` | Modes d'accès du claim créé par le chart. |
| `persistence.storage.size` | `100Gi` | Taille demandée. |
| `persistence.storage.storageClassName` | `""` | Vide : la classe par défaut du cluster. Toute autre valeur est écrite telle quelle dans le claim. |
| `persistence.storage.annotations` | `{}` | Ajoutées au claim. |
| `persistence.state.enabled` | `true` | `false` retombe sur un `emptyDir`, qui perd tous les comptes au remplacement du pod. Défendable seulement dans un environnement jetable. |
| `persistence.state.mountPath` | `/var/lib/tobby/state` | Devient `state.root`. |
| `persistence.state.existingClaim` | `""` | Utilise un claim qui vous appartient. |
| `persistence.state.accessModes` | `[ReadWriteOnce]` | Comme ci-dessus. |
| `persistence.state.size` | `20Gi` | Taille demandée. Contient les comptes et les jetons, et les téléchargements reprenables en cours. |
| `persistence.state.storageClassName` | `""` | Comme ci-dessus. |
| `persistence.state.annotations` | `{}` | Ajoutées au claim. |
| `tmpSizeLimit` | `1Gi` | Limite de taille du `/tmp` inscriptible, un `emptyDir` sur le disque du nœud (pas en mémoire). Le système de fichiers racine est en lecture seule : c'est le seul espace de travail. |

Les deux claims que crée le chart portent `helm.sh/resource-policy: keep` :
`helm uninstall` les laisse. Le claim d'état est celui à sauvegarder ; voir
[Sauvegarder et reconstruire une instance](../backup-rebuild/).

:::note[La classe de stockage `-`]
Les commentaires de `values.yaml` décrivent `"-"` comme désactivant
l'approvisionnement dynamique. Le template ne le traite pas à part : la valeur
est écrite telle quelle dans le claim, et Kubernetes cherche donc une classe
nommée `-`. Laissez la clé vide pour utiliser la classe par défaut.
:::

## Service et ingress

Un seul listener porte l'interface, l'API, le registre embarqué (`/v2/`), les
sondes et `/metrics` : un seul port suffit.

| Clé | Défaut | Effet |
|---|---|---|
| `service.type` | `ClusterIP` | Type du Service. |
| `service.port` | `8080` | Port du Service ; il cible le port du conteneur. L'Ingress l'utilise aussi. |
| `service.nodePort` | `""` | Pris en compte seulement pour `NodePort` et `LoadBalancer`. |
| `service.annotations`, `service.labels` | `{}` | Ajoutés au Service. |
| `ingress.enabled` | `false` | Rend un Ingress. Il expose l'interface, l'API **et** le registre. |
| `ingress.className` | `""` | `ingressClassName`, quand il est posé. |
| `ingress.annotations` | `{}` | Pour un registre derrière un ingress, relevez ici les limites de taille de corps et de délai du contrôleur (pour ingress-nginx : `proxy-body-size: "0"` et un `proxy-read-timeout` long), sinon les gros envois échouent d'une façon qui ressemble à des pannes réseau. |
| `ingress.hosts` | un hôte, `tobby.example.com`, chemin `/`, `Prefix` | Liste d'hôtes, chacun avec des `paths` (`path`, `pathType`, `Prefix` par défaut). |
| `ingress.tls` | `[]` | Transmis à l'Ingress tel quel. Quand le TLS se termine ici, posez aussi `config.server.secureCookies: true`. |

## Contexte de sécurité et compte de service

| Clé | Défaut | Effet |
|---|---|---|
| `serviceAccount.create` | `true` | Crée un ServiceAccount. |
| `serviceAccount.name` | `""` | Nom à utiliser ; vide, il est dérivé de la release. |
| `serviceAccount.annotations` | `{}` | Ajoutées au ServiceAccount. |
| `serviceAccount.automountServiceAccountToken` | `false` | Aucun jeton n'est projeté : Tobby n'appelle jamais l'API Kubernetes, et aucun RBAC n'est attaché. |
| `podSecurityContext` | non-root, uid/gid `65532`, `fsGroup: 65532`, `fsGroupChangePolicy: OnRootMismatch`, `seccompProfile: RuntimeDefault` | Niveau pod. `fsGroup` rend les volumes inscriptibles par l'utilisateur de l'image ; le contrôle du démarrage tolère l'accès de groupe qui en résulte (voir [sauvegarde](../backup-rebuild/#ce-que-le-démarrage-contrôle)). |
| `securityContext` | non-root, uid/gid `65532`, pas d'élévation de privilèges, `privileged: false`, `readOnlyRootFilesystem: true`, toutes capabilities retirées, `seccompProfile: RuntimeDefault` | Niveau conteneur. Satisfait tel quel le standard de sécurité de pod *restricted*. |

Helm fusionne les maps avec les défauts : `--set podSecurityContext.runAsUser=1000`
ne change qu'un champ. Une liste est remplacée en entier : poser
`securityContext.capabilities.drop` ne retire que ce que vous listez, pas `ALL`.

## Sondes

`/healthz` répond dès que le listener est up. `/readyz` répond `503` tant que le
store et la configuration ne sont pas utilisables, et de nouveau pendant le
drain d'arrêt. Les sondes sont des `httpGet` sur le port du conteneur.

| Clé | Démarrage | Vivacité | Disponibilité |
|---|---|---|---|
| `probes.<nom>.enabled` | `true` | `true` | `true` |
| `probes.<nom>.initialDelaySeconds` | `0` | `0` | `0` |
| `probes.<nom>.periodSeconds` | `5` | `20` | `10` |
| `probes.<nom>.timeoutSeconds` | `3` | `3` | `3` |
| `probes.<nom>.failureThreshold` | `30` | `3` | `3` |
| `probes.readiness.successThreshold` | | | `1` |
| Chemin | `/healthz` | `/healthz` | `/readyz` |

Les noms sont `startup`, `liveness` et `readiness`. La sonde de démarrage laisse
30 × 5 s = 150 secondes pour ouvrir un gros store avant que le pod soit déclaré
mort. Relevez `probes.startup.failureThreshold` pour un store plus long à ouvrir.

### TLS sur le listener et les sondes

Si vous donnez au listener son propre certificat (`config.server.tls`), il ne
sert **que** du TLS. Les sondes du chart et son ServiceMonitor parlent HTTP en
clair, et le chart n'a aucune valeur pour changer cela : le listener leur répond
`400 Bad Request` (« Client sent an HTTP request to an HTTPS server ») et les
sondes échouent. Le chemin pour lequel le chart est écrit est un TLS terminé sur
un ingress ou un maillage, avec `config.server.secureCookies: true`. Si vous
servez le TLS depuis le pod, passez `probes.<nom>.enabled` à `false` et
renoncez au ServiceMonitor, ou maintenez vos propres sondes dans une copie
modifiée du chart.

## Métriques, alertes et tableau de bord

`/metrics` est toujours servi sur le listener, sans authentification comme les
sondes. Tout ce qui suit est **désactivé** par défaut : le chart s'installe sur
un cluster qui n'a ni le Prometheus Operator ni Grafana. Les alertes elles-mêmes,
et le sens des seuils, sont sur [Superviser une instance](../monitoring/).

| Clé | Défaut | Effet |
|---|---|---|
| `metrics.podAnnotations` | `false` | `true` ajoute `prometheus.io/scrape`, `prometheus.io/port` et `prometheus.io/path` au pod, pour la découverte par annotations. |
| `metrics.serviceMonitor.enabled` | `false` | Rend un ServiceMonitor. Demande les CRD du Prometheus Operator. Il collecte `/metrics` en `http`. |
| `metrics.serviceMonitor.namespace` | `""` | Namespace du ServiceMonitor, quand ce n'est pas celui de la release. |
| `metrics.serviceMonitor.interval` | `30s` | Intervalle de collecte. |
| `metrics.serviceMonitor.scrapeTimeout` | `10s` | Délai de collecte. |
| `metrics.serviceMonitor.labels` | `{}` | Labels par lesquels votre Prometheus sélectionne les ServiceMonitors. |
| `metrics.serviceMonitor.basicAuth` | `{}` | Seulement pour un proxy d'authentification devant l'instance ; `/metrics` lui-même ne demande rien. |
| `metrics.serviceMonitor.relabelings`, `metricRelabelings` | `[]` | Transmis au point d'accès tels quels. |
| `alerting.prometheusRule.enabled` | `false` | Rend une `PrometheusRule` de dix alertes. Demande les CRD de l'Operator. |
| `alerting.prometheusRule.namespace` | `""` | Namespace de la règle, quand ce n'est pas celui de la release. |
| `alerting.prometheusRule.labels` | `{}` | Labels que demande le `ruleSelector` de l'Operator (par exemple `release: prometheus`). |
| `alerting.rulesFile.enabled` | `false` | Les mêmes règles en fichier Prometheus simple, dans un ConfigMap, clé `tobby-alerts.rules.yaml`, pour un Prometheus qui n'est pas géré par l'Operator. |
| `alerting.extraLabels` | `{}` | Labels ajoutés à chaque alerte. |
| `alerting.thresholds.syncFailures` | `3` | Exécutions en échec, dans la fenêtre ci-dessous, qui déclenchent `TobbySyncFailing`. |
| `alerting.thresholds.syncFailuresWindow` | `1h` | Fenêtre de la règle précédente ; c'est aussi le `for` de `TobbySyncNeverSucceeded`. |
| `alerting.thresholds.syncMaxAgeSeconds` | `21600` | Âge de la dernière synchronisation réussie, en secondes, au-delà duquel `TobbySyncStale` se déclenche. `0` retire `TobbySyncStale` **et** `TobbySyncNeverSucceeded` : il reste huit alertes. Pour les instances miroir. |
| `alerting.thresholds.saturationFor` | `15m` | Durée pendant laquelle les transferts restent au plafond de `sync.parallelism`. |
| `alerting.thresholds.storeOccupancyFor` | `15m` | Durée pendant laquelle le store reste au-delà de `storage.occupancyThreshold`. |
| `alerting.thresholds.scannerFor` | `1h` | Durée pendant laquelle la base de vulnérabilités ou l'analyseur reste dégradé. |
| `alerting.thresholds.certificateExpirySeconds` | `1209600` | Validité restante, en secondes (14 jours), sous laquelle le certificat du listener alerte. |
| `alerting.thresholds.certificateExpiryFor` | `10m` | Le `for` de l'alerte de certificat. |
| `alerting.thresholds.clockFor` | `5m` | Durée pendant laquelle l'horloge reste invraisemblable. |
| `dashboard.enabled` | `false` | Rend un ConfigMap portant un tableau de bord Grafana (`tobby-dashboard.json`). |
| `dashboard.namespace` | `""` | Namespace du ConfigMap, quand ce n'est pas celui de la release. |
| `dashboard.labels` | `grafana_dashboard: "1"` | Label que surveille le sidecar Grafana. |

`TobbyStoreOccupancyHigh` n'a de quoi comparer que si vous posez
`storage.occupancyThreshold` dans `config`.

## Dimensionnement

Le dépôt énonce peu de chiffres, et n'en mesure aucun parmi les suivants. Ce
qu'il énonce :

| Ressource | Défaut du chart | Ce que dit le chart |
|---|---|---|
| CPU | requête `100m`, **pas de limite** | Brider un transfert pour économiser des cycles dont personne d'autre ne veut est un mauvais arbitrage. |
| Mémoire | requête `256Mi`, limite `1Gi` | Un « point de départ, pas une recommandation » : le bon plafond dépend de la taille du store et du parallélisme de synchronisation, et une limite sous l'ensemble de travail fait tuer le pod par l'OOM en plein transfert. |
| Volume du store | `100Gi` | « Dimensionné pour le contenu que vous comptez porter. » Posez `storage.occupancyThreshold` pour que l'instance vous prévienne avant que le volume soit plein. |
| Volume d'état | `20Gi` | Les comptes et les jetons sont petits. Chaque blob égal ou supérieur à `transfer.resumeThreshold` (`64MiB`) y transite d'abord : prévoyez la place des plus grosses couches en vol en même temps, ou baissez le seuil. |
| `/tmp` | `1Gi` | Espace de travail sur le disque du nœud ; le seul chemin inscriptible hors des volumes. |
| Temps de démarrage | 150 s | Le budget de la sonde de démarrage. |

Les défauts de ressources sont une requête et une seule limite, posées dans
`resources` :

```yaml
resources:
  requests:
    cpu: 100m
    memory: 256Mi
  limits:
    memory: 1Gi
```

Aucun chiffre de mémoire ou de CPU mesuré sur une taille de store donnée,
aucune courbe selon la taille des blobs ou le parallélisme, et aucune
projection de taille de store n'est publié dans le dépôt ni sur ce site.
Considérez les défauts comme une première estimation et observez la consommation
réelle du pod pendant quelques semaines. Pour un miroir, `tobby sync --dry-run`
projette ce qu'un cycle va écrire
([dimensionner un cycle](../../air-gap/manage-media/#dimensionner-un-cycle)).

## Vérifier ce que vous avez posé

Rendez avant d'installer ; les refus ci-dessus apparaissent ici, pas sur le
cluster :

```sh
helm lint ./deploy/charts/tobby --values my-values.yaml
helm template tobby ./deploy/charts/tobby --values my-values.yaml
```

Depuis un clone du dépôt, `sh tools/chart-owned-settings.sh` rejoue sur le chart
les cas du tableau des clés possédées.
