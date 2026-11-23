---
title: Rapports d'acceptation
description: Les rapports bruts du crucible de chaque jalon livré, non retouchés, et les défauts que le filet a attrapés avant publication.
sidebar:
  order: 3
---

La définition de « terminé » de chaque jalon inclut un rapport d'acceptation
brut : le scénario du crucible rejoué sur un vrai nœud jetable, publié sans
retouche — chaque vérification listée, une ligne par affirmation, digests
compris. Les rapports se trouvent dans
[`docs/acceptance/`](https://github.com/tobby-fetch/tobby-fetch/tree/main/docs/acceptance)
dans le dépôt. Cette page en tient l'index.

Les cinq rapports de crucible du dépôt sont la sortie d'un **rejeu complet
de tous les jalons**, exécuté le 2026-08-27 pour l'acceptation de la v0.5.0
(commit `51112c3`), sur un hôte bare-metal jetable créé et détruit dans la
même session : chaque jalon a été rejoué plutôt que raisonné, ce qui fait de
la cascade une propriété et non un souvenir. Les fichiers des jalons 1 à 4
sont ce passage qui les reproduit ; le rejeu complet précédent date du
2026-08-22, pour la ré-acceptation de la v0.4.2. Un autre rejeu complet date
du 2026-11-07, pour la version de maintenance v0.5.1 : les mêmes 88
vérifications après le passage à Go 1.27 et à la dernière version de chaque
dépendance, dans un seul rapport. Le plus récent date du 2026-11-12, pour la
v0.6.0 : les jalons 1 à 5 de nouveau (les mêmes 88 vérifications) et les six
scénarios du jalon 6 (81 vérifications), sur le commit `4d8ad1b`. Le
dernier de tous date du 2026-11-23, pour la v0.7.0 : les jalons 1 à 6
de nouveau et les onze scénarios du jalon 7, couronne comprise.

## Les rapports

### [milestone-1-crucible-report.txt](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-1-crucible-report.txt) — fondations

Un nœud connecté et un nœud isolé sur le crucible Incus, un périphérique bloc
monté en boucle comme support. Vérifications : aller-retour de la registry
embarquée sous un nom relocalisé, canari de sortie qui échoue comme il le
doit (l'air-gap prouvé par construction), et aller-retour par le média — un
store rempli en zone connectée, rattaché physiquement, servant un contenu au
digest identique de l'autre côté de la coupure. Toutes les vérifications
passent.

### [milestone-2-crucible-report.txt](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-2-crucible-report.txt) — parcours utilisateur sécurisé

Une registry source et un nœud sécurisé. Vérifications : refus de démarrer
sans compte (code de sortie 3, `TBY-AUTH-001`), anonyme rejeté sur l'API
(RFC 9457) et sur la registry (défi Basic), import piloté entièrement par
`/api/v1` avec le digest épinglé préservé, un client tiers standard qui tire
avec des identifiants, et une tâche qui survit à un redémarrage par SIGKILL
sans aucun retransfert. Toutes les vérifications passent.

### [milestone-2-topology-report.txt](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-2-topology-report.txt) — niveau hermétique

Le même parcours sur la topologie CI hermétique (2026-08-12, poste de
développeur), idempotence comprise : le second import n'a déplacé aucun
octet. Son épilogue documente la séparation en deux niveaux : ce scénario
conteneurisé conditionne les fusions, le passage du crucible ci-dessus
conditionne le jalon.

### [milestone-3-crucible-report.txt](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-3-crucible-report.txt) — moteur de recipes

De vraies paires de clés cosign générées sur le nœud. Vérifications : une
recipe signée synchronisée avec ses digests épinglés intacts et un index
creux copié tel quel, une recipe signée par une clé étrangère refusée
(`TBY-SIG-001`) sans que rien ne soit déposé, zéro octet à la seconde
synchronisation, vérification des **deux** formats de signature cosign (le
bundle Sigstore de cosign 3.x et le tag classique), service de FileSet avec
requêtes par plage et traversée de chemin refusée, et cascade à deux sauts
avec la recipe inchangée. Toutes les vérifications passent.

### [milestone-4-crucible-report.txt](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-4-crucible-report.txt) — passthrough

L'instance déployée depuis le chart Helm de référence, une PKI privée émise
par le scénario, un proxy de sortie authentifié construit depuis le dépôt.
Vérifications : posture sécurisée par défaut et son assouplissement porteur
d'un bandeau, un cycle complet de promotion avec signatures revérifiées avant
le push, un second cycle idempotent, le changement d'intervalle à chaud
audité et conservé après redémarrage, une destination hors liste refusée
avant qu'un seul octet ne bouge — puis la même promotion depuis un nœud
scellé dont l'unique route passe par le proxy authentifié, en TLS sur PKI
privée. Toutes les vérifications passent.

### [milestone-5-crucible-report.txt](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-5-crucible-report.txt) — miroir et air-gap

Le transfert physique complet, sur un vrai périphérique bloc amovible, à
travers une coupure réseau qui n'a aucune route, et non une route
inutilisée. Trente-deux vérifications : un support sous-dimensionné refusé
avant tout transfert, avec le manque en octets et rien d'écrit ; un vrai
volume FAT32 identifié avec son plafond de 4 Gio par fichier ; aucune
synchronisation qui se déclenche d'elle-même en mode miroir ; un plan
exécuté qui laisse le store identique à l'octet près ; l'élagage exécuté
*avant* le manifeste pour que l'inventaire décrive ce que le support
contient au final ; un fichier d'identifiants déposé sous le store qui arrête
net l'instance — puis le voyage. De l'autre côté : rien n'est servi avant
vérification, un support adressé à une autre zone est refusé et la dérogation
de l'administrateur consignée, et l'arbitrage pour lequel ce jalon existe :
une livraison bloquée en entier, avec le nom du fichier en défaut, tandis que
sa voisine intacte est poussée. Puis la livraison validée retirée à
l'intérieur de la zone isolée par un client OCI standard, le journal de
retour sur le support hors du périmètre couvert par le manifeste, un support
remonté dans le temps refusé avec les deux horodatages nommés, `skopeo` qui
lit la disposition exportée, les guides qui répondent hors ligne dans les
deux langues, un répertoire empaqueté dans la zone et servi sous `/files/`
sans qu'aucune méthode d'écriture soit acceptée, et une réinitialisation
refusée sans sa confirmation saisie. Toutes les vérifications passent.

Un défaut produit a été trouvé ici et nulle part ailleurs : `tobby sync` ne
pouvait pas déclencher une instance en service, car il lisait l'enveloppe de
tâche de l'API comme une tâche nue. Sa propre suite de tests était verte :
l'instance factice qu'elle utilisait répondait ce que le client attendait,
non ce que l'API envoie.

### [release-0.5.1-crucible-report.txt](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/release-0.5.1-crucible-report.txt) — ré-acceptation de maintenance

Les jalons 1 à 5 rejoués de bout en bout pour la v0.5.1 — une version qui
change la chaîne d'outils et chaque dépendance plutôt qu'une fonctionnalité,
c'est-à-dire précisément le moment où il vaut mieux rejouer une cascade que
lui faire confiance. Les cinq rapports de scénario sont concaténés sans
retouche, 88 vérifications, toutes passantes. Le passage a aussi attrapé une
régression introduite par la version elle-même : le scénario m4 lisait le
journal du proxy de test sous une forme que la version venait de changer, ce
qui aurait aussi fait échouer le niveau CI hermétique. Son épilogue consigne
les deux défauts de préparation que la nouvelle image invitée a révélés,
corrigés avant le passage.

### Jalon 6 — contrôles durcis et identité d'entreprise

Exécuté le 2026-11-12 sur le commit `4d8ad1b`, sur un hôte bare-metal jetable
créé et détruit dans la même session. Les jalons 1 à 5 rejoués d'abord, 88
vérifications, un rapport chacun
([m1](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-6-regression-m1-crucible-report.txt),
[m2](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-6-regression-m2-crucible-report.txt),
[m3](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-6-regression-m3-crucible-report.txt),
[m4](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-6-regression-m4-crucible-report.txt),
[m5](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-6-regression-m5-crucible-report.txt)).
Puis six scénarios, 81 vérifications :

- [a-scanning](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-6-crucible-report-a-scanning.txt) :
  le binaire Trivy épinglé livré par le canal `tobby-system`, les politiques
  consultative et bloquante, la base qui franchit l'air-gap, et un binaire de
  scanner altéré sur le disque jamais exécuté — réparé à partir de la
  livraison vérifiée et audité (`scan.scanner.repaired`), ou refusé.
- [a-self-update](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-6-crucible-report-a-self-update.txt) :
  Tobby qui porte lui-même sa prochaine version à travers la coupure, la
  version signée par la clé du site poussable et celle signée seulement par la
  clé du projet bloquée.
- [b-auth](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-6-crucible-report-b-auth.txt) :
  OIDC et SAML contre un vrai Keycloak, rôles issus des groupes, compte local
  toujours utilisable quand le fournisseur d'identité est injoignable.
- [c-assurance](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-6-crucible-report-c-assurance.txt),
  [c-integrity](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-6-crucible-report-c-integrity.txt),
  [c-trust-scopes](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-6-crucible-report-c-trust-scopes.txt) :
  marqueurs d'assurance réduite portés jusqu'à la destination et au bordereau
  de transfert, contrôle d'intégrité du store, périmètres de confiance des
  deux côtés de la coupure.

Toutes les vérifications passent — avec une réserve. Le scénario a-scanning a
été corrigé sur le nœud pendant le passage : deux de ses sondes demandaient un
manifeste sans en-tête `Accept`, ce à quoi une registry OCI répond 404, de
sorte que la vérification « push consultatif déposé » échouait alors que
l'image était bien là, et que « image bloquée absente » passait à vide. Les
sondes envoient désormais le media type OCI ; le produit n'a pas été modifié.
Le premier passage, en échec, est publié à côté du passage réussi
([a-scanning-first-run-FAILED](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-6-crucible-report-a-scanning-first-run-FAILED.txt)).

Un passage antérieur le même jour, sur le commit `0bc893e`, a fait échouer
trois des six scénarios et trouvé deux défauts produit que rien d'autre
n'avait vus : une recipe admise par un périmètre `allowUnsigned` refusée au
push quand l'hôte de son cookbook porte un port (B-055), et aucun chart publié
par `helm push` qui puisse être synchronisé, parce que helm omet un champ que
les fixtures de test écrivaient toujours (B-056). Les deux sont corrigés en
v0.6.0, chacun avec un test qui échouait avant.

### Jalon 7 — finitions, documentation et recette (v0.7.0)

Exécuté le 2026-11-23 sur des hôtes bare-metal jetables, chacun créé et
détruit dans la même session. Les jalons 1 à 6 rejoués d'abord sur le
commit `c8d1b13`, 175 vérifications, un rapport chacun
([m1](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-regression-m1-crucible-report.txt),
[m2](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-regression-m2-crucible-report.txt),
[m3](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-regression-m3-crucible-report.txt),
[m4](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-regression-m4-crucible-report.txt),
[m5](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-regression-m5-crucible-report.txt),
et les six scénarios du jalon 6: [a-scanning](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-regression-m6-crucible-report-a-scanning.txt), [a-self-update](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-regression-m6-crucible-report-a-self-update.txt), [b-auth](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-regression-m6-crucible-report-b-auth.txt), [c-assurance](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-regression-m6-crucible-report-c-assurance.txt), [c-integrity](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-regression-m6-crucible-report-c-integrity.txt), [c-trust-scopes](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-regression-m6-crucible-report-c-trust-scopes.txt)).
Puis les onze scénarios du jalon 7 sur le commit `e9ea100`, 200
vérifications :

- [a-consumption-aids](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-crucible-report-a-consumption-aids.txt) : la table de correspondance et le
  `registries.yaml` généré, identiques depuis l'API et la ligne de
  commande, octet pour octet, et les règles de réécriture qui atteignent
  chaque digest épinglé.
- [b-clock](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-crucible-report-b-clock.txt), [b-diagnostics](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-crucible-report-b-diagnostics.txt), [b-rebuild](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-crucible-report-b-rebuild.txt),
  [b-supervision](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-crucible-report-b-supervision.txt) : une horloge invraisemblable signalée sans rien
  corriger, un bundle de diagnostic qui ne laisse fuir aucun des secrets
  posés, une instance reconstruite sur un nœud neuf depuis son répertoire
  d'état, et les règles d'alerte et le tableau de bord testés contre un vrai
  Prometheus.
- [c-emergency-path](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-crucible-report-c-emergency-path.txt), [c-exceptions](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-crucible-report-c-exceptions.txt), [c-provenance](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-crucible-report-c-provenance.txt) :
  la procédure d'urgence suivie à la lettre depuis sa page, des dérogations
  ciblées et bornées qui rebloquent visiblement à leur échéance, et la
  provenance de chaque dépôt.
- [d-hardening](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-crucible-report-d-hardening.txt) : les interrupteurs de durcissement face à de vrais
  clients Docker, Podman, Helm, ORAS et skopeo, le CSRF et les en-têtes de
  sécurité.
- [e-docs-offline](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-crucible-report-e-docs-offline.txt) : la documentation bilingue servie par une instance
  dans la zone isolée, chaque page, lien et code d'erreur.
- [z-crown-rke2](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-7-crucible-report-z-crown-rke2.txt) : la couronne — un cluster RKE2 installé sur un nœud
  Rocky Linux 9 nu, SELinux en mode enforcing, uniquement avec ce que Tobby
  a fait traverser, sans aucun paquet sorti de la zone isolée.

Toutes les vérifications passent, avec une précision. Sur le premier nœud
du jour, les deux scénarios qui démarrent une machine virtuelle (b-clock et
la couronne) ont échoué : la VM n'a pas démarré. Le journal QEMU affiché par
les scénarios en a donné la cause — le processeur de cet hôte loué n'adresse
que 36 bits de mémoire physique, trop peu pour les machines virtuelles — et
les onze scénarios du jalon 7 ont été rejoués sur un second hôte, dont les
rapports sont ceux publiés ici. Le premier
passage complet du jalon 7 n'a révélé que des défauts de scénario — une
image de VM qui exige Secure Boot désactivé, un paquet de distribution qui
n'existe pas, une variable de digest écrasée, un dépôt RPM vidé par `dnf` —
corrigés dans les scénarios ; le produit n'a pas été modifié pour eux. Les
deux revues adversariales du jalon, avant cette recette, ont trouvé et
corrigé neuf défauts du produit (B-063 à B-071).

### [milestone-4-quality-audit.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/docs/acceptance/milestone-4-quality-audit.md) — audit à un instant donné

Mené le 2026-08-21 sur la v0.4.1, entre le jalon 4 et le jalon 5 : huit
dimensions de revue plus l'outillage, puis un tour contradictoire où chaque
constat de sévérité haute était confié à un relecteur indépendant chargé de le
réfuter. Quatre constats de sévérité haute confirmés (deux défauts de
concurrence, un historique de tâches non borné, un défaut de digest côté
spécification), chacun prouvé par exécution ; tous corrigés en v0.4.2, le
reste explicitement reporté avec un responsable. Les constats sont publiés
avec la même franchise que les rapports verts.

## B-014 et B-015 — la preuve que le filet attrape

L'intérêt d'un système de preuve à deux niveaux est ce qu'il trouve. L'en-tête
du jalon 4 dans la feuille de route le consigne sans détour : **deux défauts
ont été trouvés par le crucible et corrigés avant publication.**

- **B-014** — les périmètres de confiance étaient comparés à deux espaces de
  motifs différents sur les deux moitiés d'une promotion. Sur toute registry
  portant un port, un périmètre correctement écrit admettait une recipe à
  l'import, puis la refusait avant le push avec `TBY-SIG-001`. Invisible aux
  tests unitaires : les deux chemins de code étaient testés séparément, et
  sans port. Trouvé en rejouant le scénario du crucible du jalon 4.
- **B-015** — une signature au format bundle Sigstore (le défaut de cosign
  3.x) était vérifiée dans la zone qui l'avait récupérée et disparaissait un
  saut plus bas : le vérificateur avait appris les deux formats au jalon 3, le
  chemin de *copie* n'avait appris que l'ancien, si bien qu'une zone aval
  refusait un contenu que sa zone amont avait accepté. Trouvé en rejouant le
  scénario du crucible du jalon *3* pendant l'acceptation du jalon 4 — la
  raison pour laquelle chaque scénario livré reste exécutable pour toujours.

Aucun des deux défauts n'était observable sous le niveau du crucible : tous
deux exigeaient de vraies registries, de vraies signatures et une vraie
topologie multi-sauts. Les deux correctifs sont parus en v0.4.0 avec des
tests de non-régression d'abord exécutés contre le code non corrigé, et le
changelog les nomme avec leurs causes racines.

La construction des scénarios et des rapports est décrite dans
[tests et preuves](../tests-and-proofs/) ; le lien entre chaque train de
versions et un jalon est dans
[versions et compatibilité](../release-compatibility/).
