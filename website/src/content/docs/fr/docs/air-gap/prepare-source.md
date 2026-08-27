---
title: Préparer le poste source
description: Installer et configurer une instance miroir, ce que le pré-vol refuse, comment planifier une synchronisation sans la lancer, et ce qui finit sur le support.
sidebar:
  order: 2
---

Le côté source d'un transfert physique, c'est un binaire sur un poste. Cette
page couvre tout ce qui se passe du côté connecté : l'installer, le
configurer en mode miroir, contrôler que le transfert tiendra, le répéter,
puis l'exécuter.

Les noms d'étapes viennent du [parcours](../../air-gap/media-workflow/) —
préparer, pré-vol, exporter — et sont ceux qu'emploient les écrans et les
messages d'erreur.

## Installer le poste

Une instance miroir est un binaire unique lié statiquement, sans dépendance
d'exécution. Installez-le comme votre site installe un logiciel sur une
machine qui va toucher un support destiné à une zone isolée :

- **Linux** — le binaire de release, ou le paquet `.deb`, `.rpm` ou `.apk`,
  tous installables sans aucun dépôt distant configuré.
- **Windows** — le binaire de release, `tobby-windows-amd64.exe` ou
  `tobby-windows-arm64.exe`. Il n'y a ni installeur ni canal de gestionnaire
  de paquets : les manifestes winget et Scoop sont construits et joints à
  chaque release, mais aucun des deux index ne porte Tobby, si bien que
  l'archive est la voie d'entrée. Voir
  [plateformes supportées](../../reference/platforms/).

Vérifiez la release avant de lui faire confiance — provenance, SBOM et
sommes de contrôle sont tous vérifiables indépendamment, et la procédure est
sur [vérifier une release](../../project/verify-a-release/). Faites-le sur
une machine connectée, avant que le binaire n'approche du support.

## Configurer le mode miroir

`tobby quickstart` pose les questions et écrit le fichier de configuration ;
`--mode mirror` pré-répond à la question du mode. Il n'écrase jamais une
configuration existante et n'est pas une obligation — tout ce qu'il écrit
peut s'écrire à la main.

Ce dont une source miroir a besoin :

```yaml
mode: mirror

storage:
  root: /media/usb/tobby-store     # le store transportable — le support
state:
  root: /var/lib/tobby/state       # comptes, jetons, clés — le poste

retriever:
  source: https://registry.example.com/zones/isolated/retriever.yaml

trust:
  roots:
    - name: qualification-2026
      keyFile: /etc/tobby/pki/cosign-qualification-2026.pub

registries:
  allowlist:
    - registry.upstream.example.com
```

Trois de ces réglages méritent une phrase.

**`storage.root` est le support.** Il n'y a pas d'étape d'empaquetage : le
store *est* le transfert, un répertoire relocalisable ordinaire. Pointez-le
sur le point de montage du support, ou sur un répertoire d'attente que vous
copiez ensuite sur le support — le store se moque de l'endroit où il est
posé, et c'est ce qui le rend transportable.

**`state.root` ne l'est pas.** Comptes, jetons, clé TLS, spool de reprise et
registre des imports par zone vivent là, sur le poste, et ne voyagent
jamais. C'est appliqué, pas conseillé : une instance **refuse de démarrer**
(`TBY-CFG-002`) quand `state.root`, `registries.credentialsFile` ou
`server.tls.keyFile` se résout dans le store — liens symboliques, `..` et
écritures relatives compris. Voir [Secrets](../../security/secrets/).

**`zone:` ne se met pas sur la source.** Une instance source apprend la zone
qu'elle sert du Retriever qu'elle résout. Seule une instance de
*destination* — qui n'a pas de Retriever, puisque son contenu arrive sur un
support — reçoit son identité de zone par configuration. Cette différence
est aussi ce qui permet au verrou de service côté destination de distinguer
les deux côtés : poser `zone:` sur un poste source change son comportement
et n'est pas un supplément anodin.

### La synchronisation est manuelle, toujours

Une instance miroir n'a pas d'ordonnanceur. `sync.interval` et `sync.prune`
sont des réglages de passthrough et sont refusés en mode miroir. La
préparation d'un support se déclenche depuis l'interface, depuis
`POST /api/v1/sync`, ou avec
[`tobby sync`](../../reference/cli/#tobby-sync) contre l'instance qui tourne
— toujours par une personne.

C'est une position de conception et non une fonctionnalité manquante : un
processus non supervisé ne doit pas décider de ce qui traverse un air gap,
et un support écrit pendant que personne ne regarde est un support dont
personne ne peut répondre.

## Pré-vol : est-ce que ça tient, est-ce que ça passera ?

Avant tout transfert, Tobby calcule ce qui voyagerait et le compare à ce que
la cible peut contenir. Il en sort deux refus et deux avertissements
(FR-055).

**Le volume contre l'espace.** Les octets à transférer sont calculés par
recipe depuis les manifests source, dédupliqués par digest et nets de ce que
le store détient déjà. La projection est comparée à l'espace libre de la
cible moins une marge de sécurité — `preflight.safetyMarginPercent`, **10 %
par défaut** — et la synchronisation est refusée avant qu'un seul octet ne
bouge quand ça ne tient pas, en énonçant le manque en octets
([`TBY-STO-004`](../../reference/errors/#tby-sto-004)). La marge existe
parce que le store n'est jamais le seul écrivain de son volume.

**La capacité du système de fichiers.** Un système de fichiers positivement
identifié comme incapable de contenir le plus gros fichier que la course
écrirait est refusé nommément
([`TBY-STO-005`](../../reference/errors/#tby-sto-005)). FAT32 et son plafond
de 4 Gio par fichier est le cas canonique, et une archive d'export en tar
unique est un fichier. L'identification est propre à chaque plateforme et
délibérément étroite : `statfs` sous Linux et macOS,
`GetVolumeInformationW` sous Windows.

**Un système de fichiers dont ce build ne connaît aucun plafond est rapporté
comme non identifié, jamais comme capable.** C'est un avertissement, pas un
refus : la course se poursuit et le rapport dit que la garantie n'était pas
disponible. Idem quand l'espace libre ne peut pas être lu.

**Une erreur « fichier trop gros » arrivant en cours d'écriture échoue
proprement**, store intact. Le pré-vol est une courtoisie qui transforme un
transfert corrompu en refus précoce ; il n'est pas la seule chose entre vous
et un blob tronqué.

`preflight.disabled: true` transforme la barrière en rapport : les volumes
et les verdicts de système de fichiers sont toujours calculés et toujours
affichés, et ils ne refusent plus rien. C'est un retrait explicite et
annoncé d'un contrôle de sécurité — consigné au démarrage, et de nouveau
chaque fois qu'il laisse passer un refus — et le verdict garde son code de
refus, si bien qu'une barrière désactivée ne peut jamais être prise pour une
barrière franchie.

## Le mode plan : toute la course, sans la course

`tobby sync --dry-run` rapporte tout ce qu'une synchronisation ferait et
n'en fait rien :

```sh
tobby sync --dry-run --storage-root /media/usb/tobby-store
tobby sync --dry-run --retriever ./candidate-retriever.yaml --output json
```

Le rapport porte la version résolue de chaque recipe, le statut par digest
de chaque ingrédient contre le store, le volume dédupliqué à transférer, la
taille projetée du store contre l'espace libre et la capacité du système de
fichiers de la cible, le contenu qu'un prune retirerait, et les verdicts de
politique qui ne demandent aucun transfert — la liste blanche de registries
et les signatures des recipes elles-mêmes.

Rien n'est écrit, rien n'est poussé, et la cadence de réconciliation d'une
instance passthrough reste exactement où elle était. La garantie est
structurelle — le planificateur ne tient qu'une vue en lecture seule du
store et aucun ordonnanceur — et un test prend l'empreinte de tout l'arbre
du store avant et après un plan, et échoue à la moindre différence.

Les codes de sortie en font une barrière plutôt qu'un rapport à lire à
l'œil :

| Sortie | Signification |
|---|---|
| `0` | rien à faire |
| `5` | des changements sont prévus |
| `3` | refusé par la politique (un registre hors liste blanche) |
| `4` | échec de vérification (une signature de recipe qu'aucune clé de confiance ne valide) |
| `1` | le plan n'a pas pu aboutir |

Le même rapport est sur `POST /api/v1/plan` et sur l'écran `/recipes/plan`,
où un Retriever **candidat** peut être planifié à la place de celui qui est
configuré — un fichier, une URL, une référence OCI, ou un document collé.
C'est ainsi qu'on relit un changement de Retriever avant de l'adopter.

`--dry-run` refuse en erreur d'usage les drapeaux qui n'ont de sens que
contre une instance qui tourne (`--wait`, `--instance`, `--token-file`, …)
plutôt que de les ignorer : un pipeline qui a écrit
`tobby sync --dry-run --wait` s'entend dire qu'il n'a attendu personne.

<!-- TODO: screenshot: l'écran de plan — un Retriever candidat planifié contre le store, volumes par recipe et verdict d'espace -->

## Exporter : remplir le support

Déclenchez la synchronisation. Tobby résout le Retriever, télécharge ce qui
manque, vérifie signatures et digests à l'entrée, et écrit tout dans le
store.

### À quoi ressemble le store

Le store transportable est un répertoire ordinaire, autonome et
auto-descriptif :

```text
<store>/
├── docker/registry/v2/   # le store OCI adressé par contenu : images, charts,
│                         # artefacts, filesets — et les recipes elles-mêmes
├── meta/                 # la comptabilité : version du format de store, graphe
│                         # des recipes, registre de provenance, et media.json
└── _tobby/               # la zone propre à Tobby : file de tâches et journaux
                          # d'opération, des deux côtés du voyage
```

Deux de ces trois répertoires sont couverts par le manifeste de média :
`docker/registry/v2/` et `meta/`. `_tobby/` ne l'est pas, par construction —
il continue d'être écrit après la prise de l'inventaire, et un fichier qui
grossit encore ne peut pas y figurer sans invalider l'inventaire à sa ligne
suivante.

### Le manifeste de média, écrit en dernier

La dernière chose que fait une synchronisation miroir — **après tout prune**
— est d'écrire `meta/media.json` : la version du format de store,
l'identifiant du support, l'identité de zone, la version productrice et le
run, l'horodatage de résolution du Retriever, les recipes couvertes avec
leurs digests épinglés, et un inventaire fichier par fichier de chemin,
taille et SHA-256.

Il est **non signé, et rien ne repose dessus**. Ce qu'il vous apporte, c'est
un échec précisément localisé à l'autre bout — quel fichier, quelle recipe —
au lieu d'une erreur de vérification générique au moment du push, et un
inventaire lisible avant de brancher le support en zone isolée. Pourquoi il
est sans danger qu'il ne soit pas signé est le sujet du
[modèle de sécurité du média](../../air-gap/media-security/).

### Avant de démonter

- La synchronisation s'est terminée, ou chaque recipe bloquée est comprise
  et son absence acceptée.
- Le manifeste de média a été écrit — c'est toujours la dernière écriture.
- Le run ID est reporté dans votre dossier de transfert.
- Le support est démonté proprement. Sous Windows en particulier, un FileSet
  servi maintenait autrefois le volume ouvert ; ce n'est plus le cas, mais
  arrêter l'instance avant d'éjecter reste la procédure.

L'écran **Média** côté source est la liste de colisage de ce moment précis :
à quelle zone le support est adressé, quand il a été résolu, ce qu'il livre,
ce qu'il pèse.

<!-- TODO: screenshot: l'écran Média côté source — la fiche de remise, lue avant de démonter le support -->

Ensuite : [importer côté zone isolée](../../air-gap/import-destination/).
