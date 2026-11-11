---
title: Mettre à jour Tobby en zone isolée
description: Tobby traverse l'air gap comme les logiciels qu'il transporte — une recipe de son image, de son chart et de ses binaires, signée par votre site, portée par le flux miroir ordinaire, déployée par une personne. Aucune mise à jour automatique.
sidebar:
  order: 8
---

Tobby se met à jour par son propre canal (R-25), exactement comme la
[base de vulnérabilités](../vulnerability-database/) : une nouvelle version
est une **recipe** que votre site cuisine et signe, portée par le flux
miroir standard, et déployée par une personne. Rien dans Tobby ne cherche
une version plus récente, et rien n'en installe une de lui-même (NFR-019).

## Ce que publie une release

Depuis la v0.6.0, chaque release publie trois artefacts prêts pour une
recipe, signés sans clé par le workflow de release et couverts par une
provenance SLSA :

| Artefact | Référence | Type dans la recipe |
|---|---|---|
| image conteneur (amd64, arm64) | `ghcr.io/tobby-fetch/tobby-fetch:vX.Y.Z` | `ContainerImage` |
| chart Helm de référence | `ghcr.io/tobby-fetch/charts/tobby:X.Y.Z` | `HelmChart` |
| binaires statiques (linux et windows, amd64 et arm64) | `ghcr.io/tobby-fetch/binaries/tobby:vX.Y.Z` | `FileSet` — un index, un fichier par plateforme, identique octet pour octet à l'asset de la release |

[`examples/tobby.yaml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/examples/tobby.yaml)
est la recipe qui les emporte, avec des digests à remplacer.

## La cuisiner, la signer avec votre chaîne

1. En zone connectée, vérifiez la release comme l'indique la page de
   vérification d'une release : `cosign verify` sur l'image, le chart et
   l'index des binaires, et `slsa-verifier` sur la provenance.
2. Copiez `examples/tobby.yaml`, fixez `metadata.version` et les trois
   versions, et remplacez chaque digest par celui qu'indiquent les notes de
   release (ou `crane digest <ref>:<version>`).
3. **Signez la recipe cuisinée avec votre clé de qualification** et
   publiez-la dans votre cookbook. La destination fait confiance à ses
   propres trust roots : une recipe signée par la seule clé du projet y est
   refusée — Tobby ne signe rien, et votre site admet une release de Tobby
   comme n'importe quel contenu (invariant n° 1).
4. Ajoutez-la au Retriever de la zone, à côté de `tobby-system`.

## La transporter, la déployer

5. Synchronisez l'instance miroir (`tobby sync --wait`) : la nouvelle
   version est écrite sur le support avec tout le reste.
6. Côté isolé, `tobby media verify` la montre poussable — ou bloquée sur sa
   signature si elle n'a pas été signée par une clé de confiance de la
   zone — et `tobby media import` la pousse dans la registry de zone.
7. **Déployez-la vous-même** : `helm upgrade` avec le chart et l'image
   désormais dans la registry de zone, ou extrayez le binaire de votre
   plateforme du FileSet et remplacez celui de l'hôte. Lisez d'abord le
   changelog de la release : une version mineure peut demander un
   changement de configuration.

## Garder l'index entier

Listez les quatre plateformes du FileSet des binaires, comme le fait
l'exemple. Un sous-ensemble rend l'index copié *partiel* (FR-022), et une
registry de zone qui valide les membres d'un index en refuse un partiel —
une instance connectée le complète en allant chercher le reste, une
instance isolée ne le peut pas. Les enfants en plus coûtent quelques
dizaines de mégaoctets.
