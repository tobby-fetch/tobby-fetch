---
title: Vérifier une version
description: Sept façons indépendantes de vérifier une version de Tobby, en commençant par celle qui fonctionne sans aucune infrastructure en ligne.
sidebar:
  order: 1
---

Chaque version est livrée avec une provenance SLSA Build L3, des SBOM
CycloneDX signés par cosign, une image de conteneur multi-architecture
signée, des binaires reproductibles au bit près et des paquets Linux
reproductibles au bit près construits à partir d'eux
([ADR-0011](../../reference/srs-adr/)). Aucune de ces affirmations ne demande
votre confiance : chacune est vérifiable, et cette page montre comment.
Remplacez `v1.0.0` par la version que vous contrôlez.

Outils : [slsa-verifier](https://github.com/slsa-framework/slsa-verifier)
v2.7.1 ou ultérieur, et [cosign](https://github.com/sigstore/cosign) 3.x. La
reconstruction indépendante n'a besoin ni de l'un ni de l'autre — seulement de
Git et de Go.

## 1. Reconstruction indépendante — la garantie air-gap

Les binaires et les paquets Linux sont reproductibles au bit près : le même
tag se reconstruit vers le même SHA-256. Un consommateur sans accès à
l'infrastructure Sigstore ou GitHub — poste de revue isolé compris — vérifie
une version en la reconstruisant et en comparant les digests.
`tools/release-build.sh` est exactement le chemin de code que le pipeline de
release exécute (CGO désactivé, `-trimpath`, `-buildvcs=false`,
`SOURCE_DATE_EPOCH` tiré du commit, toolchain forcée à la version de
`go.mod`), et `tools/package-build.sh` de même pour les paquets (nfpm à la
version `NFPM_VERSION` épinglée dans
[`.github/workflows/release.yml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/.github/workflows/release.yml)) :

```bash
git clone https://github.com/tobby-fetch/tobby-fetch && cd tobby-fetch
git checkout v1.0.0

epoch=$(git log -1 --format=%ct)
SOURCE_DATE_EPOCH=$epoch sh tools/release-build.sh \
  "v1.0.0" "$(git rev-parse HEAD)" \
  "$(date -u -d "@$epoch" +%Y-%m-%dT%H:%M:%SZ)" dist

# optional — also reproduces the .deb/.rpm/.apk packages
go install github.com/goreleaser/nfpm/v2/cmd/nfpm@<NFPM_VERSION>
SOURCE_DATE_EPOCH=$epoch sh tools/package-build.sh "v1.0.0" dist dist

# compare against the SHA256SUMS file attached to the release
# (--ignore-missing: skip the package lines if you skipped nfpm)
(cd dist && sha256sum --ignore-missing -c /path/to/SHA256SUMS)
```

Sortie attendue : une ligne `OK` par artefact. Toute différence signifie que
le binaire publié n'a pas été produit à partir des sources taguées. Le
pipeline de release applique cette propriété à lui-même : un second job de
build indépendant doit reproduire les digests du premier avant qu'une version
puisse être publiée.

## 2. Binaires : provenance SLSA

Téléchargez le binaire et `tobby.intoto.jsonl` depuis la page de la version,
puis :

```bash
slsa-verifier verify-artifact tobby-linux-amd64 \
  --provenance-path tobby.intoto.jsonl \
  --source-uri github.com/tobby-fetch/tobby-fetch \
  --source-tag v1.0.0
```

La sortie attendue se termine par `PASSED: SLSA verification passed`. Cela
prouve que le binaire a été construit par le builder de confiance isolé
slsa-github-generator, à partir de ce dépôt, à ce tag exact — un workflow du
dépôt compromis n'aurait pas pu le forger.

## 3. Paquets Linux : provenance, puis installation hors ligne

Les paquets `.deb`, `.rpm` et `.apk` enveloppent exactement le binaire de
release `tobby-linux-<arch>` (installé dans `/usr/bin/tobby`, sans script
d'installation), figurent dans `SHA256SUMS` et sont sujets de la même
provenance. Vérifiez un paquet exactement comme un binaire :

```bash
slsa-verifier verify-artifact tobby_1.0.0_linux_amd64.deb \
  --provenance-path tobby.intoto.jsonl \
  --source-uri github.com/tobby-fetch/tobby-fetch \
  --source-tag v1.0.0
```

Puis installez sans dépôt et sans accès réseau :

```bash
dpkg -i tobby_1.0.0_linux_amd64.deb                    # Debian/Ubuntu
rpm -i tobby_1.0.0_linux_amd64.rpm                     # RHEL/SUSE/Fedora
apk add --allow-untrusted tobby_1.0.0_linux_amd64.apk  # Alpine
```

Les paquets ne portent volontairement aucune signature de gestionnaire de
paquets : il n'existe ni dépôt de distribution ni trousseau de mainteneur pour
en ancrer une, et une clé publiée à côté des artefacts qu'elle signe
n'apporterait aucune sécurité. La confiance vient de la vérification de la
provenance ou des sommes de contrôle ci-dessus, faite avant l'installation —
`--allow-untrusted` sur apk dit exactement cela, et le rpm n'est pas signé
pour la même raison.

## 4. Image de conteneur : signature et provenance

Les notes de version indiquent le digest de l'image. Vérifiez toujours — et
déployez — par digest, jamais par tag :

```bash
# signature — signed by the release workflow's OIDC identity
cosign verify ghcr.io/tobby-fetch/tobby-fetch@sha256:... \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/tobby-fetch/tobby-fetch/'

# SLSA provenance — generated by the trusted container builder
slsa-verifier verify-image ghcr.io/tobby-fetch/tobby-fetch@sha256:... \
  --source-uri github.com/tobby-fetch/tobby-fetch \
  --source-tag v1.0.0
```

Le tag glissant `latest` est rafraîchi par une reconstruction hebdomadaire
contre les paquets Wolfi courants, derrière une gate Trivy qui ne publie rien
si l'analyse échoue. Ce digest est signé par cosign avec la même identité
mais **ne porte aucune provenance SLSA** — seuls les digests immuables
`vX.Y.Z` en ont. Les déploiements audités doivent épingler le digest d'une
version.

## 5. Chart Helm : signature

Depuis la v0.5.2, le chart de référence est publié avec chaque version comme
artefact OCI, sous la version même de la release — le chart `X.Y.Z` déploie
l'image `vX.Y.Z` et aucune autre. Les notes de version indiquent son digest :

```bash
cosign verify ghcr.io/tobby-fetch/charts/tobby@sha256:... \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/tobby-fetch/tobby-fetch/'

# the version you install must resolve to the digest you verified
helm show chart oci://ghcr.io/tobby-fetch/charts/tobby --version 0.6.0
helm install tobby oci://ghcr.io/tobby-fetch/charts/tobby --version 0.6.0 \
  --namespace tobby --create-namespace
```

Le chart porte une signature mais pas de provenance SLSA propre : il est
empaqueté à partir des sources taguées, ce qu'atteste déjà la provenance des
binaires et de l'image.

## 6. Binaires en FileSet : signature, SBOM et provenance

Depuis la v0.6.0, les binaires Linux et Windows sont aussi publiés comme un
seul FileSet — un index OCI multiplateforme à
`ghcr.io/tobby-fetch/binaries/tobby` dont chaque enfant contient un binaire de
release, octet pour octet. C'est ce qu'une recipe référence pour amener Tobby
dans une zone ([mettre à jour Tobby](../../air-gap/update-tobby/)). Les notes
de version indiquent le digest de l'index :

```bash
# the index: signed, with SLSA provenance from the trusted container builder
cosign verify ghcr.io/tobby-fetch/binaries/tobby@sha256:... \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity https://github.com/tobby-fetch/tobby-fetch/.github/workflows/release.yml@refs/tags/v1.0.0
slsa-verifier verify-image ghcr.io/tobby-fetch/binaries/tobby@sha256:... \
  --source-uri github.com/tobby-fetch/tobby-fetch \
  --source-tag v1.0.0

# each platform's child carries its binary's CycloneDX SBOM as a signed attestation
child=$(crane digest --platform linux/amd64 ghcr.io/tobby-fetch/binaries/tobby@sha256:...)
cosign verify-attestation --type cyclonedx ghcr.io/tobby-fetch/binaries/tobby@$child \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity https://github.com/tobby-fetch/tobby-fetch/.github/workflows/release.yml@refs/tags/v1.0.0
```

Les binaires qu'il contient sont ceux listés dans `SHA256SUMS` et couverts par
`tobby.intoto.jsonl` (section 2) : l'une ou l'autre voie les vérifie. Ces
signatures permettent à votre site de contrôler ce qu'il admet ; elles ne
remplacent jamais la signature propre de votre site sur la recipe qui les
transporte.

## 7. SBOM et OpenVEX

Chaque binaire a un SBOM CycloneDX (`.cdx.json`) signé sans clé via Sigstore ;
la signature est livrée comme fichier `.bundle` autonome à côté :

```bash
cosign verify-blob \
  --new-bundle-format \
  --bundle tobby-linux-amd64.cdx.json.bundle \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity https://github.com/tobby-fetch/tobby-fetch/.github/workflows/release.yml@refs/tags/v1.0.0 \
  tobby-linux-amd64.cdx.json
```

Remplacez `v1.0.0` par la version vérifiée : l'identité est épinglée sur le tag de la version, car une répétition du workflow de release signe des fichiers identiques depuis une branche du même dépôt.

Sortie attendue : `Verified OK`. Les SBOM propres à l'image sont générés
nativement par apko à la construction (SPDX) et joints à la version. Une fois
vérifiés, les SBOM s'ingèrent dans tout consommateur CycloneDX/SPDX — un
outil de suivi des dépendances, ou un scanner lancé directement sur le
document.

Les gates de release tiennent les constats critiques et hauts connus hors de
chaque version (Trivy sur l'image, govulncheck avec atteignabilité au niveau
du symbole sur le code). La politique permanente de l'ADR-0011 pour le reste :
un constat de scanner qui ne s'applique pas à Tobby reçoit pour réponse une
déclaration OpenVEX publiée et jointe à la version — jamais une règle
d'ignorance silencieuse. Concrètement : quand un constat est écarté, les
assets de la version incluent `tobby.openvex.json` avec une signature sans clé
dans `tobby.openvex.json.bundle`, vérifiable avec la même commande
`cosign verify-blob` que les SBOM ; fournissez le document à votre scanner
avec le SBOM. **Aucune version ne porte ce document aujourd'hui**, parce
qu'aucun constat n'a été écarté — son absence est elle-même une information
vérifiable : les gates ont passé avec zéro exclusion (la mécanique est dans
[`.vex/README.md`](https://github.com/tobby-fetch/tobby-fetch/blob/main/.vex/README.md)).

Suite : [tests et preuves](../tests-and-proofs/) montre les gates que chaque
commit franchit avant de pouvoir devenir une version.
