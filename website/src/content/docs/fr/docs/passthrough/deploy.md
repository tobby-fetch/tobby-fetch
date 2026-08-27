---
title: "Déployer : Kubernetes et VM"
description: Le chart Helm, les manifestes bruts, les paquets Linux, l'image de conteneur et le premier compte — tout ce qu'exige une installation de production.
sidebar:
  order: 2
---

Tobby se livre sous trois formes, toutes issues de la même chaîne de
release reproductible : un chart Helm (avec son jumeau en manifestes
bruts), des paquets Linux (`.deb`, `.rpm`, `.apk`) et une image de
conteneur. Quelle que soit la forme, une règle domine la mise en place.

## Store et état ne sont pas le même volume

Tobby écrit dans deux répertoires :

- **Le store** (`storage.root`) contient les artefacts, les recipes et
  les journaux d'opération. Volumineux, autonome, re-téléchargeable.
- **L'état** (`state.root`) contient l'identité de l'instance : comptes,
  jetons, certificats servis, spools de téléchargements reprenables.
  Petit, et rien ne le recrée.

Le répertoire d'état est la cible de sauvegarde ; le store ne coûte que
de la bande passante à reconstruire. Les deux ne doivent jamais être
imbriqués l'un dans l'autre — les secrets ne voyagent pas sur un média
transportable — et Tobby refuse de démarrer quand ils le sont. Le chart
Helm refuse en plus de se rendre quand les deux chemins de montage sont
identiques. Voir
[Exploiter dans la durée](../../passthrough/operate/#sauvegarde--le-répertoire-détat).

## Kubernetes

Le déploiement de référence vit dans le dépôt sous
[`deploy/`](https://github.com/tobby-fetch/tobby-fetch/tree/main/deploy) :
un chart Helm dans `deploy/charts/tobby/` et le même pod écrit en YAML
brut numéroté dans `deploy/manifests/`, pour les clusters qui ne font
pas tourner Helm. Les deux produisent un conteneur non-root sur un
système de fichiers racine en lecture seule, toutes capacités
abandonnées, probes câblées, et des PersistentVolumeClaims distincts
pour le store (100Gi par défaut) et l'état (20Gi par défaut — l'état
accueille aussi les spools de téléchargements reprenables, voir
[Exploiter](../../passthrough/operate/#les-transferts-interrompus-reprennent)).

### Avec Helm

```sh
helm install tobby ./deploy/charts/tobby \
  --namespace tobby --create-namespace \
  --set config.mode=passthrough
```

`config.mode` est la seule valeur sans défaut utilisable : une instance
doit dire ce qu'elle est (FR-001). La valeur `config` du chart devient
`/etc/tobby/config.yaml` et reprend exactement les clés de configuration
de Tobby — la sortie de `tobby config dump` se colle telle quelle. `env`
positionne les variables `TOBBY_*` et `extraArgs` ajoute des drapeaux ;
la précédence est drapeaux > environnement > fichier (FR-003). Quatre
clés (`storage.root`, `state.root`, `server.addr`,
`registries.credentialsFile`) appartiennent au chart et sont rejetées si
elles sont posées directement, parce que la spec du pod en dépend.

Les credentials de registre sont un Secret
`kubernetes.io/dockerconfigjson` distinct (FR-004), monté en lecture
seule — jamais dans le fichier de configuration, jamais dans le store :

```sh
kubectl -n tobby create secret docker-registry tobby-registry-credentials \
  --docker-server=registry.example.com \
  --docker-username=tobby \
  --docker-password="$(cat ./token)"

helm upgrade tobby ./deploy/charts/tobby --namespace tobby --reuse-values \
  --set registryCredentials.enabled=true \
  --set registryCredentials.existingSecret=tobby-registry-credentials
```

Les entrées sont cherchées par l'hôte réellement contacté — avec une
[substitution de source](../../passthrough/retriever-cascade/) en jeu,
le registre de substitution, pas le registre nominal.

### Depuis les manifestes bruts

Les fichiers de `deploy/manifests/` sont numérotés dans l'ordre
d'application et créent leur propre namespace `tobby`, qui impose le Pod
Security Standard **restricted** — une modification ultérieure
affaiblissant le contexte de sécurité du pod est rejetée par l'API
server, pas silencieusement acceptée.

```sh
kubectl apply -f ./deploy/manifests/
```

Modifiez `20-secret-config.yaml` (posez `mode`, et `retriever.source` si
vous en avez un) et `21-secret-registry-credentials.yaml` (la charge
livrée est un `{"auths":{}}` vide et valide) avant d'appliquer.

### Vérifier l'image avant de déployer

L'image publiée porte une provenance SLSA Build L3 et une signature
cosign, toutes deux faites contre le **digest**. Épinglez le digest en
production (`image.digest` dans le chart) et vérifiez d'abord :

```sh
cosign verify ghcr.io/tobby-fetch/tobby-fetch@sha256:... \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/tobby-fetch/tobby-fetch/'
```

Le tag glissant `latest` est signé avec la même identité mais ne porte
aucune provenance SLSA — seuls les digests immuables `vX.Y.Z` en ont. Le
guide de vérification complet, provenance comprise, est
[Vérifier une release](../../project/verify-a-release/).

## Installation en VM : paquets et systemd

Chaque release publie des paquets `.deb`, `.rpm` et `.apk` qui
enveloppent exactement le binaire de release `tobby-linux-<arch>` —
installé dans `/usr/bin/tobby`, sans script d'installation, entièrement
autonome (CGO désactivé). Ils sont conçus pour une installation
hors-ligne, sans dépôt : vérifiez (`SHA256SUMS` ou provenance SLSA),
copiez le fichier, installez sans réseau :

```sh
dpkg -i tobby_0.4.2_linux_amd64.deb                    # Debian/Ubuntu
rpm -i tobby_0.4.2_linux_amd64.rpm                     # RHEL/SUSE/Fedora
apk add --allow-untrusted tobby_0.4.2_linux_amd64.apk  # Alpine
```

Les paquets ne portent délibérément aucune signature de gestionnaire de
paquets : la confiance vient de la vérification de provenance ou de
checksum faite avant l'installation — `--allow-untrusted` sur apk le dit
exactement. Le raisonnement est détaillé dans
[Vérifier une release](../../project/verify-a-release/).

Les paquets n'installent que le binaire ; la définition du service est à
vous. Cette unit de référence reprend la posture du déploiement en
conteneur :

```ini
# /etc/systemd/system/tobby.service
[Unit]
Description=Tobby instance
After=network-online.target
Wants=network-online.target

[Service]
User=tobby
Group=tobby
ExecStart=/usr/bin/tobby serve --config /etc/tobby/config.yaml
Restart=on-failure
# SIGTERM lance l'arrêt en douceur ; laissez-lui plus que
# shutdown.gracePeriod (30s par défaut) avant le SIGKILL.
TimeoutStopSec=60
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=/var/lib/tobby/store /var/lib/tobby/state

[Install]
WantedBy=multi-user.target
```

Créez les deux répertoires appartenant à l'utilisateur du service,
posez `storage.root` et `state.root` dans `/etc/tobby/config.yaml`, et
gardez les secrets hors du fichier d'unit — le mot de passe du proxy,
par exemple, appartient au fichier de configuration ou à un fichier
d'environnement lisible du seul utilisateur du service (voir
[Réseau d'entreprise](../../passthrough/network/)).

## Image de conteneur sans Kubernetes

`ghcr.io/tobby-fetch/tobby-fetch` est une image minimale, sans shell,
tournant sous l'UID 65532. Montez les deux volumes et la configuration :

```sh
docker run -d --name tobby --read-only \
  -p 8080:8080 \
  -v /srv/tobby/store:/var/lib/tobby/store \
  -v /srv/tobby/state:/var/lib/tobby/state \
  -v /srv/tobby/config.yaml:/etc/tobby/config.yaml:ro \
  ghcr.io/tobby-fetch/tobby-fetch:v0.4.2 \
  serve --config /etc/tobby/config.yaml
```

## Matrice des systèmes d'exploitation

| Plateforme | Palier |
| --- | --- |
| Linux amd64 / arm64 | Périmètre de production validé : service passthrough, paquets, image de conteneur. |
| Windows amd64 / arm64 | Binaire statique unique publié, et le parcours du poste miroir y est validé (NFR-018). Le mode passthrough sous Windows est hors du périmètre validé. Voir [plateformes supportées](../../reference/platforms/). |
| macOS amd64 / arm64 | Palier de confort (NFR-001) : même chaîne reproductible, distribution via `brew install tobby-fetch/tap/tobby`, suite de tests complète en CI — mais aucun support de production impliqué. |

Le store refuse les systèmes de fichiers qui ne peuvent pas contenir sa
disposition — FAT32 en fait partie ; voir
[limites](../../discover/limits/).

## Le premier compte, sans interaction

L'authentification est active par défaut, et Tobby refuse de servir tant
que le répertoire d'état ne contient pas au moins un compte
(`TBY-AUTH-001`) — une installation Kubernetes neuve reste en
`CrashLoopBackOff` jusqu'à ce que l'administrateur existe, ce qui est
voulu : aucune surface n'est jamais exposée ouverte. Sur un terminal,
`tobby quickstart` vous accompagne. Pour l'automatisation, chaque
question a un drapeau, et le mot de passe arrive sur l'entrée standard —
jamais sur la ligne de commande :

```sh
printf '%s\n' "$ADMIN_PASSWORD" | tobby quickstart \
  --mode passthrough \
  --config /etc/tobby/config.yaml \
  --storage-root /var/lib/tobby/store \
  --state-root /var/lib/tobby/state \
  --password-stdin
```

Les commandes primitives équivalentes sont `tobby user add admin
--state-root /var/lib/tobby/state --password-stdin` suivie de `tobby
serve --config /etc/tobby/config.yaml`. Le premier compte d'une instance
est toujours `admin`, et l'outil calcule lui-même le hash du mot de
passe. Sur Kubernetes, où l'image n'a pas de shell, exécutez l'étape
`user add` dans un pod éphémère montant le claim d'état — la recette
exacte est dans
[`deploy/README.md`](https://github.com/tobby-fetch/tobby-fetch/blob/main/deploy/README.md).

S'il vous faut absolument tourner ouvert, `auth.disabled: true` est un
opt-in explicite qui pose un bandeau d'avertissement permanent dans
l'interface (FR-075). Ne le laissez jamais actif.

Suite : pointez l'instance vers le monde extérieur à travers
[le réseau d'entreprise](../../passthrough/network/).
