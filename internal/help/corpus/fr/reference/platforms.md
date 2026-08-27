---
title: Plateformes supportées
description: La matrice de fonctionnalités par système d'exploitation — ce qui est validé sous Linux, sous Windows et sous macOS, comment chacun est vérifié, et les comportements propres à chaque plateforme qu'un exploitant doit connaître.
sidebar:
  order: 7
---

Tobby est livré sous forme d'un binaire unique lié statiquement pour Linux
et Windows, en amd64 et arm64, plus des binaires macOS en tier de confort
(SRS NFR-001). « Fonctionne partout » et « **validé** partout » sont deux
affirmations différentes, et cette page les tient séparées.

Trois mots sont employés ci-dessous, et ils veulent dire exactement ceci :

- **Validé** — un scénario d'exploitation de bout en bout tourne sur cette
  plateforme en intégration continue, et le comportement est supporté en
  production.
- **Testé** — la suite unitaire et d'intégration tourne sur cette
  plateforme en intégration continue, sous détecteur de course, deux fois
  par exécution.
- **Compile** — la cible se compile et le binaire démarre. Rien de plus
  n'est affirmé.

## Matrice

| Capacité | Linux | Windows | macOS |
|---|---|---|---|
| Mode miroir — synchronisation manuelle (FR-014) | Validé | **Validé** | Testé |
| Store transportable auto-porteur (FR-050) | Validé | **Validé** | Testé |
| Journal d'opération sur le store de transport (FR-053) | Validé | **Validé** | Testé |
| Manifeste de support et sa vérification (FR-054) | Validé | **Validé** | Testé |
| Opération côté destination, `tobby media` (FR-052) | Validé | **Validé** | Testé |
| Export / import OCI image layout (FR-051) | Validé | **Validé** | Testé |
| Registry OCI embarquée sur `/v2/` (FR-040) | Validé | **Validé** | Testé |
| Interface web et API REST (FR-062, FR-061) | Validé | **Validé** | Testé |
| Service de FileSets sous `/files/` (FR-047) | Validé | Validé, avec une réserve ci-dessous | Testé |
| Packing de FileSet (FR-048) | Validé | Testé | Testé |
| Mode passthrough — promotion continue (FR-013) | Validé | Compile | Testé |
| Recipes signées, vérification cosign (FR-033) | Validé | Testé | Testé |
| Transport sur périphérique bloc amovible réel | Validé | Simulé en CI | Non couvert |
| Image conteneur (`ghcr.io/tobby-fetch/tobby-fetch`) | Validé | — | — |
| Paquets Linux (`.deb`, `.rpm`, `.apk`) | Validé | — | — |

Le mode passthrough est livré et validé comme un service Linux
conteneurisé. Le binaire Windows unique sait le faire tourner, mais le
passthrough sous Windows est hors du périmètre validé v1.0.0 (NFR-018) :
une zone connectée en permanence est un serveur, et l'histoire serveur de
Tobby est Linux.

macOS est un tier de confort. La suite complète tourne sur des runners
macOS et les binaires passent par la même chaîne de release reproductible,
avec SBOM et provenance, mais aucun scénario d'exploitation de bout en
bout n'y est validé et aucun support de production n'est impliqué.

## Comment chaque plateforme est vérifiée

L'intégration continue joue la suite entière — `go test -race -count=2` —
sur `ubuntu-latest`, `ubuntu-24.04-arm`, `macos-latest` et
`windows-latest`, et chaque job bloque les fusions. Au-delà :

- **Linux** porte les scénarios de topologie hermétiques et la suite de
  non-régression navigateur, et c'est la plateforme sur laquelle tourne le
  crucible d'acceptation — y compris la seule chose qu'aucun runner
  d'intégration continue ne sait faire : écrire sur un vrai périphérique
  bloc amovible et le porter entre deux réseaux isolés.
- **Windows** joue le parcours UC2 de bout en bout : une synchronisation
  miroir produit un store, le store est copié vers un chemin qu'il n'a
  jamais occupé (le transport, simulé — un runner hébergé n'a pas de
  périphérique amovible), et une instance côté destination le vérifie puis
  pousse son contenu, à digests identiques d'un bout à l'autre. Le runner
  attache également un **vrai** volume FAT32, pour que le contrôle
  préalable de taille de fichier de FR-055 s'exerce contre le système de
  fichiers pour lequel il existe, et non contre une doublure.
- **macOS** joue la même suite unitaire et d'intégration, et crée une
  vraie image disque FAT32 pour le même contrôle préalable.

## Spécificités Windows

### Installation

Les binaires Windows sont portables : un unique `.exe`, sans dépendance
d'exécution et sans installeur.

Téléchargez `tobby-windows-amd64.exe` ou `tobby-windows-arm64.exe` depuis la
[page des releases](https://github.com/tobby-fetch/tobby-fetch/releases) et
vérifiez-le comme décrit dans
[Vérifier une release](../../project/verify-a-release/). C'est le seul canal
d'installation aujourd'hui.

Deux canaux de gestionnaire de paquets portent les mêmes binaires. Les deux
manifestes sont produits par le workflow de release depuis les
`SHA256SUMS` de cette exécution — chacun épingle donc l'artefact exact de
sa release — et joints à la release en assets.

**[Scoop](https://scoop.sh/) est publié**, et installe par utilisateur,
sans droits d'administrateur — souvent le seul type d'installation qu'un
poste managé autorise :

```powershell
scoop bucket add tobby https://github.com/tobby-fetch/scoop-bucket
scoop install tobby
```

**[winget](https://learn.microsoft.com/windows/package-manager/) est en
cours de revue.** Le jeu de manifestes de `tobby-fetch.tobby` est soumis à
`microsoft/winget-pkgs` ; tant qu'il n'est pas fusionné, `winget install
tobby` ne fonctionne pas. Cette dernière étape est une pull request sur un
index communautaire relu par des gens extérieurs au projet, ce qui est la
raison pour laquelle aucune release ne l'automatise.

### Les permissions sont une liste d'accès, pas des bits de mode

Les fichiers portant du secret — identifiants de registry, clés privées
TLS, base des comptes locaux, jetons statiques — sont créés accessibles au
seul propriétaire (NFR-020). Sous Unix c'est le mode `0600`. Sous Windows
les bits de mode ne portent rien : Windows projette le bit d'écriture sur
l'attribut lecture seule et jette le reste, donc un fichier « créé 0600 »
y serait lisible par tous les comptes que le répertoire parent admet — ce
qui, dans un déploiement joint à un domaine, en fait beaucoup.

Ce qui applique la règle sous Windows, c'est la liste de contrôle d'accès
discrétionnaire, remplacée intégralement par une entrée unique nommant le
propriétaire du fichier lui-même, et marquée *protégée* pour que les
entrées héritables du répertoire parent ne soient pas réintégrées
par-dessus. Le propriétaire est lu sur l'objet plutôt que supposé être le
processus courant : un fichier restauré depuis une sauvegarde, ou créé par
un installeur tournant sous un autre compte, reste lisible par exactement
un compte — celui qui le possède.

Une conséquence à connaître : copier un tel fichier vers un hôte Unix, ou
sur un support FAT32, n'emporte pas la liste d'accès. Les secrets ne sont
pas censés voyager du tout (NFR-020) — Tobby refuse de démarrer quand un
chemin de secret configuré se résout sous la racine du store — mais une
sauvegarde prise avec un outil qui ignore les listes d'accès est une autre
affaire.

### FAT32 et le plafond de 4 Gio

Une clé USB formatée sur un poste Windows est très souvent en FAT32, et
FAT32 stocke une longueur de fichier sur 32 bits : aucun fichier ne peut
atteindre 4 Gio. Tobby identifie le système de fichiers du store et de la
cible d'export avant qu'une opération commence, et refuse celui qui ne
peut pas contenir le plus gros fichier qu'elle écrirait, en nommant la
limite (FR-055). Formatez le support en exFAT ou NTFS dès qu'un blob ou
une archive d'export peut dépasser 4 Gio.

### Liens symboliques dans les FileSets

Extraire un FileSet contenant un lien symbolique exige le privilège
`SeCreateSymbolicLinkPrivilege`, que Windows accorde aux administrateurs
et à personne d'autre par défaut. Un compte qui ne l'a pas obtient un
refus nommant le privilège ; accordez-le via *Créer des liens symboliques*
dans la stratégie de sécurité locale, ou activez le mode Développeur. Les
FileSets sans lien symbolique ne sont pas concernés.

### Chemins longs

Le store imbrique son contenu sous des répertoires dérivés des digests, ce
qui reste largement en deçà de la limite de 260 caractères pour toute
racine de store raisonnable. Une racine elle-même profondément imbriquée
peut malgré tout l'atteindre ; activez le support des chemins longs
(`HKLM\SYSTEM\CurrentControlSet\Control\FileSystem\LongPathsEnabled`) ou
gardez le store plus près de la racine de son volume.

### Arrêt gracieux

`SIGTERM` n'est jamais levé par le noyau Windows. Tobby draine
gracieusement sur Ctrl+C et Ctrl+Attn dans une console ; un arrêt qui
contourne la console — `taskkill /F`, l'arrêt d'un service — termine le
processus sans drainage. Une synchronisation interrompue n'est jamais
perdue : elle reste en cours sur disque et reprend au démarrage suivant
(FR-029).

## Ce qui n'est délibérément pas couvert

L'honnêteté sur les trous fait partie de la matrice :

- **Un vrai périphérique bloc amovible sous Windows.** L'étape de
  transport est une copie de répertoire en intégration continue. Le vrai
  périphérique — monté, rempli, démonté, porté — est exercé sous Linux par
  le crucible d'acceptation.
- **Les contrôles clients OCI tiers (FR-076) sous Windows et macOS.** Ils
  exigent un démon Docker Linux et un moyen d'y installer une autorité de
  confiance privée ; ils tournent sur les runners Linux et sont ignorés,
  nommément, ailleurs.
- **Le mode passthrough sous Windows**, comme dit plus haut.
- **Les bits de permission exacts d'un FileSet extrait sous Windows.**
  L'extraction préserve ce que la plateforme sait exprimer ; Windows
  n'exprime ni le bit setuid ni un mode à trois chiffres, donc ces
  assertions ne tournent que sous Unix.
