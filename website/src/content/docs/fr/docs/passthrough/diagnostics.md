---
title: Collecter un bundle de diagnostic
description: Une archive locale et expurgée à remettre au support — ce qu'elle contient, comment la produire en ligne de commande, dans l'interface ou par l'API, et comment l'expurgation échoue fermée.
sidebar:
  order: 10
---

Quand quelque chose se passe mal, la personne qui vous aide a besoin des mêmes
choses : quel build, comment il est configuré, ce qu'il a journalisé, ce qu'ont
fait ses dernières tâches, ce que disent les métriques, si le store se vérifie,
à quoi ressemble le répertoire d'état. Le bundle de diagnostic les rassemble en
**un seul geste** dans **une seule archive** — et il est construit pour que vous
puissiez la remettre sans la relire ligne à ligne pour y chercher des secrets.

Tobby n'envoie l'archive nulle part. Il n'y a ni téléversement, ni appel
sortant, ni adresse à configurer (NFR-019) : c'est un fichier que vous écrivez
ou téléchargez, et c'est vous qui choisissez qui le reçoit.

## Ce qu'il contient

Une archive tar compressée en gzip, aux noms de fichiers fixes pour qu'un
ingénieur du support retrouve la même chose dans chaque archive :

| Fichier | Ce que c'est |
|---|---|
| `MANIFEST.json` | Chaque fichier de l'archive avec sa taille et son empreinte SHA-256, la version du format, et chaque fichier documenté **écarté** avec la raison. |
| `version.json` | Version, commit, date de build, version de Go, plateforme, mode, zone. |
| `config.yaml` | La configuration effective, expurgée exactement comme [`tobby config dump`](../../reference/cli/) l'expurge. |
| `logs.jsonl` | Les derniers enregistrements de journal que l'instance tient en mémoire — une fenêtre de `diagnostics.logRecords` enregistrements (2000 par défaut, 100000 au plus). |
| `media-log.jsonl` | Mode miroir : la fin du journal d'opérations du support. |
| `tasks/<id>.json` | Les rapports des dernières tâches — `diagnostics.tasks` d'entre elles (20 par défaut, 500 au plus) — avec leurs items, erreurs et résolutions. |
| `metrics.txt` | Un instantané des métriques, tel qu'une collecte les renverrait. |
| `integrity.json` | Le dernier rapport de [vérification à la demande](../verify-store/), s'il y en a eu un. |
| `state-inventory.json` | Le [répertoire d'état](../backup-rebuild/) : noms, classes, tailles et permissions — jamais les contenus. |

La fenêtre de journal est une **fenêtre, jamais une archive** : elle vit dans la
mémoire du processus en marche et disparaît avec lui. Une instance passthrough
journalise sur sa sortie standard et ne garde rien d'autre ; ce qui compte pour
un incident futur doit donc aussi être collecté par votre propre chaîne de
journaux.

## Le produire

**Sur l'hôte**, avec la configuration que l'instance utilise :

```sh
tobby diagnostics bundle --config /etc/tobby/config.yaml
# diagnostic bundle written to tobby-diagnostics-20261116T101500Z.tar.gz (...)
tobby diagnostics bundle -o /var/tmp/support.tar.gz --output json
```

Sans instance en marche, l'archive est assemblée à partir de la configuration,
du store et du répertoire d'état. La fenêtre de journal et les métriques
appartiennent à un processus en marche : elles sont listées comme **écartées**,
avec la raison, dans le manifeste. Un fichier existant n'est jamais écrasé, et
l'archive est écrite lisible par son seul propriétaire.

**Depuis l'instance en marche**, par la même commande qui interroge l'API :

```sh
export TOBBY_API_TOKEN=...        # le jeton d'un administrateur (jamais un flag)
tobby diagnostics bundle --instance https://tobby.example:8443
```

ou depuis l'écran d'administration **Diagnostic** (`/admin/diagnostics`), ou avec
`POST /api/v1/diagnostics/bundle`. L'archive de l'instance ajoute la fenêtre de
journal et les métriques. Il faut le rôle **administrateur** : les rôles
opérateur et lecteur sont refusés.

Produire un bundle est un événement du [journal d'audit](../../security/audit-log/)
(`diagnostics.bundle`) qui nomme le demandeur et dit si l'archive a été écrite,
et le compteur `tobby_diagnostics_bundles_total{outcome}` dénombre les issues :
`written`, `refused`, `failed`.

## L'expurgation échoue fermée

L'archive est contrôlée **avant d'être livrée**, en mémoire, et n'est pas écrite
du tout si le contrôle trouve un secret. Deux temps :

1. **Épurer.** Dans les fichiers qui portent du texte écrit par d'autres
   composants — la fenêtre de journal, le journal du support, les rapports de
   tâches, les métriques — les secrets que l'instance connaît sont remplacés par
   `[REDACTED]`, ainsi que les identifiants d'une forme reconnaissable : en-têtes
   `Authorization` et `Cookie`, identifiants bearer et basic, mot de passe dans
   une URL, bloc de clé privée, et la valeur d'une clé nommée `password`,
   `secret`, `token` et semblables.
2. **Contrôler.** *Chaque* fichier est ensuite cherché pour les secrets que
   l'instance connaît. Les fichiers structurés que Tobby écrit lui-même —
   version, configuration, rapport d'intégrité, inventaire d'état — ne sont pas
   épurés : un secret dans l'un d'eux est un défaut, et le bundle est
   **abandonné** avec [`TBY-DIA-002`](../../reference/errors/#tby-dia-002), qui
   nomme le fichier, jamais le secret.

« Les secrets que l'instance connaît » sont : chaque valeur secrète de la
configuration (le mot de passe du proxy, où qu'il ait été posé), le **contenu**
de chaque fichier secret que la configuration référence (identifiants de
registry, clé privée TLS, secret client OIDC, clé SAML) et des fichiers de clé
du répertoire d'état, et la valeur des variables d'environnement qui ressemblent
à des identifiants (`*PASSWORD*`, `*SECRET*`, `*TOKEN*`, `*KEY*`…). Chacune est
cherchée en clair et dans les encodages où elle est susceptible d'apparaître :
base64 (standard, brut, URL), hexadécimal, échappé pour URL, échappé pour JSON ;
pour une clé privée, son corps PEM ; pour un fichier d'identifiants de registry,
chaque identifiant et le `user:password` décodé. Si un fichier secret configuré
existe mais ne peut pas être lu, le bundle échoue avec
[`TBY-DIA-001`](../../reference/errors/#tby-dia-001) plutôt que de deviner ce
qu'il contient.

Ce que cela ne promet **pas**, dit sans détour :

- Un secret de **moins de 4 octets** n'est pas cherché ; un secret de moins de 8
  n'est reconnu que comme mot entier, pour qu'un mot de passe court ne fasse pas
  expurger tous les mots qui le contiennent. La valeur d'une variable
  d'environnement n'est prise pour un secret qu'à partir de 8 octets, car une
  variable qui a seulement « KEY » dans son nom et la valeur `true` ferait
  abandonner le bundle dès qu'un document contient ce mot.
- Un secret que l'instance ne connaît pas — saisi par une personne dans un champ
  libre d'une tâche, par exemple — n'est attrapé que s'il a une forme
  reconnaissable.
- Le contrôle protège contre les **secrets**. Les noms d'hôtes, de registries,
  de références d'images et de comptes sont dans l'archive, et peuvent être
  sensibles pour vous. Relisez ce que vous partagez.

## Configuration

| Clé | Défaut | Borne |
|---|---|---|
| `diagnostics.logRecords` | `2000` | 1 à 100000 — la taille de la fenêtre en mémoire. |
| `diagnostics.tasks` | `20` | 1 à 500 — combien de rapports de tâches récentes. |
