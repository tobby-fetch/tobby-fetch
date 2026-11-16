---
title: Contrôle des critères de version
description: Un script qui vérifie chaque critère de la version 1.0.0 à partir de preuves réelles et imprime une liste de contrôle, avec la preuve ou la raison de son impossibilité.
sidebar:
  order: 7
---

La version 1.0.0 est conditionnée par onze critères. Personne ne devrait avoir
à croire sur parole une case cochée : un script contrôle chacun d'eux à partir
de preuves qui existent — les rapports d'acceptation bruts, les verdicts de la
CI et du pipeline de release, les artefacts publiés, l'arborescence taguée :

```bash
mise run release-criteria -- --version v1.0.0-rc.1
# ou : go run ./tools/release-criteria --version v1.0.0-rc.1
```

Lancez-le depuis un clone qui contient le tag (`git fetch --tags`), avec
[`gh`](https://cli.github.com/) authentifié. Chaque critère affiche `OK`, `KO`
ou `NON VÉRIFIABLE ICI`, suivi de la preuve (nom d'un rapport, URL d'un job de
CI, verdict du vérificateur) ou de la raison. Le code de sortie est non nul
quand un critère vérifiable est `KO`.

## Ce que vérifie chaque critère

| Critère | Preuve |
|---|---|
| Les deux modes validés de bout en bout et acceptés | **Non vérifiable par un script** : la preuve est le procès-verbal de recette signé de la release candidate. |
| Suite crucible verte, couronne comprise | Pour les jalons 1 à 7, les rapports bruts de `docs/acceptance/` *au tag* : chacun se termine par sa ligne `scenario … complete` et ne contient aucune ligne `FAIL`. Les jalons 6 et 7 doivent avoir un rapport pour chaque script de scénario, et le jalon 7 le scénario couronne (`z-crown-rke2`) avec le pied de rapport attestant la destruction du nœud d'acceptation. |
| Conformité OCI | Les jobs de CI `conformance` (linux/amd64 et linux/arm64) du run sur le commit tagué. |
| SLSA L3 et cosign | Les commandes de [Vérifier une version](../verify-a-release/), exécutées sur les artefacts publiés avec une configuration Docker vide (accès anonyme aux registries) : `slsa-verifier` sur chaque binaire, paquet et image, `cosign verify-blob` sur les SBOM, `cosign verify` sur l'image, le chart et le FileSet. |
| Aucune CVE critique ou haute | Le job `Image CVE gate (Trivy)` du run du pipeline de release pour le tag. |
| Couverture de 70 % sur les paquets cœur | Le job de CI du plancher de couverture. |
| e2e sur les deux architectures et Windows | Les jobs de topologie hermétique sur linux/amd64 et linux/arm64, et la passe de tests Windows. |
| Documentation en anglais et en français | Chaque page anglaise a sa page française au tag, et le site se construit. |
| i18n | Le job de CI `i18n completeness`. |
| Quatre méthodes d'authentification | Les tests nommés d'OIDC, de SAML, des jetons statiques et de l'authentification basic existent au tag, et le job de tests de la CI est vert. |
| Spécification du format `v1` | Un tag `v1` dans `tobby-fetch/recipe-spec`. Sur une release candidate, ce critère est attendu `KO` (**attendu à la 1.0**) et ne fait pas échouer l'exécution. |

## Honnête sur ce qu'il ne voit pas

Un outil absent n'est jamais un `OK`. Sans `slsa-verifier`, `cosign`, `crane`
ou `gh`, les lignes concernées affichent `NON VÉRIFIABLE ICI` avec la commande
exacte à lancer ailleurs. La construction du site ne s'exécute que si
l'arborescence de travail est au commit tagué ; sinon le script le dit.
`--quick` ne vérifie que le binaire linux/amd64, pour un coup d'œil rapide.

Les rapports sont lus dans l'arborescence taguée, pas dans l'arborescence de
travail : le script atteste ce que la version contient réellement. Voir aussi
[Rapports d'acceptation](../acceptance-reports/).
