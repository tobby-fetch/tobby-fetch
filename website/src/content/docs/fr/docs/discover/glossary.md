---
title: Glossaire
description: Définitions simples des mots employés dans cette documentation pour les zones, les recipes, la confiance, le store, les supports amovibles et le vocabulaire du projet, chacune liée à la page qui traite du sujet.
sidebar:
  order: 1.5
---

Si vous connaissez `docker pull` mais pas cosign, et que vous découvrez ce
produit, cette page sert à retrouver le sens d'un mot. Chaque entrée est
courte et renvoie à la page qui traite le concept en détail. Quand un concept
porte deux noms dans la documentation, l'un est retenu ici et l'autre est
mentionné une fois sous la forme « aussi écrit ».

## Zones et modes

| Terme | Signification |
|---|---|
| **Artefact OCI** | Tout objet stocké dans un registre conforme au standard OCI : image de conteneur, chart Helm ou autre fichier empaqueté et adressé par digest. Tobby déplace ces objets, et rien d'autre. Voir [Pourquoi Tobby](../why-tobby/). |
| **Zone** | Un espace réseau avec ses propres règles de frontière. Une **zone connectée** atteint le registre amont, éventuellement via un proxy et des filtres. Une **zone restreinte** n'est joignable que par des chemins contrôlés, par exemple une DMZ. Une **zone isolée** (air-gap) n'a aucun chemin réseau vers l'amont. Voir [Pourquoi Tobby](../why-tobby/). |
| **Registre de zone** | Le registre situé dans une zone, depuis lequel les charges de travail de cette zone tirent leurs images. C'est là que Tobby promeut le contenu. Voir [Connecter vos clients](../../passthrough/connect-clients/). |
| **Registre de production** et **chaîne de qualification** | Le registre de production est le registre amont qui héberge le contenu qualifié et le cookbook. La chaîne de qualification est le processus propre à l'organisation qui qualifie le contenu et y publie les recipes signées ; elle ne fait pas partie de Tobby. Voir [Pourquoi Tobby](../why-tobby/). |
| **Identité de zone** | Le nom de la zone qu'une instance sert : le `metadata.name` de son Retriever. Une instance côté destination doit la configurer (`zone`), car son contenu arrive sur un support et elle ne peut sinon pas savoir si un support lui est adressé. Voir [Référence de configuration](../../reference/configuration/). |
| **Mode passthrough** | Le mode des zones qui disposent d'un chemin réseau : un service en fonctionnement promeut le contenu en continu vers le registre de zone. Voir [Zones connectées — vue d'ensemble](../../passthrough/overview/). |
| **Mode miroir** (mirror) | Le mode des zones isolées. Côté connecté, une instance synchronise le contenu, à la demande, dans un store qui est transporté à travers l'air gap ; côté isolé, une seconde instance dans le même mode vérifie ce store avant de le servir ou de le pousser. Miroir est le *mode*, la zone isolée est la *zone* qu'il sert, et un support est ce qui est transporté. Voir [Le parcours média de bout en bout](../../air-gap/media-workflow/). |
| **Promotion** (promouvoir) | Le verbe central : déplacer vers une zone, sans le modifier et après vérification, un contenu déjà qualifié en amont. Tobby ne construit, ne modifie et ne re-signe rien de ce qu'il promeut. Voir [Votre première promotion](../../try/first-promotion/). |
| **Cascade** | Des zones chaînées où l'instance de chaque zone alimente la suivante : connectée, puis restreinte, puis plus restreinte. Les mêmes recipes signées circulent le long de la chaîne et chaque instance vérifie à l'entrée. Voir [Retriever de zone et cascade](../../passthrough/retriever-cascade/). |

## Recipes

| Terme | Signification |
|---|---|
| **Recipe** | Un document YAML qui liste les ingrédients d'une application, chacun épinglé par version et par digest. C'est l'unité que Tobby promeut. Voir [Recipes, cookbooks et retrievers](../../recipes/understand/). |
| **Ingrédient** | Un artefact OCI référencé par une recipe : image de conteneur, chart Helm, artefact OCI générique ou FileSet. Voir [Recipes, cookbooks et retrievers](../../recipes/understand/). |
| **Cookbook** | Un espace de noms de dépôts OCI où les recipes sont publiées comme de simples artefacts OCI. Le cookbook d'une zone est le catalogue faisant autorité des logiciels approuvés pour elle. Voir [Recipes, cookbooks et retrievers](../../recipes/understand/). |
| **Recipe brouillon / cuisinée** (draft / cooked) | Un brouillon est une recipe en cours d'écriture : les contraintes de version sont permises et les digests facultatifs. Une recipe cuisinée est publiée dans un cookbook, chaque ingrédient épinglé par tag exact et digest, signée et immuable. **Cuisiner** (cooking) désigne l'étape entre les deux. Voir [Recipes, cookbooks et retrievers](../../recipes/understand/). |
| **Digest / épinglage** | Le digest est l'adresse de contenu `sha256:…` d'un artefact. Une recipe épingle chaque ingrédient par ce digest : une seule signature atteste les octets exacts de toute la livraison. Voir [Recipes, cookbooks et retrievers](../../recipes/understand/). |
| **Retriever** | Un document YAML qui liste les recipes, avec leurs contraintes de version, qu'une zone souhaite. C'est l'état désiré de la zone, avec un Retriever par zone. Voir [Recipes, cookbooks et retrievers](../../recipes/understand/). |
| **FileSet** | Un type d'ingrédient : des fichiers quelconques (lots de configuration, documentation, scripts, certificats) empaquetés comme une image OCI standard. Aussi écrit « file set ». Voir [Recipes, cookbooks et retrievers](../../recipes/understand/). |

## Confiance et politique

| Terme | Signification |
|---|---|
| **cosign** | Un outil tiers, issu du projet Sigstore, qui signe des artefacts OCI et vérifie ces signatures. Tobby vérifie les signatures cosign ; il ne signe jamais rien lui-même. Voir [Signatures, racines de confiance et liste d'autorisation](../../security/content-trust/). |
| **Racine de confiance** | Une clé publique configurée dans Tobby (`trust.roots`) et utilisée pour vérifier les signatures cosign des recipes. Voir [Signatures, racines de confiance et liste d'autorisation](../../security/content-trust/). |
| **Liste d'autorisation** | La liste de motifs d'hôtes de registre (`registries.allowlist`) que Tobby peut contacter comme source ou vers lesquels il peut promouvoir. Quand la clé est absente, rien n'est restreint. Aussi écrit « allow-list ». Voir [Signatures, racines de confiance et liste d'autorisation](../../security/content-trust/). |
| **Périmètre d'assouplissement** | Une exception déclarée et nommée (`trust.scopes`) qui admet les recipes de certains motifs de dépôts sous des règles assouplies, par exemple non signées. Il n'existe aucun contournement global : hors périmètre, la vérification est stricte. Aussi écrit « trust scope ». Voir [Signatures, racines de confiance et liste d'autorisation](../../security/content-trust/). |
| **Assurance réduite** | Le marqueur que porte une recipe admise non signée sous un périmètre d'assouplissement, ainsi que tout ce qu'elle atteint. Il nomme le périmètre et la raison, et apparaît dans les rapports, l'API, l'interface, les journaux et les métriques. Voir [Le marqueur d'assurance réduite](../../security/reduced-assurance/). |
| **Rôles** | Un compte est `viewer`, `operator` ou `admin`, chacun incluant le précédent. Déclencher une synchronisation ou vérifier un support demande `operator` ; les dérogations et les écrans d'administration demandent `admin`. Voir [Authentification et rôles](../../security/auth-rbac/). |
| **Dérogation** | Le contournement délibéré de l'un des deux refus d'import portant sur tout le support et protégeant contre l'accident : un support adressé à une autre zone, ou plus ancien que le dernier importé. L'interface web et l'API exigent le rôle `admin` ; en ligne de commande, c'est l'accès à l'hôte qui fait le contrôle. Toute dérogation appliquée est auditée, à la vérification comme à l'import. Aucune dérogation n'existe pour un échec d'intégrité ou de signature. Voir [Importer côté zone isolée](../../air-gap/import-destination/). |

## Store et contenu

| Terme | Signification |
|---|---|
| **Store** | Le répertoire (`storage.root`) où Tobby conserve les artefacts, les recipes et les journaux d'opération. Il est volumineux, autonome et récupérable. Voir [Déployer](../../passthrough/deploy/). |
| **Répertoire d'état** | Le répertoire (`state.root`) qui porte l'identité de l'instance : comptes, jetons, certificats. Il est petit, rien ne le recrée, et c'est la cible de sauvegarde. Il ne doit jamais être imbriqué avec le store. Voir [Déployer](../../passthrough/deploy/). |
| **Relocalisation** (chemin relocalisé) | La règle qui place chaque ingrédient dans le registre de zone sous son hôte source d'origine : `<registre-de-zone>[/<préfixe-de-base>]/<hôte-source>/<chemin-du-dépôt>`. `docker.io/bitnami/wordpress` devient `registry.zone.example/docker.io/bitnami/wordpress`, avec le même digest. Voir [Connecter vos clients](../../passthrough/connect-clients/). |
| **Préfixe de base** | Un préfixe de chemin de dépôt facultatif, appliqué uniformément à tous les ingrédients relocalisés d'une instance (`storage.basePrefix`, la disposition propre de l'instance). `destination.basePath` est une clé distincte : le préfixe que voit la zone suivante sur le registre de destination. Voir [Connecter vos clients](../../passthrough/connect-clients/). |
| **Registre embarqué** | Le registre OCI standard (`/v2/`) que chaque instance sert depuis son propre store, à côté de l'interface web et de l'API. Une zone sans registre à elle peut y tirer. Voir [Connecter vos clients](../../passthrough/connect-clients/). |
| **Synchronisation** (sync) | Une exécution qui résout le Retriever, récupère les recipes et leurs ingrédients, les vérifie et, en passthrough avec un registre de destination, pousse ce qui manque à la zone. Le passthrough la lance à intervalle régulier (`sync.interval`) ; le mode miroir seulement à la demande. C'est une tâche, avec un identifiant de run. Voir [Retriever de zone et cascade](../../passthrough/retriever-cascade/). |
| **Registre de destination** | Le registre de zone dans lequel une instance promeut, défini par `destination.registry`. Lorsqu'il est vide, rien n'est promu. Voir [Référence de configuration](../../reference/configuration/). |
| **Substitution de source** | Un réglage au moment de la récupération (`registries.substitutions`) qui fait récupérer `docker.io/…` par une zone aval auprès du registre de zone amont, sans modifier aucune recipe. Il ne change que le point de contact, jamais le chemin relocalisé. Voir [Retriever de zone et cascade](../../passthrough/retriever-cascade/). |
| **Référence effective** | La référence réellement contactée une fois la substitution de source appliquée. La liste d'autorisation et la recherche des identifiants s'appliquent à elle, tandis que les périmètres de confiance portent sur la référence nominale. Voir [Signatures, racines de confiance et liste d'autorisation](../../security/content-trust/). |
| **Classe de provenance** | L'origine enregistrée pour chaque dépôt du store : `recipe` (géré par une recipe vérifiée), `unit-import` (un import ponctuel, ou un FileSet empaqueté sur l'instance) ou `seed` (poussé par `/v2/` avec un client standard). Voir [Exploiter dans la durée](../../passthrough/operate/). |
| **Import ponctuel** | L'import d'un seul artefact OCI par référence, en dehors de toute recipe. Aussi écrit « import unitaire » ; sa classe de provenance est `unit-import`. Voir [Imports ponctuels](../../passthrough/one-off-import/). |
| **Purge** (prune) | Suppression du contenu géré par recipe que le Retriever résolu ne référence plus. En passthrough, c'est le réglage `sync.prune`, désactivé par défaut. En mode miroir, elle est activée par défaut et confirmée au déclenchement d'une synchronisation. Les imports ponctuels, la base de vulnérabilités et le contenu déposé en seed ne sont jamais éligibles. Voir [Gérer les supports dans la durée](../../air-gap/manage-media/). |

## Supports amovibles

| Terme | Signification |
|---|---|
| **Support** (média) | Le stockage amovible, par exemple un disque, qui transporte un store à travers l'air gap. Déplacer le répertoire, c'est faire le transfert. Voir [Le parcours média de bout en bout](../../air-gap/media-workflow/). |
| **Manifeste de média** | L'inventaire de ce que couvre un support, stocké dans `meta/media.json`. Il n'est volontairement pas signé : la destination le traite comme une déclaration et revérifie le contenu. Voir [Modèle de sécurité des supports](../../air-gap/media-security/). |
| **Verdict** | Le résultat de la vérification d'un support : `pushable` (toutes les livraisons validées), `partial` (certaines validées, d'autres bloquées) ou `blocked`. Voir [Importer côté zone isolée](../../air-gap/import-destination/). |
| **Verrou du support** (media gate) | Le verrou d'une instance côté destination : tant qu'elle n'a pas vérifié elle-même le support, dans ce processus, elle refuse de servir son contenu (`TBY-MED-030`). Il se referme à chaque redémarrage. Voir [Modèle de sécurité des supports](../../air-gap/media-security/). |
| **Pré-vol** | Le contrôle exécuté avant un export, qui calcule ce qui voyagerait et refuse les transferts impossibles, par exemple un contenu qui ne tient pas avec sa marge de sécurité. Voir [Préparer le poste source](../../air-gap/prepare-source/). |
| **Mode plan** (dry-run) | `tobby sync --dry-run` : indique tout ce qu'une synchronisation ferait, sans le faire. Voir [Préparer le poste source](../../air-gap/prepare-source/). |
| **Bordereau** | Une page imprimable, en anglais ou en français, qui résume un transfert physique : support, fraîcheur, inventaire, vérification, analyse et marqueurs d'assurance. C'est une aide non signée, jamais une preuve. Voir [Le bordereau de transfert](../../air-gap/transfer-slip/). |

## Vocabulaire du projet

| Terme | Signification |
|---|---|
| **Identifiant de run** (run ID) | L'identifiant qu'une synchronisation, un import ou une vérification de support porte à travers les journaux, la tâche et son rapport. Filtrez les journaux dessus. Voir [Dépannage](../../reference/troubleshooting/). |
| **Code d'erreur `TBY-…`** | Un identifiant stable de la forme `TBY-<domaine>-<nnn>` attaché à chaque erreur visible par l'utilisateur. Le code ne change jamais de sens. Voir [Erreurs et dépannage](../../reference/errors/). |
| **Crucible** | Le niveau de test qui rejoue les scénarios d'un jalon sur de vrais nœuds jetables et conditionne l'acceptation du jalon, et non les fusions. Voir [Tests et preuves de qualité](../../project/tests-and-proofs/). |
| **Jalon / train** | Le projet est livré en jalons numérotés. Le jalon N sort sur la ligne de versions (train) v0.N.x, et le jalon 7 sort en v0.7.x, avant la 1.0.0. Voir [État du projet et notes de version](../status/). |
| **`FR-nnn` / `NFR-nnn`** | Les exigences fonctionnelles et non fonctionnelles de la spécification (SRS), chacune avec un numéro stable. Voir [SRS et ADR](../../reference/srs-adr/). |
| **`UC1` à `UC3`** | Les trois cas d'usage de la SRS : promotion passthrough entre zones connectées, synchronisation miroir vers une zone isolée, et registre de substitution ou d'amorçage. Voir [SRS et ADR](../../reference/srs-adr/). |
| **`R-nn`** | Un élément de feuille de route ajouté après la spécification initiale, listé sur la [feuille de route](https://tobby-fetch.github.io/tobby-fetch/roadmap.html) et cité par l'amendement de la SRS qui l'implémente. Voir [SRS et ADR](../../reference/srs-adr/). |
| **`B-nnn`** | Un numéro de défaut, cité dans les entrées du [changelog](https://github.com/tobby-fetch/tobby-fetch/blob/main/CHANGELOG.md) des correctifs qui l'ont résolu. |
