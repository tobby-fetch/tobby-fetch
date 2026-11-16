---
title: Contribuer
description: Compiler depuis les sources, signer vos commits (DCO), vous repérer dans le code et exécuter localement toute la pyramide de tests.
sidebar:
  order: 6
---

Tobby est développé en ouvert — GPL-3.0-only, documents de conception
d'abord, chaque décision dans un ADR public. Cette page vous mène d'un clone à
une pull request fusionnée. Les règles de workflow vivent dans
[CONTRIBUTING.md](https://github.com/tobby-fetch/tobby-fetch/blob/main/CONTRIBUTING.md) ;
cette page y ajoute le contexte.

## Compiler depuis les sources

La chaîne d'outils est épinglée dans
[`mise.toml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/mise.toml)
pour que chaque machine — et la CI — compile avec les mêmes versions : Go
1.27.4, golangci-lint 2.15.0, gitleaks 8.30.1, et Node 26.13.0 avec pnpm
12.18.0 (build du site uniquement ; le produit lui-même n'a aucun Node) :

```sh
git clone https://github.com/tobby-fetch/tobby-fetch && cd tobby-fetch
mise install        # installs the pinned toolchain
mise run setup      # one-time: activates the git hooks (secret scan on commit)
mise run build      # produces bin/tobby
```

Seuls Git et le Go épinglé sont strictement requis — `mise run build` enveloppe
`go build -trimpath -o bin/tobby ./cmd/tobby`.

## DCO — la signature exigée sur chaque commit

C'est l'explication de référence ; les autres pages renvoient ici.

Chaque commit doit porter un trailer `Signed-off-by`, qui certifie, selon le
[Developer Certificate of Origin](https://developercertificate.org), que vous
avez écrit la modification ou avez par ailleurs le droit de la soumettre sous
la licence du projet. Git ajoute le trailer pour vous :

```sh
git commit -s -m "feat: add per-recipe pre-flight byte estimate"
git commit --amend -s --no-edit    # forgot on the last commit
git rebase --signoff main          # forgot on a whole branch
```

Il n'y a **pas de CLA**. Le DCO est volontairement plus léger : vous ne cédez
rien et conservez le copyright de vos contributions
([ADR-0003](../../reference/srs-adr/)). Le contrôle est assuré par
[`dco.yml`](https://github.com/tobby-fetch/tobby-fetch/blob/main/.github/workflows/dco.yml) —
une simple vérification shell sur `git log`, sans action tierce, checkout
épinglé par SHA complet comme tout le reste. Il bloque la pull request au
moindre commit non signé et avertit lorsque l'e-mail de signature diffère de
celui de l'auteur du commit. Les nouveaux fichiers source portent aussi un en-tête
SPDX (`GPL-3.0-only`) — voir CONTRIBUTING.md pour les lignes exactes.

## Le code, à grands traits

Un module, un binaire (`cmd/tobby`), tout le reste sous `internal/` dans de
petits paquets strictement stratifiés — l'audit du jalon 4 a confirmé que le
graphe de dépendances n'a aucun cycle et que les interfaces se trouvent du côté
du consommateur. Carte approximative :

- **Entrée et surface** — `cli` (commandes), `server` (câblage HTTP), `ui`
  (gabarits rendus côté serveur + htmx vendorisé, sans Node), `api` (miroirs
  de `/api/v1`), `fileserve` (service HTTP des FileSets).
- **Moteur** — `engine` (résolution des recipes, synchronisation, promotion,
  cookbook), `importer` (import unitaire), `blobfetch` (transferts en flux,
  reprise fine), `relocate` (nommage de destination, ADR-0013), `schedule`,
  `tasks` (file persistante).
- **Confiance et politique** — `sigverify` (les deux layouts cosign, hors
  ligne), `policy` (trust roots, périmètres, liste d'autorisation), `auth`
  (comptes, tokens, RBAC), `audit`, `taxonomy` (le catalogue d'erreurs
  bilingue `TBY-*`).
- **Substrat** — `store` (stockage de la registry OCI embarquée, GC),
  `config`, `netx` (le transport sortant partagé unique : proxy, CA privées),
  `logging`, `metrics`, `runid`, `tlsadmin`, `buildinfo`.

L'infrastructure de test vit dans `test/` (scénarios hermétiques `topology/`,
suite chromedp `browser/`, vérifications `ocilogin/` avec de vrais clients) et
dans `crucible/`.

## Exécuter la pyramide en local

Les tâches mise sont les mêmes commandes que celles de la CI — voir
[tests et preuves](../tests-and-proofs/) pour la signification de chaque gate :

```sh
mise run test          # go test -race -count=2 ./...  (unit + integration)
mise run lint          # golangci-lint, strict profile, zero suppressions
mise run coverage      # per-package coverage floor (>= 70% per internal package)
mise run vuln          # govulncheck, reachable known vulnerabilities
mise run secrets-scan  # gitleaks over the full git history
mise run test-browser  # chromedp suite — uses your installed Chrome,
                       # never downloads one
mise run doc           # serve this website locally
```

Les scénarios de topologie (`test/topology/scenario-m*.sh`) nécessitent Docker
et tournent partout où Docker tourne. Lancez `mise run lint` et `mise run test`
avant d'ouvrir une pull request ; la CI impose les deux ainsi que les autres
gates, et `main` n'accepte de changements que par une pull request relue et
verte.

## Rejouer le crucible : prérequis matériels

Extrait du
[README du crucible](https://github.com/tobby-fetch/tobby-fetch/blob/main/crucible/README.md) :

- **Un hôte Linux** avec [Incus](https://linuxcontainers.org/incus/) installé
  et initialisé. Les scénarios des jalons 1 à 4 s'exécutent entièrement dans
  des conteneurs système, donc tout hôte Linux convient, y compris des VM
  cloud sans virtualisation imbriquée — le jalon 1 a été validé sur une petite
  instance cloud jetable. Les scénarios des jalons suivants qui exigent des
  noyaux par nœud requerront KVM (`/dev/kvm`).
- **Un pool de stockage ZFS ou btrfs** pour la réinitialisation instantanée des
  scénarios par snapshot (`dir` fonctionne, mais les réinitialisations sont
  lentes).
- **Root sur l'hôte** — le média amovible est un périphérique bloc adossé à
  une boucle (`losetup`), le même niveau de privilège que l'administration
  d'Incus.
- **Outils de l'hôte fournis par l'environnement** : `go`, `jq`, `curl`,
  `openssl` et `helm`. Rien n'est téléchargé pendant un scénario, à une
  exception délibérée près : `cosign` est installé à la demande par les
  scénarios m3/m4 s'il est absent, car les vraies signatures sont l'objet de ces
  scénarios.

macOS et Windows peuvent piloter un hôte crucible à distance via l'API Incus,
mais ne peuvent jamais en héberger un. Tout s'exécute dans un projet Incus
dédié (`tobby-crucible`) ; `./crucible/teardown.sh` le supprime entièrement.
