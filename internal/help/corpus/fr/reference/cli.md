---
title: Référence de la CLI
description: Chaque commande tobby avec ses options et leurs valeurs par défaut, la table publiée des codes de sortie, le contrat --output json, le comportement non interactif et la langue des messages.
sidebar:
  order: 1
---

Cette page documente l'interface en ligne de commande telle que livrée dans la
série v0.5.x actuelle, extraite du binaire lui-même (`tobby <command> --help`)
et du code source de la CLI (`internal/cli/`).

La CLI porte un **contrat sur lequel une automatisation peut compter d'une
version à l'autre** (SRS FR-066, amendement R-08) :

- `--output json` sur toute commande qui rapporte quelque chose, le document
  machine étant seul sur la sortie standard et son schéma publié à côté du
  document OpenAPI ;
- une [table des codes de sortie](#codes-de-sortie) exhaustive et publiée,
  couverte par la promesse de versionnement sémantique du projet — retirer un
  code ou en renuméroter un est un changement cassant ;
- un [mode non interactif](#comportement-non-interactif) garanti : aucune
  commande n'invite l'utilisateur, aucune n'exige de terminal ;
- `--wait` sur toute commande qui démarre une tâche sur une instance.

Chacun de ces quatre points est tenu par un test qui parcourt l'arbre réel des
commandes, pas par cette page : `internal/cli/contract_test.go` et
`internal/taxonomy/exit_test.go`.

## Arbre des commandes

```
tobby
├── serve          Run the instance (HTTP listener, embedded registry)
├── quickstart     Guided first start: answer a few questions, get a serving instance
├── sync           Trigger a synchronization on a running instance, or plan one (--dry-run)
├── export         Export the store to a standard OCI image layout
├── import         Import a standard OCI image layout into the store
├── config
│   └── dump       Print the effective configuration (secrets redacted)
├── consumption
│   ├── table      Print the source→destination mapping table
│   └── registries-yaml  Print the K3s/RKE2 registries.yaml mirror snippet
├── fileset
│   └── pack       Package a local directory as a FileSet in the store
├── media
│   ├── verify     Re-verify a transported store without pushing anything
│   └── import     Verify a transported store, then push what it cleared
├── store
│   └── verify     Verify the integrity of a whole store (either mode)
├── diagnostics
│   └── bundle     Write one redacted diagnostic archive for support
├── state
│   └── inspect    List the state directory: classes, sizes, permissions
├── scan
│   └── exceptions List the vulnerability exceptions with their origin, scope, expiry and state (R-15)
├── recipe
│   └── push       Validate a recipe and publish it to a cookbook
├── user
│   ├── add        Create a local account (the first account defaults to admin)
│   ├── list       List the local accounts
│   └── passwd     Change a local account's password
├── version        Print the Tobby version and build metadata
└── completion     Shell completion scripts (bash, zsh, fish, powershell)
```

Les descriptions de l'arbre sont celles que le binaire affiche, en anglais.
La CLI ne reflète volontairement pas tous les écrans de l'UI : elle couvre les
opérations utiles à l'automatisation (SRS FR-066). L'exigence de stricte parité
s'applique entre [l'UI et l'API](../../reference/api/).

## Options communes

Toute commande qui charge la configuration en couches accepte le même jeu
d'options. Une option est la couche de configuration la plus haute : elle
l'emporte sur la variable d'environnement et la clé de fichier correspondantes
(voir la [référence de configuration](../../reference/configuration/)).

| Option | Défaut | Rôle |
|---|---|---|
| `--config <path>` | `/etc/tobby/config.yaml` | Chemin du fichier de configuration YAML. L'emplacement par défaut est facultatif ; un chemin donné explicitement doit exister. |
| `--mode <m>` | *(aucun — requis pour servir)* | Mode de fonctionnement : `passthrough` ou `mirror`. |
| `--storage-root <dir>` | *(aucun)* | Répertoire du store autonome. |
| `--state-root <dir>` | *(aucun)* | Répertoire d'état de l'instance (comptes, tokens) — hors du store. |
| `--server-addr <addr>` | `:8080` | Adresse d'écoute HTTP. |
| `--log-level <l>` | `info` | `debug`, `info`, `warn` ou `error`. |
| `--shutdown-grace-period <d>` | `30s` | Délai accordé à l'arrêt gracieux pour vider les requêtes en cours. |
| `--proxy-url <url>` | *(aucun)* | Proxy sortant pour tout le trafic sortant. Le mot de passe du proxy n'est volontairement **pas** une option : les valeurs d'option sont visibles dans la table des processus. Il vient du fichier de configuration ou de `TOBBY_NETWORK_PROXY_PASSWORD`. |
| `--tls-cert-file <pem>` | *(aucun)* | Certificat PEM présenté par le listener (exige `--tls-key-file`). |
| `--tls-key-file <pem>` | *(aucun)* | Clé privée PEM de `--tls-cert-file`. |

La validation se fait commande par commande : une commande n'exige jamais un
réglage qu'elle n'utilise pas. `tobby user` n'a besoin que du répertoire
d'état ; `tobby recipe push` que des réglages tournés vers la registry ;
`tobby sync` sans `--dry-run` ne demande ni store ni mode, car il pilote une
instance au lieu de toucher au store. Aucune ne requiert de mode.

## Format de rapport

`--output` désigne le **format de rapport** sur toutes les commandes, et rien
d'autre. Il ne désigne jamais un chemin de destination — `tobby export <path>`
prend sa destination en argument positionnel, symétrique de
`tobby import <path>`.

| Valeur | Où | Signification |
|---|---|---|
| `text` | défaut partout sauf pour `config dump` | Le rapport pour humains. |
| `json` | toute commande qui rapporte | Le document machine, seul sur stdout. |
| `yaml` | `config dump` uniquement, et son défaut | La configuration sous forme de fichier que l'on peut réécrire. |

Deux règles valent pour toutes les commandes :

- **Le document machine est seul sur la sortie standard.** La narration pour
  humains, les journaux structurés, la progression et les enregistrements
  d'audit vont sur la sortie d'erreur, si bien que `tobby … --output json | jq`
  se compose et que `tobby config dump > config.yaml` écrit un fichier qui ne
  contient que la configuration.
- **Une valeur inconnue est une erreur d'usage** (code de sortie `2`), jamais
  un repli silencieux sur le texte.

Les documents sont décrits par un JSON Schema publié à côté du document OpenAPI
et servi par une instance en marche sur `GET /api/v1/cli-output.schema.json`.
Il porte une entrée par commande qui rapporte, indexée par le chemin de la
commande. Ajouter un membre à un document est additif ; en retirer un, ou
changer son type, est un changement cassant — même promesse que pour les codes
de sortie.

## Commandes

### tobby serve

```
tobby serve [flags]
```

Exécute l'instance : un listener HTTP unique portant l'UI web, l'API REST, la
registry OCI embarquée (`/v2/`), la surface FileSet (`/files/`), les sondes et
`/metrics`. N'utilise que les options communes.

Le démarrage échoue vite et est sécurisé par défaut :

- `storage.root` est requis, créé s'il manque, et son accès en écriture est
  sondé.
- Sauf si l'authentification est explicitement désactivée, `state.root` est
  requis et au moins un compte local doit exister — sinon l'instance refuse de
  démarrer avec [`TBY-AUTH-001`](../../reference/errors/#tby-auth-001).
- Une configuration invalide (URL de proxy erronée, paire TLS à moitié
  déclarée, périmètre de confiance mal formé…) refuse le démarrage avec un
  message actionnable plutôt que de dégrader en silence.
- Sur SIGTERM/SIGINT, la disponibilité passe d'abord à 503, puis les requêtes
  en cours disposent du délai de grâce pour se terminer.

Elle ne rapporte rien à la fin parce qu'elle ne se termine jamais : sa sortie
est le flux de journaux structurés, qui a [son propre schéma](../../reference/metrics-logs/).

### tobby quickstart

```
tobby quickstart [flags]
```

Premier démarrage interactif : demande le répertoire de stockage, le répertoire
d'état, le mode et le premier compte administrateur, écrit le fichier de
configuration, et démarre éventuellement le service.

| Option | Défaut | Rôle |
|---|---|---|
| `--config <path>` | `./tobby.yaml` | Destination du fichier de configuration écrit. |
| `--mode <m>` | *(demandé)* | Répond d'avance à la question du mode. |
| `--storage-root <dir>` | `./storage` | Répond d'avance à la question du store. |
| `--state-root <dir>` | `./state` | Répond d'avance à la question de l'état. |
| `--password-stdin` | `false` | Lit le mot de passe administrateur sur une ligne de l'entrée standard. |
| `--serve` | `false` | Démarre l'instance juste après avoir écrit la configuration. |

Une option déjà donnée évite sa question. Les invites vont sur stderr ; stdout
reste réservé aux machines. Quickstart n'écrase jamais un fichier de
configuration existant. C'est une aide, jamais une obligation : sans terminal
et sans réponses, il refuse en fournissant les commandes équivalentes pilotées
par options.

### tobby sync

```
tobby sync [--wait] [--prune] [flags]              # déclenche, pilote une instance
tobby sync --dry-run [--retriever <src>] [flags]   # planifie, ne pilote rien
```

**Sans `--dry-run`, cette commande pilote une instance en marche.** Elle appelle
`POST /api/v1/sync` — le point d'accès derrière le bouton *Synchroniser* — et
rapporte la tâche que l'instance a créée. Elle ne synchronise pas par
elle-même et n'ouvre jamais le store : l'instance qui sert ce store le tient
ouvert en écriture, et un second écrivain est exactement ce que le format du
store interdit.

| Option | Défaut | Rôle |
|---|---|---|
| `--instance <url>` | `TOBBY_INSTANCE_URL`, puis l'adresse d'écoute configurée sur localhost | URL de base de l'instance à piloter, par ex. `https://tobby.example:8443`. |
| `--token-file <path>` | `TOBBY_API_TOKEN` à défaut | Fichier contenant le token d'API statique (rôle operator). Le token n'est volontairement **pas** une option : les valeurs d'option sont visibles dans la table des processus. |
| `--request-timeout <d>` | `30s` | Budget par requête auprès de l'instance. Il borne une requête, jamais l'attente. |
| `--allow-plaintext-token` | `false` | Envoie le token d'API à une instance `http://` qui **n'est pas sur cet hôte**. Refusé par défaut : le token traverserait le réseau en clair ; utilisez `https://`, ou `http://` en boucle locale (`localhost`, `127.0.0.1`, `[::1]`). Avec l'option, un avertissement le signale à chaque exécution (FR-075). |
| `--wait` | `false` | Bloque jusqu'à ce que la tâche atteigne un état terminal, et sort sur le résultat propre de la tâche. |
| `--wait-timeout <d>` | *(aucun)* | Cesse d'attendre après ce délai. Cesser d'attendre le dit et nomme la tâche — la synchronisation continue sur l'instance. |
| `--prune` | *(le réglage propre de l'instance)* | `--prune` supprime le contenu qu'aucune recipe n'atteint plus ; `--prune=false` l'interdit pour cette exécution. L'absence signifie « faire ce que cette instance fait par défaut ». |

**Avec `--dry-run`, rien n'est contacté et rien n'est écrit.** Le plan
s'exécute localement contre le répertoire du store et rapporte ce qu'une
synchronisation ferait : les versions résolues de chaque recipe, le statut par
digest de chaque ingrédient, le volume dédupliqué à transférer, la taille
projetée du store face à l'espace libre et aux capacités du système de fichiers
de la cible, le contenu qu'un prune supprimerait, et les verdicts de politique
qui n'exigent aucun transfert — la liste blanche de registries et les
signatures propres des recipes. La cadence de réconciliation automatique d'une
instance passthrough reste exactement où elle était.

| Option | Rôle |
|---|---|
| `--dry-run` | Mode plan. |
| `--retriever <src>` | Planifie un Retriever candidat au lieu de celui configuré (chemin de fichier, URL HTTP(S) ou référence OCI). |
| `--skip-destination` | Ne contacte pas la destination de promotion. |

Mélanger les deux jeux d'options est une erreur d'usage plutôt qu'une
non-opération silencieuse : un pipeline qui a écrit
`tobby sync --dry-run --wait` apprend qu'il a attendu pour rien.

```sh
tobby sync --wait                                    # déclenche et bloque
TOBBY_API_TOKEN=… tobby sync --instance https://tobby.example:8443 --wait --output json
tobby sync --dry-run --retriever ./retriever.yaml --output json
```

Codes de sortie : `0` la synchronisation a réussi, ou le plan n'a rien trouvé
à faire ; `5` des changements sont prévus (`--dry-run` uniquement) ; `3` refusé
par la politique ; `4` la vérification a échoué ; `1` l'exécution ou le plan n'a
pas pu aboutir.

### tobby export

```
tobby export <path> [flags]
```

Écrit le store local — ou une sélection de celui-ci — sous forme de standard
OCI image layout, lisible par `skopeo`, `oras` et `crane` (SRS FR-051,
ADR-0006). C'est la voie de sortie vers l'interopérabilité : le contenu
appartient à celui qui l'a stocké et doit pouvoir être récupéré sans Tobby.

Le layout est par défaut un unique tar non compressé — un seul fichier
franchit plus sûrement un air-gap qu'une arborescence — ou un répertoire avec
`--directory`.

| Option | Défaut | Rôle |
|---|---|---|
| `--format <f>` | `oci-layout` | Format d'interopérabilité. Un seul existe aujourd'hui ; l'option existe pour qu'un second soit additif. |
| `--directory` | `false` | Écrit le layout sous forme de répertoire plutôt que d'un tar unique. |
| `--overwrite` | `false` | Remplace la destination si elle existe déjà. Sans cette option, une destination existante donne [`TBY-LAY-003`](../../reference/errors/#tby-lay-003). |
| `--dry-run` | `false` | Rapporte ce que contiendrait l'export et sa taille, sans rien écrire. |
| `--recipe <sel>` | *(tout)* | Exporte une recipe et tout ce qu'elle gère (`name` ou `name@version`) ; répétable. |
| `--repository <repo>` | *(tout)* | Exporte un dépôt relocalisé ; répétable. |

Une sélection de recipe emporte ses ingrédients, l'artefact de la recipe, et
les artefacts de signature cosign des deux, dans l'un ou l'autre des layouts
que cosign publie — les signatures voyagent avec le contenu qu'elles attestent.

Exécutez-la contre une instance arrêtée, ou utilisez
`POST /api/v1/oci-layout/export` sur une instance en marche : deux processus
qui écrivent dans un même répertoire de stockage, c'est un processus de trop.

```sh
tobby export --storage-root /var/lib/tobby /media/usb/payload.tar
tobby export --storage-root /var/lib/tobby /media/usb/payload.tar --dry-run --output json
```

### tobby import

```
tobby import <path> [flags]
```

Restaure un standard OCI image layout — un répertoire, ou un tar non compressé
de celui-ci — dans le store local, à digests identiques.

Le layout est une donnée non fiable : chaque manifeste n'est accepté que si
ses octets hachent vers le digest qui l'adresse, chaque blob est validé contre
le digest que son manifeste épingle, et une archive portant autre chose que
des fichiers `oci-layout`, `index.json` et `blobs/<algorithm>/<digest>` est
refusée avant d'être lue ([`TBY-LAY-002`](../../reference/errors/#tby-lay-002)).
Les archives compressées sont refusées aussi : décompressez d'abord.

| Option | Rôle |
|---|---|
| `--format <f>` | Format d'interopérabilité (`oci-layout`). |
| `--repository <repo>` | Dépôt auquel appartiennent les entrées, pour les layouts qui ne nomment qu'un tag — ce que produit `skopeo copy`. |

Les entrées sont indépendantes : une image qui n'a pas survécu au support
échoue sur sa propre ligne et le reste est tout de même importé. Un import qui
a perdu des entrées sort avec `1` et les nomme.

Ce qui arrive est enregistré comme un import unitaire d'origine
`oci-layout` : du contenu qu'aucune recipe ne gère, jamais purgé, retirable
individuellement. La commande respecte `hardening.disableUnitImport`, lu
dans le même fichier de configuration que l'instance (`--config`, ou le
chemin par défaut s'il existe) : sur une instance durcie, elle refuse avec
[TBY-POL-005](../errors/#tby-pol-005), n'écrit rien, consigne un événement
d'audit `hardening.refused` sur stderr et sort avec le code de politique
`3`.

### tobby config dump

```
tobby config dump [--output yaml|json] [flags]
```

Affiche la configuration effective — toutes les couches fusionnées, **secrets
masqués par construction** — sur stdout, de sorte que
`tobby config dump > config.yaml` capture exactement ce avec quoi l'instance
tournerait. C'est l'outil de contrôle que désigne l'action corrective de
[`TBY-CFG-001`](../../reference/errors/#tby-cfg-001). Détails dans la
[référence de configuration](../../reference/configuration/).

`--output json` ré-encode le même document masqué pour les appelants qui
préfèrent ne pas embarquer d'analyseur YAML.

### tobby consumption table

```
tobby consumption table [--recipe <nom>[@<version>]]... [--destination <hôte[:port]>] [--output markdown|json|csv]
```

Affiche la table de correspondance source→destination (SRS FR-065) : pour
chaque ingrédient des recipes retenues — FileSets compris — la référence que
nomme la recipe, la référence dans la registry de destination et le digest
épinglé. Elle lit le graphe de recipes du répertoire du store : elle
fonctionne donc sur un hôte où aucune instance ne tourne, et correspond à ce
qui a été poussé.

`--recipe` est répétable (`nom` retient toutes les versions enregistrées,
`nom@version` l'une d'elles ; aucune retient tout le store). La registry est
`--destination`, sinon `consumption.registry`, sinon `destination.registry` ;
sans aucune, la commande échoue avec `TBY-AID-001`. `--output` choisit le
format — `markdown` (par défaut), `json` ou `csv` — et les trois portent les
mêmes lignes. Voir les [aides à la consommation](../../passthrough/consumption-aids/).

### tobby consumption registries-yaml

```
tobby consumption registries-yaml [--recipe <nom>[@<version>]]... [--destination <hôte[:port]>]
```

Affiche sur stdout, en un seul document YAML, le `registries.yaml` qui fait
tirer à K3s et RKE2 les images des recipes retenues depuis la registry de la
zone : un mirror par hôte de registry source, avec la registry de la zone pour
endpoint et une réécriture vers le chemin relocalisé. `tobby consumption
registries` en est un alias. Aucun identifiant n'est jamais écrit ; la sortie
est déterministe et sans horodatage. C'est la seule commande qui ne doit pas
`--output json` : le fichier qu'elle affiche est fait pour être installé tel
quel sur les nœuds.

### tobby fileset pack

```
tobby fileset pack <directory> <name>:<version> [flags]
```

Empaquette un répertoire local comme un FileSet — une image OCI standard dont
la couche est l'arborescence du répertoire — et l'importe dans le store de cet
hôte, épinglé par son digest (SRS FR-048). C'est la voie sanctionnée pour
servir une poignée de fichiers locaux dans une zone isolée : aucun point
d'envoi n'est ouvert, le contenu est adressé par digest, inventorié, analysable
et supprimable comme tout le reste du store.

L'empaquetage est reproductible : le même répertoire produit toujours le même
digest. Un FileSet empaqueté est **non signé** et enregistré comme import
manuel d'origine locale — Tobby ne détient aucune clé de signature (ADR-0007) —
et les listings le disent. Le servir est une étape distincte et explicite : la
commande affiche le bloc de configuration à ajouter.

En mode texte, le digest seul est sur stdout, pour que la commande se compose
dans un script ; le reste du rapport est sur stderr.

La commande respecte `hardening.disableUnitImport`, lu dans le même fichier de
configuration que l'instance (`--config`, ou le chemin par défaut s'il
existe) : sur une instance durcie, elle refuse avec
[TBY-POL-005](../errors/#tby-pol-005), n'écrit rien, consigne un événement
d'audit `hardening.refused` sur stderr et sort avec le code politique `3`. La
lancer à côté d'une instance ne contourne pas l'interrupteur de celle-ci.

### tobby media verify

```
tobby media verify [flags]
```

Re-vérifie un store arrivé sur un support physique et rapporte, sans rien
pousser et **sans écrire dans le store** — pas même dans le journal
d'opérations propre au support.

Le support n'est pas fiable tant qu'il n'a pas fait ses preuves, l'ordre est
donc fixé par le SRS FR-054 : d'abord la complétude et les sommes de contrôle
du manifeste, puis les signatures des recipes contre les trust roots de
**cette** instance, puis chaque ingrédient contre son digest épinglé. Les
trust roots présentes sur le support sont ignorées.

Le rapport nomme, pour chaque refus, à la fois pourquoi et **quel fichier**.
Une recipe dont la signature se vérifie et dont chaque fichier atteignable
correspond à son digest épinglé est poussable ; toute autre est bloquée
entièrement, sans levée possible, et ses voisines sur le même support ne sont
pas touchées. Un manifeste absent ou illisible et un graphe de recipes altéré
bloquent le support dans son ensemble, sans levée possible ; un support adressé
à une autre zone ou plus ancien que le dernier importé ici le bloquent aussi,
et ces deux-là, un administrateur peut les lever.

| Option | Rôle |
|---|---|
| `--zone` | La zone que sert cette instance. Requise — par l'option, `TOBBY_ZONE`, ou `zone:` dans le fichier de configuration. |
| `--output text\|json` | `json` émet le rapport de vérification lui-même sur stdout, le même document que renvoie l'API. |
| `--allow-zone-mismatch` | Poursuit sur un support adressé à une autre zone. Sur `verify`, cela ne fait que prévisualiser ce que ferait un import levé. |
| `--allow-stale` | Poursuit sur un support plus ancien que le dernier importé pour cette zone. |

```sh
tobby media verify --storage-root /mnt/usb --zone production
tobby media verify --storage-root /mnt/usb --zone production --output json | jq .verdict
```

Codes de sortie : `0` chaque livraison est poussable, `3` refusé par la
politique (identité de zone, fraîcheur), `4` un échec de vérification.

### tobby media slip

```
tobby media slip [flags]
```

Établit le [bordereau de transfert](../../air-gap/transfer-slip/) (R-07) : le
compte rendu imprimable et bilingue d'un transfert physique — le support, sa
fraîcheur et son inventaire, le rapport de vérification, les rapports d'analyse
de vulnérabilités par recipe et par élément, et les marqueurs d'assurance
réduite.

Avec une identité de zone, la commande est le côté destination : elle vérifie
d'abord le support, comme le fait `media verify` et sans rien écrire, de sorte
que le bordereau porte le verdict. Sans identité, elle est le côté source. Un
store sans manifeste obtient quand même un bordereau, qui nomme le refus. Le
bordereau est **non signé** et le dit en tête et en pied : c'est une aide, pas
une preuve d'authenticité, et Tobby ne signe rien.

| Option | Rôle |
|---|---|
| `--output text\|html\|json` | `text` (défaut) est le document en texte brut, `html` une page autonome avec règles d'impression en ligne qui ne charge rien, `json` le document indépendant de la langue que sert l'API. |
| `--lang en\|fr` | Langue du bordereau. Défaut : `LC_ALL` / `LC_MESSAGES` / `LANG`, anglais sinon. |
| `--zone` | La zone que sert cette instance ; facultative ici. |
| `--allow-zone-mismatch`, `--allow-stale` | Comme sur `media verify` : prévisualisent ce que ferait un import levé. |

```sh
tobby media slip --storage-root /mnt/usb --zone production --lang fr
tobby media slip --storage-root /mnt/usb --zone production --output html > slip.html
```

La commande ne lit pas le scanner : sa section d'analyse le dit, et les
rapports par élément se suffisent à eux-mêmes. Codes de sortie : `0` quel que
soit le verdict — un support bloqué est un bordereau réussi d'un mauvais
support — et `4` quand la vérification elle-même n'a pas pu s'exécuter.

### tobby media import

```
tobby media import [flags]
```

Vérifie un store transporté et pousse ce que la vérification a validé vers la
registry de la zone (SRS FR-052). L'ordre n'est pas une suite d'étapes, c'est
la garantie : rien n'est poussé, servi ou écrit avant que le support entier ait
été re-vérifié.

Ce qui traverse passe ensuite par les mêmes contrôles qu'une promotion
passthrough — la liste blanche de registries et les signatures des recipes,
re-vérifiées sur les octets exacts sur le point de partir — seul ce qui manque
à la destination bouge, et les recipes signées arrivent dans le cookbook propre
à la zone avec leurs signatures. Le contenu que porte le support et qu'aucune
recipe vérifiée n'atteint est rapporté et jamais poussé.

L'opération se journalise sur le support, sous `_tobby/logs/`, hors de la
couverture du manifeste : le canal d'audit de retour du transfert. Un import
terminé fait avancer l'enregistrement de fraîcheur par zone, ce qui fait que
ré-importer le support du mois dernier est un refus plutôt qu'un retour en
arrière silencieux.

Elle prend les mêmes options que `media verify`, plus un `destination.registry`
configuré — vérifier n'en demande aucun, importer ne peut pas avancer sans. Lever
un garde-fou est un acte d'administrateur et est consigné dans le journal
d'audit.

Elle ne porte pas de `--wait` : la tâche s'exécute dans ce processus, la
commande est donc déjà bloquante. Sous `--output json`, son rapport est la
tâche elle-même, dans la forme que sert `GET /api/v1/tasks/{id}`.

```sh
tobby media import --config /etc/tobby/config.yaml
```

Codes de sortie : `0` importé, `1` un push a échoué, `3` refusé par la
politique, `4` un échec de vérification.

### tobby store verify

```
tobby store verify [flags]
```

Vérifie un store entier à la demande (SRS FR-057), dans l'un ou l'autre mode —
le store d'une instance et un store transportable se vérifient de la même
façon. Quatre contrôles, chacun indépendant du verdict des autres :

| Contrôle | Ce qu'il établit |
|---|---|
| blobs | chaque blob est re-haché contre le digest sous lequel il est stocké |
| manifests | chaque manifeste atteignable depuis une recipe enregistrée est complet : sa config, ses couches, ses artefacts de signature, les enfants d'un index que la recipe a retenus (une plateforme qu'elle n'a pas sélectionnée n'est pas un défaut ; une qu'elle a retenue et que le store a perdue en est un) |
| signatures | chaque signature de recipe enregistrée se vérifie contre les trust roots configurées **maintenant**, périmètres appliqués ; le [marqueur d'assurance réduite](../../security/reduced-assurance/) d'une recipe admise sans signature par un périmètre est re-rapporté |
| orphans | le contenu qu'aucune recipe enregistrée ni aucune racine protégée (imports unitaires, contenu amorcé, base de vulnérabilités) n'atteint est listé |

La commande **ne fait que lire**. Elle ne répare rien, ne supprime rien et
n'écrit rien dans le store, qui est identique à l'octet près ensuite. Le
rapport est horodaté (début et fin, identifiant de run, verdict) et nomme
chaque défaut sous forme de code de la taxonomie avec le fichier ou l'objet
concerné ; le contenu orphelin est une note et ne fait jamais passer le verdict
à `defects`. Elle refuse un répertoire qui n'est pas déjà un store Tobby plutôt
que de l'estampiller.

Sans `--instance`, elle s'exécute dans ce processus, contre le répertoire, et
fonctionne sur un hôte où aucune instance ne tourne. Les trust roots viennent
de la configuration (`trust.roots`, `trust.scopes`), jamais du store. Quand une
instance sert *effectivement* le store, cette forme **refuse** — elle ne voit
pas le verrou de contenu de l'instance — et nomme `--instance` : la vérification
s'exécute alors sur l'instance comme tâche suivie, sous son verrou de contenu,
sur une voie à part qui ne retarde jamais une synchronisation. Voir
[Vérifier l'intégrité du store](../../passthrough/verify-store/).

| Option | Rôle |
|---|---|
| `--storage-root` | Le store à vérifier. Requis — par l'option, `TOBBY_STORAGE_ROOT`, ou `storage.root:` dans le fichier de configuration. |
| `--output text\|json` | `json` émet le rapport lui-même sur stdout, le même document que sert `GET /api/v1/store/verification` sous `verification`. Avec `--instance` et sans `--wait`, la tâche que l'instance a créée. |
| `--instance` | URL de base de l'instance en marche à piloter (défaut `TOBBY_INSTANCE_URL`, puis l'adresse d'écoute configurée) ; token pris dans `TOBBY_API_TOKEN` ou `--token-file`, envoyé en `http://` uniquement vers cet hôte sauf `--allow-plaintext-token`. |
| `--wait`, `--wait-timeout` | Avec `--instance` : suit la tâche jusqu'à sa fin, affiche le rapport, et sort sur son résultat. |

Il n'y a ni planification ni option de réparation : la v1 vérifie à la demande
et rapporte.

```sh
tobby store verify --storage-root /mnt/usb
tobby store verify --config /etc/tobby/config.yaml --output json | jq '.defects[] | .code + " " + .path'
```

Codes de sortie : `0` le store est intact, `1` la vérification a été annulée ou
a échoué, `4` un défaut a été trouvé — un blob qui ne correspond pas à son
digest, un manifeste incomplet ou illisible, une signature qui ne se vérifie
plus.

### tobby diagnostics bundle

```
tobby diagnostics bundle [-o <fichier>] [--instance <url> [--token-file <fichier>]] [flags]
```

Écrit une archive locale et expurgée à remettre au support (SRS FR-095) : la
version et le build, la configuration effective, les derniers enregistrements
de journal, les rapports des dernières tâches, un instantané des métriques, le
dernier rapport d'intégrité, l'inventaire d'état, et un `MANIFEST.json` avec
l'empreinte de chaque fichier.

L'expurgation échoue fermée : chaque fichier est cherché pour les secrets que
l'instance connaît avant que rien ne soit écrit, et une correspondance abandonne
la commande avec [`TBY-DIA-002`](../../reference/errors/#tby-dia-002), qui
nomme le fichier. Tobby n'envoie l'archive nulle part. Le nom par défaut est
`tobby-diagnostics-<heure UTC>.tar.gz` dans le répertoire courant ; un fichier
existant n'est jamais écrasé, et l'archive est écrite lisible par son seul
propriétaire.

Sans `--instance`, l'archive est assemblée sur l'hôte à partir de la
configuration, du store et du répertoire d'état ; la fenêtre de journal et les
métriques appartiennent à un processus en marche et figurent au manifeste comme
écartées, avec la raison. Avec `--instance` (ou `TOBBY_INSTANCE_URL`), l'archive
est demandée à l'instance en marche, avec le jeton de l'administrateur pris dans
`TOBBY_API_TOKEN` ou `--token-file`, et les inclut. La demande est auditée dans
les deux cas. Codes de sortie : `0` écrite, `3` refusée (contrôle d'expurgation,
ou rôle), `1` échec. Voir
[Collecter un bundle de diagnostic](../../passthrough/diagnostics/).

### tobby state inspect

```
tobby state inspect [flags]
```

Liste les entrées du répertoire d'état de l'instance (SRS FR-097) avec leur
classe — *identité* (à sauvegarder) ou *jetable* —, si elles sont secrètes, leur
taille et leurs permissions. Des noms, classes, tailles et modes seulement ;
jamais un contenu. Elle lit le répertoire sans rien ouvrir, donc sans risque à
côté d'une instance en marche. Une entrée secrète lisible par tous les
utilisateurs de l'hôte est signalée, et la commande sort en `3` avec
[`TBY-STA-002`](../../reference/errors/#tby-sta-002) — le refus qu'un démarrage
donnerait. Voir [Sauvegarder et reconstruire une instance](../../passthrough/backup-rebuild/).

### tobby scan exceptions

```
tobby scan exceptions [flags]
```

Liste les exceptions de vulnérabilités de la politique d'analyse (SRS FR-031,
amendement R-15) — le même document que
[l'écran Politique de contenu](../../security/vulnerability-scanning/#la-politique-effective)
et `GET /api/v1/policy` : pour chaque exception son origine (`instance`,
`retriever`, ou `vex` avec le document OpenVEX), sa portée, sa justification,
son échéance et son état **maintenant** — `active`, `expired` (elle ne
s'applique plus : les constats qu'elle couvrait sont jugés de nouveau) ou
`unbounded` (une exception de Retriever au format `v1alpha1` sans échéance,
appliquée, `TBY-SCN-028`). Les documents OpenVEX sont listés avec les
déclarations admises et ignorées, et pourquoi ; les interrupteurs de
durcissement sont indiqués.

**Où elle lit.** Par défaut, **la configuration de cet hôte** (`--config`,
l'environnement) et ses documents `scan.vex`, dans ce processus, sans rien
contacter : un document illisible ou invalide échoue comme il refuserait le
démarrage (`TBY-SCN-027`). Les exceptions du Retriever ne sont connues que
d'une instance en marche qui a servi son Retriever — elles ne sont pas listées
dans ce mode, et le rapport le dit (`"retriever_known": false`). Avec
`--instance` (ou `TOBBY_INSTANCE_URL` défini), la commande lit à la place la
politique effective d'une instance en marche, `GET /api/v1/policy`, exceptions
du Retriever comprises ; le document JSON dit laquelle (`"source":
"configuration"` ou `"instance"`).

| Option | Rôle |
|---|---|
| `--config` | Le fichier de configuration à lire (forme hors ligne). |
| `--output text\|json` | `json` écrit le document de politique sur stdout (`source`, `evaluated_at`, `mode`, `threshold`, `blocking`, `retriever_known`, `exceptions[]`, `vex_documents[]`, `hardening`). |
| `--instance` | URL de base de l'instance en marche à lire ; jeton (rôle **admin**, le seuil de `GET /api/v1/policy` et de son écran) depuis `TOBBY_API_TOKEN` ou `--token-file`, envoyé en `http://` seulement vers cet hôte sauf `--allow-plaintext-token`. |

```sh
tobby scan exceptions
tobby scan exceptions --output json | jq -r '.exceptions[] | select(.state=="expired") | .id'
tobby scan exceptions --instance https://tobby.example:8443
```

La commande ne modifie rien. Codes de sortie : `0` la politique a été lue,
quels que soient les états ; `1` la configuration ou un document VEX n'a pas pu
être lu ; `2` erreur d'usage (une option d'instance sans `--instance`, par
exemple).

### tobby recipe push

```
tobby recipe push <file> <registry>/<cookbook>/<name>:<version> [flags]
```

Valide un document de recipe et le publie sous la forme de l'artefact OCI que
définit la [spécification des recipes](https://tobby-fetch.github.io/recipe-spec/).
La publication est refusée quand :

- le document n'est pas une Recipe valide ;
- il n'est pas entièrement épinglé — un cookbook ne contient que des recipes
  cuisinées : chaque ingrédient porte un tag exact **et** un digest ;
- son nom ou sa version contredit la référence sous laquelle il est publié ;
- le tag existe déjà avec un contenu différent — une version de recipe publiée
  est immuable ([`TBY-POL-004`](../../reference/errors/#tby-pol-004)). Publier
  deux fois le même document est une non-opération, pas un conflit.

La signature reste hors de Tobby, qui ne détient jamais de clé privée. En mode
texte, le digest publié est affiché seul sur stdout, prêt pour cosign :

```sh
tobby recipe push harbor.yaml registry.example.com/cookbook/harbor:2.15.4
cosign sign --key cosign.key --use-signing-config=false --tlog-upload=false \
  registry.example.com/cookbook/harbor@<the printed digest>
```

### tobby user

```
tobby user add <name> [--role viewer|operator|admin] [--password-stdin]
tobby user list
tobby user passwd <name> [--password-stdin]
```

Gère les comptes locaux de l'instance, directement contre le répertoire d'état
sur l'hôte de l'instance. Le premier compte créé a par défaut le rôle `admin` ;
les suivants `viewer`. L'outil calcule le hachage du mot de passe (argon2id) —
il n'y a jamais à fabriquer un hachage à la main.

L'enregistrement d'audit d'une opération sur un compte va sur **stderr**, avec
tous les autres journaux structurés : stdout porte le rapport.

### tobby version

Affiche la version et les métadonnées de build sur une ligne : version,
commit, date de build, version de Go, plateforme. Sous `--output json`, les
mêmes faits arrivent en membres séparés, plus la majeure d'API que parle ce
binaire et la liste exhaustive des codes de sortie qu'il peut renvoyer.
Vérifier que cette sortie correspond à une release signée est décrit dans
[Vérifier une release](../../project/verify-a-release/).

### tobby completion

Génère le script de complétion pour `bash`, `zsh`, `fish` ou `powershell`.

## Codes de sortie

Le code de sortie classe l'issue, pour que les scripts se branchent sans
analyser les messages (SRS FR-066, amendement R-08). **La table ci-dessous est
produite depuis le code** — `internal/taxonomy` — et un test fait échouer la
compilation si les deux divergent, dans un sens comme dans l'autre : un code que
la table n'énumère pas, ou une ligne que rien ne peut produire.

La table est couverte par la
[promesse de versionnement sémantique](../../project/release-compatibility/) du
projet : retirer un code, ou en renuméroter un, est un changement cassant.

<!-- generated:exit-codes -->
| Code | Nom | Signification |
|---|---|---|
| `0` | `ok` | **Succès** — La commande a fait ce qui était demandé. |
| `1` | `failure` | **Échec opérationnel** — Erreur réseau, stockage ou interne — tout ce qui a échoué sans verdict de politique ni de vérification. Tous les codes de la classe opérationnelle sortent ici, ainsi qu'une commande qui a déclenché une tâche que l'instance a ensuite fait échouer. |
| `2` | `usage` | **Erreur d'usage** — Drapeau erroné, commande inconnue, ou commande à qui l'on demande ce qu'elle ne peut pas honorer. Le message porte l'indication `see 'tobby … --help'` désignant la commande dont l'analyse a échoué. |
| `3` | `policy` | **Refus de politique** — Refusé par une politique ou une autorisation explicite : liste blanche de registres, rôles, refus de démarrage sécurisés par défaut, immutabilité d'un tag de recipe, les deux garde-fous de média levables par un administrateur. |
| `4` | `verification` | **Échec de vérification** — Un contrôle d'intégrité ou d'authenticité a échoué : signatures, digests épinglés, types d'artefact, sommes de contrôle du média. La classe la plus sévère, et la seule qu'aucune levée ne rouvre. |
| `5` | `changes-planned` | **Changements prévus** — Une exécution sans effet de bord a trouvé du travail à faire — `tobby sync --dry-run` sur un Retriever porteur de changements. C'est un succès qui a quelque chose à dire, pour qu'une barrière d'intégration puisse s'y brancher sans y voir une compilation cassée. |
<!-- /generated:exit-codes -->

Toute erreur de la [taxonomie TBY-\*](../../reference/errors/) appartient à l'une
des trois classes d'échec, son code de sortie fait donc partie du même contrat
que son code. La colonne `Nom` est le nom machine stable d'une ligne : il fait
lui aussi partie du contrat, et n'est jamais traduit.

Sous `--wait`, une commande qui a démarré une tâche sort sur le résultat
**propre à la tâche**, traduit par la même table : un refus de politique sur
l'instance est un code de sortie `3` en ligne de commande.

## Sortie d'erreur

Les erreurs s'affichent sur **stderr** ; stdout est réservé à la sortie
machine. Une erreur de la taxonomie affiche sa forme structurée en trois
parties — ce qui s'est passé, la cause probable, l'action corrective — avec son
code `TBY-*` stable et, quand il est disponible, l'identifiant de corrélation
qui retrouve les enregistrements de journal correspondants. Les autres erreurs
gardent une forme simple d'une ligne, `tobby: <message>`.

## Comportement non interactif

La CLI est conçue pour l'automatisation d'abord : l'interactivité est une aide,
jamais une obligation. Aucune commande n'invite l'utilisateur, aucune n'exige
de terminal — garantie tenue par un test qui exécute tout l'arbre des commandes
avec un tube en entrée standard et fait échouer toute commande qui se bloque ou
affiche une invite.

- `tobby quickstart` refuse de s'exécuter quand l'entrée standard n'est pas un
  terminal et que des réponses manquent — le message de refus fournit les
  commandes équivalentes pilotées par options
  (`tobby user add … --password-stdin`, puis `tobby serve …`).
- `tobby user add` et `tobby user passwd` demandent le mot de passe sans écho,
  deux fois ; sous `--password-stdin`, ils le lisent sur la première ligne de
  l'entrée standard, et refusent une entrée vide. Sans terminal et sans
  `--password-stdin`, ils échouent dans la première seconde avec un message qui
  nomme l'option.
- Les identifiants ne sont jamais des options. Le mot de passe du proxy vient du
  fichier de configuration ou de `TOBBY_NETWORK_PROXY_PASSWORD` ; le token
  d'API de `tobby sync` vient de `TOBBY_API_TOKEN` ou de `--token-file`. Une
  valeur d'option est visible dans la table des processus et dans l'historique
  du shell.
- Les invites et la progression vont sur stderr ; seule la sortie machine va
  sur stdout.
- Aucune commande ne pose jamais une question à laquelle elle peut répondre
  d'après les options, l'environnement ou le fichier de configuration.

## Langue des messages

Les messages de la CLI suivent la convention de l'hôte : si `LC_ALL` (vérifié
en premier) ou `LANG` commence par `fr`, les erreurs de la taxonomie
s'affichent en français ; tout autre cas affiche l'anglais. Cela n'affecte que
les messages — options, commandes, codes de sortie, noms des codes de sortie et
sortie machine sont identiques dans les deux langues. L'UI web négocie sa
propre langue par utilisateur, indépendamment de la locale de l'hôte.
