---
title: Correspondance de conformité
description: Comment Tobby répond à SLSA, NIS2, NIST SSDF et OWASP SCVS — exigence, mécanisme, preuve, statut.
sidebar:
  order: 5
---

Cette page fait correspondre des familles d'exigences de quatre référentiels
au mécanisme Tobby qui y répond et à la preuve que vous pouvez contrôler. Il
s'agit d'une **correspondance, non d'une revendication de certification** :
aucun de ces référentiels ne certifie un produit, et Tobby ne revendique aucun
label. Utilisez les lignes comme matière première pour un dossier
d'homologation ou d'audit ; vérifiez chaque preuve vous-même — c'est le rôle
de la [page de vérification](../verify-a-release/).

Statuts : **delivered** (dans la release courante, preuve disponible) ou
**upcoming** (avec son jalon — suivez-les sur l'[état du projet](../../discover/status/)).

## SLSA v1.0

SLSA v1.0 définit les niveaux de build L0 à L3. Tobby vise et revendique le
**Build L3 — le plus haut niveau défini — plus la reproductibilité par-dessus**.
Les documents qui citent un « L4 » se réfèrent au brouillon v0.1 abandonné
([ADR-0011](../../reference/srs-adr/)).

| Exigence | Mécanisme Tobby | Preuve | Statut |
|---|---|---|---|
| Provenance générée par un builder isolé du workflow du projet | Workflows réutilisables du trusted builder `slsa-github-generator` (binaires et image), référencés par tag comme l'exige le vérificateur | [release.yml](https://github.com/tobby-fetch/tobby-fetch/blob/main/.github/workflows/release.yml), ADR-0011 | delivered (depuis v0.1.0) |
| Provenance disponible et vérifiable par les consommateurs | Commandes `slsa-verifier` exactes intégrées aux notes de chaque release et à la documentation | [Vérifier une release](../verify-a-release/) | delivered |
| Au-delà de L3 : builds reproductibles | Reconstruction bit à bit imposée par une gate de double build indépendant avant toute publication ; chemin de reconstruction côté consommateur documenté | job `verify-reproducibility` de release.yml ; [reconstruction indépendante](../verify-a-release/) | delivered |

## NIST SSDF (SP 800-218)

| Groupe de pratiques | Mécanisme Tobby | Preuve | Statut |
|---|---|---|---|
| PO — Préparer l'organisation | Chaîne d'outils épinglée (`mise.toml`), trunk protégé avec relecture obligatoire, DCO sur chaque commit, politique de sécurité publiée | [CONTRIBUTING.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/CONTRIBUTING.md), [SECURITY.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/SECURITY.md) | delivered |
| PS — Protéger le logiciel | Releases signées, SHA256SUMS sous provenance SLSA, actions épinglées par SHA de commit complet, outils tiers vérifiés par somme de contrôle | release.yml, [ci.yml](https://github.com/tobby-fetch/tobby-fetch/blob/main/.github/workflows/ci.yml) | delivered |
| PW — Produire un logiciel bien sécurisé | Exigences numérotées et testables (SRS) ; lint strict sans aucune suppression ; détecteur de race et double exécution anti-flaky ; seuil de couverture par paquet ; analyse de secrets sur tout l'historique | [Tests et preuves](../tests-and-proofs/), [SRS](../../reference/srs-adr/) | delivered ; fuzzing des parseurs et revue de validation d'entrées upcoming (jalon 7) |
| RV — Répondre aux vulnérabilités | Trivy et govulncheck comme gates de merge et de release ; reconstruction hebdomadaire de l'image derrière une gate Trivy ; divulgation coordonnée avec versions correctives | ci.yml, [weekly-rebuild.yml](https://github.com/tobby-fetch/tobby-fetch/blob/main/.github/workflows/weekly-rebuild.yml), SECURITY.md | delivered |

## OWASP SCVS

| Famille de contrôles | Mécanisme Tobby | Preuve | Statut |
|---|---|---|---|
| V1 — Inventaire | Un SBOM par binaire de release, plus des SBOM natifs d'image générés au build (non reconstruits par un scanner) | assets de release ; ADR-0011 | delivered |
| V2 — SBOM | CycloneDX, signé cosign, commande de vérification publiée | [Vérifier une release](../verify-a-release/) | delivered |
| V3 — Environnement de build | Trusted builder isolé, actions épinglées par SHA, gate de reproductibilité, build d'image déclaratif (melange+apko, sans Dockerfile) | release.yml, ADR-0011 | delivered |
| V4 — Gestion des paquets | Graphe de modules verrouillé (`go.sum`, installations vérifiées par sumdb), mises à jour pilotées par renovate, outils téléchargés épinglés par somme de contrôle | racine du dépôt, ci.yml | delivered |
| V5 — Analyse des composants | Trivy (critical/high bloquent), govulncheck avec atteignabilité au niveau du symbole, gate de liste d'autorisation des licences de dépendances | jobs ci.yml `vulnerabilities`, `govulncheck`, `licenses` | delivered |
| V6 — Pedigree et provenance | Provenance SLSA L3 par artefact, signature DCO par commit, changelog nommant les défauts et leurs causes racines | release.yml, [dco.yml](https://github.com/tobby-fetch/tobby-fetch/blob/main/.github/workflows/dco.yml), [CHANGELOG](https://github.com/tobby-fetch/tobby-fetch/blob/main/CHANGELOG.md) | delivered |

## NIS2 (directive 2022/2555)

NIS2 réglemente des **entités**, non des produits logiciels. Ces lignes font
correspondre les familles de mesures de l'article 21(2) qu'un opérateur
réglementé doit mettre en œuvre à ce que Tobby apporte à cet opérateur — ou
s'applique à lui-même en tant que fournisseur dans la chaîne
d'approvisionnement de l'opérateur.

| Famille de mesures | Mécanisme Tobby | Preuve | Statut |
|---|---|---|---|
| Sécurité de la chaîne d'approvisionnement (21(2)(d)) | Vérification à l'entrée : signatures cosign contre les trust roots propres à la destination, digests épinglés, liste d'autorisation des registries ; releases de Tobby vérifiables de bout en bout | [Confiance dans le contenu](../../security/content-trust/), [Vérifier une release](../verify-a-release/) | delivered |
| Traitement et divulgation des vulnérabilités (21(2)(e)) | Processus CVD publié avec versions correctives pour Tobby lui-même ; analyse de CVE pilotée par politique du contenu que Tobby déplace, avec base de données actualisable hors ligne | SECURITY.md ; analyse : **upcoming (jalon 6)** | mixte — voir les cellules |
| Cryptographie (21(2)(h)) | Vérification Sigstore/cosign, stockage des identifiants en argon2id, TLS partout avec prise en charge d'une CA privée et sans « skip verify » global | [Fiche sécurité](../../security/one-pager/), audit du jalon 4 (« zéro `InsecureSkipVerify` ») | delivered |
| Contrôle d'accès (21(2)(i)) | Authentification activée par défaut, RBAC à trois rôles sur l'interface, l'API et la registry, invariant du dernier administrateur ; identité d'entreprise (OIDC, SAML) | [Authentification et RBAC](../../security/auth-rbac/) ; OIDC/SAML : **upcoming (jalon 6)** | mixte — voir les cellules |
| Journalisation et éléments de preuve d'incident (appuie 21(2)(b)) | Journal d'audit de sécurité à schéma stable, identifiant de run de bout en bout ; la piste d'audit franchit l'air gap sur le média | [Journal d'audit](../../security/audit-log/), [tracer un transfert](../../air-gap/traceability/) | delivered |

## Lire le tableau honnêtement

Deux choses que cette page ne fera pas : revendiquer une certification, et
cacher une lacune derrière une ligne verte. Lorsqu'un mécanisme n'est livré
qu'en partie, la ligne dit laquelle. La [page des limites](../../discover/limits/)
dit ce que Tobby ne fait pas du tout — pas de qualification amont, pas de
purge de la destination, aucune signature — parce qu'une correspondance qui
omet son espace négatif relève du marketing, et cette section s'adresse aux
auditeurs.
