---
title: Mettre à jour et revenir en arrière
description: Faire passer une instance à une version plus récente selon le mode d'installation, ce que deviennent le répertoire d'état et le store d'une version à l'autre, comment vérifier le résultat, et ce qu'un retour arrière peut et ne peut pas promettre.
sidebar:
  order: 13
---

Une mise à jour remplace le programme et laisse les données où elles sont.
C'est ce qui la rend courte, et c'est aussi ce qui rend le retour arrière
plus délicat qu'il n'y paraît : l'ancien programme retrouve des données que le
nouveau a touchées. Cette page donne les étapes par mode d'installation, ce
que le code contrôle au passage, et où s'arrêtent ses garanties.

:::tip[En bref]
- Avant tout : sauvegardez le répertoire d'état, la configuration et les
  fichiers secrets qu'elle référence ([sauvegarde](../backup-rebuild/)), et
  lisez le changelog de la version pour y repérer les ruptures —
  [avant de commencer](#avant-de-commencer).
- Rien dans Tobby ne convertit les données d'un format à un autre. Un build
  lit les formats qu'il trouve ou refuse de démarrer —
  [ce que le code contrôle](#ce-que-le-code-contrôle-dune-version-à-lautre).
- Mettez à jour en remplaçant le paquet, le tag d'image ou la version du
  chart ; l'instance redémarre et est indisponible pendant ce temps —
  [par mode d'installation](#mettre-à-jour-selon-le-mode-dinstallation).
- Vérifiez avec `tobby version`, `/readyz`, `tobby state inspect` et une
  synchronisation à blanc — [vérifier](#vérifier-le-résultat).
- Revenir en arrière sur le programme est facile. Revenir en arrière sur les
  données n'est garanti que dans un sens : restaurer la sauvegarde faite
  avant la mise à jour — [revenir en arrière](#revenir-en-arrière).
:::

## Avant de commencer

1. **Lisez le changelog de la version**, en tête de son entrée : les
   [ruptures de compatibilité](https://github.com/tobby-fetch/tobby-fetch/blob/main/CHANGELOG.md)
   y figurent, avec ce qu'il faut changer. Le fichier de configuration est lu
   strictement (voir plus bas) : une clé renommée ou supprimée est un refus de
   démarrer, pas une valeur par défaut silencieuse.
2. **Sauvegardez** ce que [Sauvegarder et reconstruire une instance](../backup-rebuild/)
   énumère : le répertoire d'état avec ses permissions, le fichier de
   configuration et les fichiers secrets qu'il référence. C'est la copie dont
   dépend un retour arrière.
3. **Décidez quoi faire du store.** Le store d'un passthrough est un cache : les
   synchronisations suivantes le remplissent de nouveau, la sauvegarde de l'état
   suffit. Le store d'un miroir *est* le support : faites-en votre propre copie
   (snapshot de volume, `cp -a`) si vous voulez pouvoir revenir aux mêmes octets.
4. **Notez la version en cours**, pour savoir où revenir : `tobby version` sur
   l'hôte, la page `/about`, ou le label `version` de la métrique
   `tobby_build_info`.
5. **Contrôlez le répertoire d'état avec le nouveau build, avant la bascule.**
   `tobby state inspect` exécute les contrôles que le démarrage ferait sur le
   répertoire d'état (version du layout, permissions des entrées secrètes) et
   sort en erreur avec le même refus. Lancez le *nouveau* binaire sur l'état
   actuel, pendant que l'ancienne instance sert encore :

   ```sh
   tobby state inspect --state-root /var/lib/tobby/state
   ```

   La commande est sans danger à côté d'une instance en marche, et elle ne
   regarde ni le store ni le fichier de configuration.
6. **Sur Kubernetes avec Helm, conservez les valeurs du déploiement** :

   ```sh
   helm get values tobby --namespace tobby --output yaml > values-before-upgrade.yaml
   helm history tobby --namespace tobby
   ```

   Le numéro de révision que `helm history` affiche est ce qu'un retour arrière
   désigne.

## Ce que le code contrôle d'une version à l'autre

Quatre éléments sont versionnés sur disque, et le fichier de configuration est
lu strictement. Rien d'autre ne porte de numéro de version.

| Quoi | Où | Aujourd'hui | Ce que fait un build qui ne connaît pas la version |
|---|---|---|---|
| Layout du répertoire d'état | `layout.json` dans `state.root` | `1` | **Refuse de démarrer** : [`TBY-STA-001`](../../reference/errors/#tby-sta-001). |
| Fichier des comptes locaux | champ `version` de `accounts.yaml` | `1` | **Refuse de démarrer**, avec `accounts.yaml has unsupported version <n> (this build supports 1)`. |
| Format du store | `meta/format.json` dans `storage.root` | `1` | **Refuse d'ouvrir le store**, avec `store: format version <n> is not supported by this build (it supports version 1): use a Tobby release matching the store, or export/import the content with standard OCI tooling (skopeo, oras, crane) through the OCI image layout`. |
| Manifeste de média et format de store | `meta/media.json` sur un store transporté | `1` et `1` | **Bloque le support entier** : [`TBY-MED-003`](../../reference/errors/#tby-med-003) et [`TBY-MED-004`](../../reference/errors/#tby-med-004). |
| Fichier de configuration | `/etc/tobby/config.yaml` | pas de version | **Refuse de démarrer** sur une clé inconnue : `field <clé> not found in type config.Config`. |

Trois conséquences, toutes lues dans le code :

- **Il n'existe aucune étape de migration.** Aucun code ne convertit un
  répertoire d'état, un store ou un support d'un format à un autre. Les
  contrôles ci-dessus comparent un entier ; relever l'un d'eux serait un
  événement documenté à part. Le format du store vaut `1` dans v0.4.0, v0.5.0,
  v0.6.0 et dans la branche de développement : aucune version n'y a touché
  jusqu'ici.
- **Les contrôles refusent dans les deux sens.** Le store et le manifeste de
  média sont comparés pour *égalité* : un store estampillé d'un format plus
  récent est refusé par un build plus ancien, exactement comme l'inverse. Le
  layout de l'état est refusé quand il est *supérieur* à celui que le build
  connaît.
- **Le premier démarrage d'un build peut créer des fichiers.** Un répertoire
  d'état sans `layout.json` est *adopté* : le build contrôle les permissions des
  entrées secrètes, puis écrit l'enregistrement du layout. Si ces permissions
  sont trop larges, le démarrage est refusé avec
  [`TBY-STA-002`](../../reference/errors/#tby-sta-002) et rien n'est adopté. Un
  démarrage marque aussi le store de sa version de format quand il n'en a pas.
  Les builds jusqu'à v0.6.0 n'ont aucun contrôle du layout : une mise à jour
  depuis eux est la première fois qu'un répertoire est contrôlé. L'étape
  précédente, `tobby state inspect`, vous le dit.

Ce que fait aussi un démarrage, et ce qu'il ne fait pas :

- **Le travail interrompu reprend.** Une tâche trouvée en attente ou en cours au
  démarrage est remise en file ; les transferts sont incrémentaux par digest,
  une reprise ne déplace que ce qui manque. Un arrêt propre laisse aux
  transferts en vol `shutdown.gracePeriod` (30 secondes par défaut) pour
  finir ou poser un point de reprise — voir
  [arrêt propre](../operate/#arrêt-propre).
- **Les sessions ne survivent pas.** Les sessions de l'interface vivent en
  mémoire : tout le monde est déconnecté. Les jetons et les comptes sont dans le
  répertoire d'état et restent.

Jusqu'où va la promesse de compatibilité, et ce qui se fige à la 1.0, est dit
une seule fois, sur [Versions et compatibilité](../../project/release-compatibility/).
Il n'est pas répété ici.

## Mettre à jour selon le mode d'installation

Tous les modes ont la même forme : arrêter l'ancien programme, démarrer le
nouveau sur les deux mêmes répertoires. L'instance est indisponible entre les
deux — le store demande un seul écrivain, il n'y a donc pas de mise à jour
progressive (voir [limites](../../discover/limits/)). Une nouvelle version se
vérifie avant d'être installée, comme le montre
[Vérifier une version](../../project/verify-a-release/).

### Paquet Linux

Le paquet contient le binaire de la version dans `/usr/bin/tobby` et n'exécute
aucun script à l'installation : l'installer ne redémarre donc rien. Installez-le
par-dessus l'ancien, puis redémarrez le service que votre unité définit :

```sh
dpkg -i tobby_<version>_linux_amd64.deb                    # Debian/Ubuntu
rpm -U tobby_<version>_linux_amd64.rpm                     # RHEL/SUSE/Fedora
apk add --allow-untrusted tobby_<version>_linux_amd64.apk  # Alpine
systemctl restart tobby
```

L'unité est la vôtre (voir [Déployer](../deploy/#installation-en-vm--paquets-et-systemd)) :
un redémarrage envoie SIGTERM et attend le drain, gardez donc `TimeoutStopSec`
au-dessus de `shutdown.gracePeriod`. Le fichier de configuration et les deux
répertoires ne font pas partie du paquet et ne sont pas touchés.

### Binaire, Homebrew, Scoop, winget

Un poste miroir, ou tout hôte qui exécute le binaire directement, se met à jour
en remplaçant le fichier. Arrêtez d'abord l'instance si elle tourne à partir de lui.

```sh
brew upgrade tobby-fetch/tap/tobby      # macOS
scoop update tobby                      # Windows
winget upgrade tobby-fetch.tobby        # Windows ; peut avoir quelques jours de retard
```

Ou téléchargez le nouvel asset de la version et remplacez l'ancien fichier,
comme le montre [Installer et démarrer](../../try/install-and-start/). Le store
et le répertoire d'état restent où ils sont. Pour un support qui franchit un
air gap, lisez [Mettre à jour Tobby dans une zone isolée](../../air-gap/update-tobby/) :
la nouvelle version voyage elle-même comme une recette signée.

### Image de conteneur

Tirez le nouveau tag, supprimez le conteneur, relancez-le avec les mêmes
montages. L'image n'a pas d'état ; les deux volumes sont l'instance.

```sh
docker pull ghcr.io/tobby-fetch/tobby-fetch:v<version>
docker stop tobby && docker rm tobby
docker run -d --name tobby --read-only -p 8080:8080 \
  -v /srv/tobby/store:/var/lib/tobby/store \
  -v /srv/tobby/state:/var/lib/tobby/state \
  -v /srv/tobby/config.yaml:/etc/tobby/config.yaml:ro \
  ghcr.io/tobby-fetch/tobby-fetch:v<version> \
  serve --config /etc/tobby/config.yaml
```

`docker stop` attend dix secondes par défaut avant de tuer le processus ;
passez `--timeout 60` pour laisser au drain ses 30 secondes. Épinglez un tag
exact ou un digest : le tag flottant `latest` est une mise à jour que vous
n'avez pas choisie.

### Chart Helm

La version du chart **est** la version de Tobby : déplacer le chart déplace
l'image.

```sh
helm upgrade tobby oci://ghcr.io/tobby-fetch/charts/tobby --version <version> \
  --namespace tobby --values values-before-upgrade.yaml
```

Passer les valeurs que vous avez sauvegardées, plutôt que `--reuse-values`,
rend chaque valeur explicite. Si vous utilisez quand même `--reuse-values`, un
`image.tag` posé par une mise à jour antérieure est conservé et l'image ne
bouge pas : effacez-le avec `--set image.tag=`, ou épinglez `image.digest`. Le
déploiement est un seul réplica en stratégie `Recreate` : l'ancien pod libère
les volumes avant que le nouveau démarre. Les deux claims portent
`helm.sh/resource-policy: keep` : ni une mise à jour ni `helm uninstall` ne
retire les données.

Un changement de la configuration rendue redémarre le pod (son empreinte est
une annotation du modèle de pod). Avec `existingConfigSecret`, l'empreinte est
calculée à partir de la valeur `config`, pas de votre Secret : modifier ce
Secret ne redémarre pas le pod à lui seul. Toutes les valeurs du chart sont
dans [Valeurs du chart Helm](../chart-values/).

### Manifestes Kubernetes bruts

La ligne `image` de `deploy/manifests/40-deployment.yaml` épingle un tag, et ce
tag n'est pas déplacé pour vous quand une version est coupée : mettez-y la
version que vous déployez, puis appliquez :

```sh
kubectl apply -f ./deploy/manifests/
kubectl --namespace tobby rollout status deployment/tobby
```

La stratégie est aussi `Recreate` ici. Comparez les manifestes de l'ancienne et
de la nouvelle version avant d'appliquer : les autres changements d'une version
(contexte de sécurité, sondes, claims) n'arrivent que si vous les reprenez.

## Vérifier le résultat

Dans cet ordre, en vous arrêtant à la première étape qui échoue :

1. **Le processus démarre.** Un refus donne sa raison en une ligne `TBY-…`
   ([Dépannage](../../reference/troubleshooting/#linstance-ne-démarre-pas)).
   Sur Kubernetes, un refus se voit comme un pod qui redémarre en boucle ;
   lisez ses journaux.
2. **C'est la version attendue.** L'enregistrement de démarrage `tobby starting`
   porte `version` et `commit` ; `tobby version` les affiche pour le binaire de
   l'hôte ; `/about` et la métrique `tobby_build_info` disent ce qui tourne.
3. **Elle est prête.** `GET /readyz` répond `200` une fois le store et la
   configuration utilisables ; avant, `503`. `/healthz` dit seulement que le
   processus est vivant.
4. **L'identité est intacte.** `tobby state inspect` n'indique aucune entrée
   d'identité manquante, et le journal de démarrage ne contient aucun
   avertissement [`TBY-STA-003`](../../reference/errors/#tby-sta-003).
5. **Le store est intact.** `tobby store verify --instance <url> --wait`
   (ajoutez `--token-file` quand l'authentification est active) recalcule le
   hash de chaque blob, contrôle les manifestes et les signatures enregistrées
   contre les racines de confiance configurées maintenant, et liste les
   orphelins, sans rien écrire dans le store. C'est une tâche suivie sur
   l'instance. Voir [Vérifier l'intégrité du store](../verify-store/).
6. **La prochaine synchronisation est celle que vous attendez.**
   `tobby sync --dry-run` la planifie sans l'exécuter : le code de sortie `5`
   signifie que des changements sont prévus, ce qui n'est pas une erreur. Après
   une version qui change la façon dont le contenu est vérifié ou analysé, le
   plan peut différer du précédent.

Surveillez ensuite les [alertes](../monitoring/) pendant une journée. Une
synchronisation qui échoue juste après une mise à jour donne sa cause dans la
tâche en échec.

## Revenir en arrière

Disons d'abord ce qu'est un retour arrière. Le *programme* revient en démarrant
l'ancienne version. Les *données* — le répertoire d'état et le store — ne
reviennent pas d'elles-mêmes : elles restent comme la nouvelle version les a
laissées. Que l'ancien programme s'en accommode, c'est toute la question.

### Ce que le code garantit

- Un build plus ancien **refuse** un layout d'état, un format de store ou un
  format de manifeste de média plus élevé que ceux qu'il connaît, avec les
  messages ci-dessus. Il ne démarre pas sur des données qu'il sait ne pas
  comprendre.
- Ce refus est franc et précoce : il survient au démarrage, avant que l'instance
  serve quoi que ce soit.
- La configuration est lue strictement : un build plus ancien refuse une clé que
  le plus récent a ajoutée.

### Ce qu'il ne garantit pas

- **Rien ne fait revenir les données.** Il n'y a ni étape de rétrogradation ni
  trace de ce que la version plus récente a changé. `layout.json` dit quel build
  a adopté le répertoire, pas lequel l'a utilisé en dernier.
- **Un même numéro de format ne veut pas dire un même contenu.** Les quatre
  numéros de format valent `1` aujourd'hui : un build d'une version plus ancienne
  passe donc tous les contrôles ci-dessus. Le contrôle compare un entier ; il ne
  sait pas si le build plus récent a écrit un champ, un fichier ou une annotation
  que l'ancien ne gère pas. Les fichiers de métadonnées du store sont du JSON lu
  sans décodage strict : un build plus ancien ignore un champ qu'il ne connaît
  pas, et réécrit le fichier sans lui. Cela vient de la lecture du code ; le
  projet ne teste pas de rétrogradation d'une version à l'autre.
- **Les builds jusqu'à v0.6.0 ne contrôlent pas du tout le layout de l'état.**
  Revenir à l'un d'eux retire la protection elle-même.
- **Une recette ou un Retriever écrit pour un format plus récent** est un
  problème de document, pas de données : un build plus ancien refuse un
  `apiVersion` qu'il ne connaît pas quand il lit le document.
- **Avant la 1.0, rien ne promet davantage.** Le format du store est promis
  lisible dans les deux sens sur toute la série 1.x ; voir
  [Versions et compatibilité](../../project/release-compatibility/).

### Le retour arrière garanti : restaurer, puis démarrer l'ancienne version

Le seul chemin que cette page peut cautionner est celui que teste la page de
[sauvegarde](../backup-rebuild/) : remettre en place le répertoire d'état, la
configuration et les fichiers secrets pris **avant** la mise à jour, avec leurs
permissions, et démarrer la version qui les a écrits. Les comptes et les jetons
émis après la mise à jour disparaissent avec elle. Pour le store :

- **Passthrough :** laissez-le tel quel, ou restaurez sa propre copie si vous
  en avez fait une. Il contient du contenu, pas de l'identité ; la
  synchronisation suivante le remet en accord avec le Retriever.
- **Miroir :** restaurez la copie faite avant la mise à jour. Rien n'établit
  qu'un store écrit par la version plus récente soit lisible par l'ancienne au-delà
  des contrôles de format.

Concrètement :

- **Paquet :** installez le fichier du paquet précédent par le chemin de
  rétrogradation de votre gestionnaire de paquets (`rpm` demande `--oldpackage`),
  restaurez l'état, redémarrez.
- **Conteneur :** lancez le tag précédent sur les volumes restaurés.
- **Helm :** `helm rollback tobby <révision> --namespace tobby`, avec la
  révision que `helm history` montrait avant la mise à jour. Elle restaure le
  déploiement et le Secret de configuration de cette révision. Elle ne restaure
  **pas** les deux claims : ils gardent les données que la nouvelle version a
  écrites, restaurez donc aussi l'état depuis la sauvegarde, instance arrêtée
  (voir [reconstruire une instance](../backup-rebuild/#reconstruire-une-instance)).
- **Manifestes :** remettez le tag d'image précédent et appliquez, avec la même
  restauration.

Si la nouvelle version tourne déjà depuis un moment et qu'un retour arrière
ferait perdre des comptes, des jetons ou des imports que vous voulez garder, ne
revenez pas en arrière sur les données : lisez le refus ou la tâche en échec, et
corrigez en avant.

Un support produit par une version plus récente et porté vers une destination
plus ancienne n'est refusé que si le numéro de format de son manifeste ou de son
store diffère ([`TBY-MED-003`](../../reference/errors/#tby-med-003),
[`TBY-MED-004`](../../reference/errors/#tby-med-004)). Le code ne compare aucune
autre version : gardez les deux côtés sur la même version quand vous le pouvez.
