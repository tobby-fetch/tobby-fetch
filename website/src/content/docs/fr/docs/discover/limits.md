---
title: Limites et hors périmètre
description: Ce que Tobby ne fait délibérément pas, ce qu'il ne fait pas encore, avec la conséquence opérationnelle et la justification de chaque limite.
sidebar:
  order: 5
---

L'honnêteté est une fonctionnalité de cette documentation : chaque limite
ci-dessous est accompagnée de sa conséquence opérationnelle et de sa
justification. Certaines sont des décisions de conception permanentes
([SRS §5](../../reference/srs-adr/) est la source normative) ; d'autres ne
sont simplement pas encore livrées et portent un badge de jalon — la page
[état du projet](../../discover/status/) les suit.

## Décisions de conception permanentes

### Tobby ne signe rien

Tobby ne détient aucune clé privée et ne produit aucune signature — ni sur le
contenu, ni sur les recipes, ni sur son manifeste de média ou son journal
d'audit.
**Conséquence :** votre organisation possède toute la chaîne de signature.
Les clés, leur rotation et l'étape de signature dans votre pipeline de
qualification relèvent de vous, hors de Tobby.
**Justification :** un outil de transport qui signe devient une ancre de
confiance, et sa compromission permettrait de forger la confiance.
L'authenticité provient exclusivement de vos signatures cosign, vérifiées
contre [les trust roots de la destination](../../security/content-trust/).

### Le manifeste du média n'est pas signé

L'inventaire que porte un store transportable est une aide à l'intégrité —
liste de fichiers, sommes de contrôle, identité de zone, identifiant de run —,
non une ancre de confiance.
**Conséquence :** le manifeste seul ne prouve rien. Ce qui prouve
l'authenticité à l'arrivée, c'est la vérification de chaque signature de
recipe contre les trust roots configurées sur l'instance de destination ; les
trust roots trouvées sur le média sont ignorées.
**Justification :** signer le manifeste réintroduirait le problème d'ancre de
confiance ci-dessus, sans gain de sécurité. Le raisonnement complet est sur la
page [modèle de sécurité du média](../../air-gap/media-security/).

### Pas de purge de la destination

Tobby ne supprime jamais de contenu d'une registry de destination (SRS §5.1).
**Conséquence :** le contenu obsolète s'accumule dans les registries de zone
jusqu'à ce que vous le supprimiez. Le schéma prévu est un mark-and-sweep en
aval sur le cookbook de la zone : énumérer chaque ingrédient référencé par
chaque recipe, supprimer tout le reste.
**Justification :** supprimer à l'intérieur d'une zone est une autorité que
Tobby ne doit pas détenir. Son obligation est de ne pas faire obstacle : il
expose des listings standard complets et propage les recipes avec les
artefacts, si bien que l'ensemble de référence d'un sweep est toujours
disponible dans la zone.

### Pas de qualification amont

Tobby ne construit, ne teste, n'analyse pour qualifier, ne signe ni ne choisit
de logiciel, et n'orchestre pas de CI/CD (SRS §5.3).
**Conséquence :** une recipe signée contenant un mauvais choix sera
fidèlement transportée. Garbage in, verified garbage out.
**Justification :** la qualification est le travail de votre pipeline. La
vérification que Tobby effectue — signatures, digests, liste d'autorisation,
et analyse depuis le jalon 6 — est une garantie de transport, non une étape
de qualification.

### Pas de serveur générique de dépôt de fichiers

Servir des fichiers téléversés arbitraires est exclu (SRS §5.2).
**Conséquence :** vous ne pouvez pas déposer de fichiers en vrac sur une
instance en HTTP. Les fichiers voyagent comme ingrédients FileSet — empaquetés,
épinglés par digest, signés — et les FileSets vérifiés sont servis en lecture
seule sous `/files/`, ce qui suffit pour des dépôts apt/rpm et l'amorçage
d'un hôte nu.
**Justification :** une surface de téléversement ad hoc contournerait le
modèle de recipe et toute sa chaîne de vérification.
La seule voie par laquelle des fichiers locaux entrent dans un store est
[`tobby fileset pack`](../../reference/cli/#tobby-fileset-pack), qui lit un
répertoire **de l'hôte de l'instance** — jamais un corps de requête —, est
réservé aux administrateurs, est confiné aux répertoires nommés par
`files.packRoots`, et consigne ce qu'il a produit comme un import manuel non
signé d'origine locale.

### Un seul mode par instance, fixé au démarrage

Une instance fonctionne dans exactement un mode ; en changer exige un
redémarrage.
**Conséquence :** couvrir les deux cas d'usage suppose deux instances.
**Justification :** changer de mode réaffecte l'instance — son store, sa
posture, son modèle de déclenchement. En faire un interrupteur à chaud
inviterait aux accidents ; l'interface affiche donc le mode en lecture seule.

### La synchronisation du miroir est toujours manuelle

En mode mirror, il n'y a pas d'ordonnanceur : la synchronisation est un
bouton ou un appel d'API, jamais sans surveillance.
**Conséquence :** les transferts air-gap ont lieu quand un opérateur le
décide, pas sur un minuteur.
**Justification :** préparer un média amovible est une procédure physique
supervisée ; un job non surveillé qui écrit sur un support que personne ne
regarde est un risque, pas un confort.

## Limites de plateforme et de déploiement

### Matrice des systèmes pris en charge

Le périmètre de production est volontairement étroit (NFR-018) :

| OS | Prise en charge |
|---|---|
| Linux (amd64/arm64) | Complète : service et poste de travail, paquets et image de conteneur |
| Windows | Parcours poste de travail mirror, validé en CI ; passthrough hors du périmètre validé |
| macOS | Niveau de confort : même build reproductible, aucun scénario de production validé |

**Conséquence :** exécutez les instances de production sous Linux ; n'utilisez
les builds macOS que pour l'évaluation et l'écriture de recipes. Le détail par
capacité est sur la page [plateformes prises en charge](../../reference/platforms/).

**winget peut avoir du retard sur la dernière release.** Chaque version de
`tobby-fetch.tobby` atteint `microsoft/winget-pkgs` par une pull request
relue par des personnes extérieures à ce projet, c'est pourquoi aucune release
n'automatise cette étape. **Conséquence :** si vous avez besoin d'une version
le jour de sa sortie, installez avec `scoop install tobby` (le
[bucket Scoop](https://github.com/tobby-fetch/scoop-bucket) se met à jour
seul) ou depuis l'archive de release, vérifiée comme décrit dans
[vérifier une release](../../project/verify-a-release/).

### Les médias FAT32 sont refusés

Le contrôle préalable refuse un média de transport au système de fichiers
incompatible, FAT32 explicitement parmi eux, en nommant la limite dans
l'erreur.
**Conséquence :** formatez les médias de transport avec un système de fichiers
capable de contenir de gros fichiers (exFAT, ext4, NTFS).
**Justification :** le plafond de 4 Gio par fichier de FAT32 tronquerait les
gros blobs en plein transfert ; refuser d'emblée vaut mieux qu'un store
corrompu à destination. Un système de fichiers dont ce build ne connaît pas le
plafond est signalé comme **non identifié**, jamais comme capable : le
contrôle préalable avertit et laisse le run se poursuivre plutôt que de
prétendre à une garantie qu'il n'a pas.

### Une seule réplique

Le déploiement de référence exécute une réplique avec une stratégie
`Recreate` ; il n'existe pas de topologie haute disponibilité.
**Conséquence :** une mise à niveau de Tobby implique une courte fenêtre
d'indisponibilité de l'instance, registry embarquée comprise.
**Justification :** le store exige un écrivain exclusif (ramasse-miettes sûr,
file de tâches, état cohérent). Tobby est un chemin de promotion, non la
registry de référence servant votre zone — et s'il l'est temporairement (le
cas d'usage de remplacement), planifiez les mises à niveau en conséquence.

## Des réglages par défaut à connaître

- **Le nettoyage du store de transit est désactivé par défaut en
  passthrough.** L'élagage aligné sur les retrievers existe (R-33) mais
  `sync.prune` vaut `false` sauf configuration : un store de transit n'est pas
  une unité de livraison, et le rafraîchir n'implique pas de le réduire.
  Définissez `storage.occupancyThreshold` pour que l'instance vous prévienne
  quand le store dépasse son volume — non défini signifie non surveillé, pas
  « dans les limites ».

Trois lacunes que cette page listait sont comblées : la vérification
d'intégrité du store à la demande avec un rapport horodaté (R-31, v0.6.0 —
[vérifier l'intégrité du store](../../passthrough/verify-store/)), l'analyse
de vulnérabilités avec politique bloquante ou consultative (6.1, v0.6.0 —
[analyse de vulnérabilités](../../security/vulnerability-scanning/)), et la
sauvegarde du répertoire d'état avec une procédure de reconstruction testée
de bout en bout (R-27, v1.0.0-rc.1 —
[sauvegarder et reconstruire une instance](../../passthrough/backup-rebuild/)).

Chaque fonctionnalité est suivie, avec son statut et son jalon, sur la page
[état du projet](../../discover/status/) — et le
[modèle de menaces](../../security/threat-model/) indique quels contrôles de
sécurité existent aujourd'hui et lesquels sont planifiés.
