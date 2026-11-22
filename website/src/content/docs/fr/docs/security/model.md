---
title: Le modèle de sécurité
description: Comment Tobby décide ce qu'il croit, ce qu'il refuse de faire et où chaque garantie est appliquée — signatures, défauts sûrs, aucune connexion non configurée, franchissement du média, périmètres de confiance, politique de vulnérabilités, authentification et audit.
sidebar:
  order: 1.5
---

Cette page explique le modèle dans son ensemble : les règles que Tobby suit,
dans l'ordre où une décision se prend, et où chacune est appliquée et montrée.
Elle ne répète pas le détail. Chaque section se termine par la page qui le
porte. Pour une page imprimable avec l'état de livraison de chaque
fonctionnalité, voir [le modèle de sécurité en une page](../one-pager/) ; pour
les adversaires et les flux réseau, le [modèle de menaces](../threat-model/).

## 1. Tobby ne signe rien

Tobby transporte du contenu ; il ne crée jamais d'authenticité. Il ne détient
aucune clé de signature, dans aucun mode, et aucun code en lui ne signe. Une
signature est produite en amont, par la chaîne de qualification de
l'organisation, avec cosign, et Tobby ne fait que la **vérifier**.

La conséquence est une répartition nette des rôles. Compromettre une instance
Tobby ne donne aucune capacité de signature, et une organisation peut utiliser
n'importe quelle infrastructure de signature, un jeton matériel ou une garde
partagée, sans que Tobby ait à le savoir. La
[voie d'urgence](../../recipes/emergency-path/) elle-même suit cette règle :
vous signez, avec votre clé, hors de Tobby.

La vérification est à clé, entièrement hors ligne (ni Fulcio, ni Rekor, aucun
service en ligne), et elle a lieu deux fois : à l'import dans le store, puis
avant tout push vers une destination.
→ [Signatures, trust roots et liste d'autorisation](../content-trust/)

## 2. L'authenticité vient des trust roots de la destination

Une recipe est de confiance quand sa signature cosign se vérifie contre un
**trust root** configuré sur l'instance qui la reçoit : une clé publique
nommée, fournie en ligne, par fichier, ou par une URL HTTPS lue une fois au
chargement de la configuration, jamais au moment de la vérification. Les roots
forment un ensemble, de sorte qu'une clé se renouvelle par recouvrement, sans
instant où la vérification serait assouplie.

Une recipe signée épingle chaque ingrédient par son digest : une seule
signature valide atteste donc les octets exacts de toute la livraison. Un
contenu qui échoue à un contrôle de digest ou de signature n'est ni poussé ni
servi : **la recipe est bloquée en entier**, et les recipes sœurs qui se
vérifient ne sont pas touchées.
→ [Signatures, trust roots et liste d'autorisation](../content-trust/), [erreurs `TBY-SIG-*`](../../reference/errors/#tby-sig-001)

## 3. Sécurisé par défaut : les assouplissements sont visibles

Une posture plus faible n'existe que si quelqu'un l'écrit, et chacune est
annoncée là où les personnes concernées la verront. Trois exceptions ne
s'écrivent pas dans la configuration de l'instance elle-même : l'analyse
est consultative par défaut (`scan.mode: warn`), un `registries.allowlist`
absent ne restreint rien (signalé comme *non déclaré*), et le Retriever, un
document non signé, peut assouplir la politique de vulnérabilités. Les trois
figurent dans le tableau :

| Assouplissement | Comment il se demande | Où il se voit |
| --- | --- | --- |
| Authentification désactivée | `auth.disabled` dans le fichier de configuration ou `TOBBY_AUTH_DISABLED`, jamais par option | bandeau permanent, journal de démarrage, événement d'audit `auth.override_active` |
| Recipes non signées admises | une entrée `trust.scopes` avec `allowUnsigned`, pour des dépôts nommés uniquement | bandeau permanent, [marqueur d'assurance réduite](../reduced-assurance/) sur chaque élément atteint |
| Vulnérabilités signalées sans bloquer | le défaut `scan.mode: warn` ; `block` est opt-in | bandeau consultatif, `TBY-SCN-006` sur chaque élément en infraction |
| Seuil de vulnérabilité relevé, ou dérogations ajoutées, par le Retriever | `spec.scanPolicy` dans le Retriever, qui n'est pas signé ; son seuil remplace celui de l'instance dans les deux sens | le rapport de chaque élément (`threshold_level: retriever`) ; les dérogations du Retriever sur `/admin/policy` ; `TBY-SCN-028` pour une dérogation sans échéance ; aucun événement d'audit |
| Registries contactées sans restriction | aucune clé `registries.allowlist` | signalé comme *non déclaré* |
| Support destiné à une autre zone, ou plus ancien que le dernier importé | une dérogation (`TBY-MED-006`, `TBY-MED-007`), à chaque vérification ou import : `admin` sur l'écran et l'API, quiconque a accès à l'hôte en ligne de commande | événement d'audit `media.override` à chaque application d'une dérogation, à une vérification comme à un import |
| Un FileSet servi sans connexion | `anonymous: true` sur son entrée `files.filesets` | bandeau permanent |

Le Retriever n'est pas signé, par choix de conception : il porte la
confiance de la configuration de l'instance, et son emplacement doit être
protégé comme le fichier de configuration — voir
[le modèle de menaces](../threat-model/#adversaires-et-ce-qui-les-arrête).

Une instance sans compte refuse de démarrer plutôt que de démarrer ouverte
(`TBY-AUTH-001`), et une instance dont un fichier de secret se trouve dans le
store transportable refuse aussi (`TBY-CFG-002`). Il n'existe **aucun
interrupteur global** qui désactive la vérification des signatures : le seul
assouplissement est un périmètre, nommé, lié à des motifs de dépôt explicites.
→ [Authentification et RBAC](../auth-rbac/), [Secrets](../secrets/)

### Interrupteurs de durcissement

L'autre sens existe aussi : deux réglages, désactivés par défaut, qui
**ferment une porte d'entrée** au lieu d'en ouvrir une (SRS FR-098).

| Interrupteur | Ce qu'il ferme | Ce qui fonctionne toujours |
| --- | --- | --- |
| `hardening.disableUnitImport` | l'import unitaire, l'import d'OCI image layout et l'empaquetage de FileSets, à l'écran, par l'API **et** en ligne de commande — [TBY-POL-005](../../reference/errors/#tby-pol-005) | les synchronisations de recipes, les imports de support |
| `hardening.disableRegistryPush` | toute écriture dans la registry embarquée, pour tous les clients et tous les rôles, administrateurs compris — `DENIED` OCI portant [TBY-POL-006](../../reference/errors/#tby-pol-006) | les pulls, les synchronisations de recipes |

Avec les deux, le contenu n'entre dans le store que par des recipes
vérifiées contre les trust roots de l'instance. Un interrupteur de
durcissement ne demande aucun acquittement, mais il est signalé comme tout
réglage de sécurité — le journal de démarrage, `tobby config dump`, l'écran
dont il désactive l'action (affichée désactivée, en nommant le réglage), la
métrique `tobby_hardening_enabled` — et chaque refus est un événement
d'audit `hardening.refused` : un opérateur ne prend jamais un refus pour une
panne. Alimenter l'instance par push (UC3) devient alors impossible, à
dessein.

Un **import de support** (`tobby media import`, l'écran des supports et son
API) n'est pas un import unitaire, et aucun des deux interrupteurs ne le
ferme : c'est la voie normale par laquelle le contenu atteint une zone de
destination. Il n'apporte que ce que livrent les recipes signées du
support, après vérification du support contre les trust roots de cette
instance, et le pousse dans le registre de la zone. Sur l'instance de
destination, le contenu du support porte la classe `recipe`, tirée du
graphe de recipes que le support transporte ; rien de ce qu'il livre n'est
enregistré comme `seed`.

Interrupteurs activés ou non, chaque dépôt porte sa **classe de
provenance** — `recipe`, `unit-import` ou `seed` — dans le navigateur de
contenu, l'API, les enregistrements de journal qui le concernent et la
métrique `tobby_store_unmanaged_repositories` : le contenu qu'aucune recipe
vérifiée ne garantit est toujours compté et toujours visible. La classe est
un fait propre à ce store ; elle n'est jamais écrite dans aucune registry et
ne décide rien de la confiance. → [Configuration](../../reference/configuration/#durcissement)

## 4. Aucune connexion qui n'ait été configurée

Tobby n'ouvre aucune connexion réseau que l'opérateur n'a pas configurée :
pas de télémétrie, pas de rapport de plantage, pas de vérification de mise à
jour. Son trafic sortant est l'ensemble des points d'accès de sa propre
configuration, chacun filtré par la liste d'autorisation des registries quand
il atteint un registry, et acheminé par le proxy et les autorités de
certification privées configurés, sans réglage pour désactiver la vérification
TLS. La liste d'autorisation s'applique à l'hôte **effectivement contacté** ;
quand la substitution de source réécrit un point d'accès, la liste et les
identifiants suivent l'hôte effectif, tandis que les périmètres de confiance
continuent de suivre la référence nominale.

Cela se teste, cela ne se promet pas : chaque exécution de recette part d'un
canari d'egress qui doit échouer depuis la zone isolée, et les exécutions de
bout en bout derrière un proxy qui capture l'egress n'enregistrent de
connexions que vers les points d'accès configurés.
→ [Modèle de menaces et flux réseau](../threat-model/), [Tests et preuves](../../project/tests-and-proofs/)

## 5. Franchir l'air-gap

Côté destination d'un support amovible, rien de ce que dit le support n'est
cru.

- **Le contenu est atteignable depuis une recipe vérifiée, sinon il n'est pas
  poussé.** La vérification part des recipes, contrôle leurs signatures contre
  les trust roots propres à la destination, puis suit leurs digests épinglés
  jusqu'au contenu qu'elles atteignent. Tout ce qui n'est pas atteignable
  depuis une recipe vérifiée n'est jamais poussé et est signalé.
- **Le manifeste du média est un inventaire, pas une preuve.** Il est
  délibérément non signé. Il détecte la corruption et l'incomplétude, et dit à
  une personne ce qu'elle doit attendre. Un manifeste falsifié ne peut pas
  rendre bon un blob mauvais.
- **La vérification est double.** Chaque fichier est contrôlé contre
  l'inventaire *et* contre sa propre adresse de contenu (son digest) : réécrire
  l'inventaire pour qu'il s'accorde avec un blob abîmé ne change rien.
- **Le matériel de confiance présent sur le support est ignoré.** Un fichier de
  trust root déposé sur un support est sans effet, par exigence et par test.
- **Tant qu'une vérification n'a pas validé le support en entier, la
  destination n'en sert rien** : `/v2/` et `/files/` répondent `403`
  (`TBY-MED-030`), pour tous les rôles, sans réglage pour les rouvrir. Une
  fois validé, il ne sert que le contenu qu'atteignent les recipes
  poussables, tel que cette vérification l'a haché, en lecture seule
  (`TBY-MED-033` pour tout le reste) ; une liste de referrers est construite
  à partir des manifestes référents eux-mêmes, jamais de l'index présent sur
  le support.

Les deux seules dérogations (un support adressé à une autre zone, un support
plus ancien que le dernier importé) sont des garde-fous anti-accident : ce ne
sont pas des contrôles de sécurité, car un champ du manifeste peut être
falsifié. C'est pourquoi elles seules peuvent être levées : par un
administrateur sur l'écran et l'API, par quiconque a accès à l'hôte en ligne
de commande. Toute dérogation appliquée est auditée, à l'étape Vérifier — où,
sur l'écran et l'API, une vérification avec dérogation peut ouvrir le verrou
de service — comme à l'import.
→ [Modèle de sécurité des médias](../../air-gap/media-security/), [Parcours d'un média](../../air-gap/media-workflow/)

## 6. Les périmètres de confiance

Un périmètre de confiance est la seule forme que prennent l'assouplissement ou
le cloisonnement. Il est nommé, lié à des motifs de dépôt explicites (comparés
au chemin nominal de cookbook de la recipe, le premier qui correspond l'emporte,
aucune correspondance donne le défaut strict), et il fait l'une de deux choses :

- **renoncer** : `allowUnsigned` admet des recipes sans signature, et tout ce
  qu'une telle recipe atteint porte un marqueur d'assurance réduite visible,
  dans l'API, les rapports de tâche, l'interface, les journaux, les métriques
  et le manifeste du média (où la destination le traite comme une déclaration
  de la source, jamais comme un verdict) ;
- **restreindre** : `roots` limite les trust roots autorisés à vérifier les
  recipes de ce périmètre. Cela réduit la confiance et ne renonce à rien ;
  c'est ainsi qu'une clé dédiée, comme une clé d'urgence, est cantonnée à un
  seul cookbook.

→ [Signatures, trust roots et liste d'autorisation](../content-trust/), [Le marqueur d'assurance réduite](../reduced-assurance/), [La voie d'urgence](../../recipes/emergency-path/)

## 7. Analyse de vulnérabilités et politique

Tobby analyse les images qu'il récupère avec un binaire Trivy épinglé, hors
ligne, avant que quoi que ce soit d'une recipe soit poussé ou écrit sur un
support. L'analyse ne peut pas être désactivée : il n'y a aucun réglage
`enabled`, et le mode consultatif par défaut est un mode visible, pas une
absence d'analyse.

Ce qui est analysé est décidé par le **contenu**, pas par le type déclaré : une
image publiée comme « artefact » ou comme FileSet est détectée d'après ses
octets et analysée comme une image. Une politique unique (un seuil CVSS,
consultative ou bloquante, des dérogations tolérées) est composée à partir de
l'instance, du Retriever et de la recipe. Une recipe ne peut que la
durcir ; le seuil du Retriever remplace celui de l'instance et peut
l'assouplir autant que la durcir. La base de vulnérabilités voyage avec Tobby lui-même, et son âge figure
dans chaque rapport ; un scanner absent, périmé ou non vérifiable est un mode
dégradé visible (`TBY-SCN-001` à `TBY-SCN-004`), jamais un passage silencieux.
L'analyse répond à un adversaire différent de celui des signatures : *un
contenu honnêtement signé, mais porteur de vulnérabilités connues*.
→ [Analyse de vulnérabilités](../vulnerability-scanning/), [Base de vulnérabilités](../../air-gap/vulnerability-database/)

## 8. Authentification et autorisation

L'authentification est activée par défaut dans les deux modes. Quatre méthodes
coexistent : comptes locaux (authentification basique, hachages argon2id
calculés par l'outil), jetons d'API statiques (affichés une fois, stockés sous
forme de hachage, expiration facultative), OpenID Connect et SAML 2.0. Activer
une méthode fédérée ne désactive jamais les locales : la perte du fournisseur
d'identité ne verrouille donc pas l'instance. Un utilisateur fédéré ne reçoit un
rôle que par un mappage de groupes explicite ; Tobby n'en accorde jamais par
défaut.

Trois rôles fixes, `viewer < operator < admin`, contrôlent chaque surface :
l'interface, l'API, la registry embarquée (par méthode HTTP, pour que les
clients standard fonctionnent) et le serveur de fichiers. Le tableau des
niveaux requis est généré à partir des routes que le serveur monte réellement
et il est vérifié par un test : une route ne peut pas être livrée sans niveau
revu. Le dernier administrateur ne peut être ni supprimé ni rétrogradé. Les
échecs d'authentification sont limités par origine.

Une session de navigateur n'autorise rien par son seul cookie. Toute requête
qui modifie l'état et qu'elle authentifie, dans l'interface comme dans l'API,
porte le jeton anti-falsification de la session, et la requête est refusée
quand le navigateur indique qu'elle vient d'une autre origine. Chaque réponse
de l'écouteur porte les en-têtes de sécurité du navigateur, et chaque page
HTML une Content-Security-Policy qui n'autorise aucun script sans empreinte.
→ [Authentification, comptes et RBAC](../auth-rbac/), [Identité d'entreprise](../enterprise-identity/)

Chaque entrée qui franchit une frontière de confiance est bornée, décodée strictement et contrôlée selon sa grammaire → [Validation des entrées](../input-validation/)

## 9. Audit

Les actions pertinentes pour la sécurité émettent des événements d'audit au
schéma fixe de six champs (acteur, action, cible, résultat, horodatage,
origine), sous forme d'enregistrements de journal structurés séparables par un
seul champ marqueur. L'origine est l'adresse réseau du client ; les en-têtes de
transfert ne sont pas crus. Connexions et refus, changements de comptes et de
jetons, actions qui touchent au contenu, expirations de sessions et de jetons,
refus d'analyse, refus de durcissement et vérification du store sont consignés, et chaque
synchronisation porte un identifiant de run qui la suit dans les journaux, les
tâches et les rapports.
→ [Journal d'audit](../audit-log/), [Métriques et journaux](../../reference/metrics-logs/)

## Ce que le modèle ne prétend pas

Énoncer les limites fait partie du modèle.

- Tobby ne **qualifie** pas le contenu amont : il garantit que ce que
  l'organisation a signé est ce qui arrive, pas que c'est bon.
- Le journal d'audit est une **preuve opérationnelle**, pas une ancre de
  confiance signée. Envoyez-le à votre SIEM pour gagner de l'indépendance vis-à-vis de l'hôte.
- La garde des clés (HSM, KMS, révocation) revient à l'organisation.
- Le chiffrement au repos est l'affaire du volume ; Tobby garde ses secrets dans
  le répertoire d'état, hors du store transportable, et refuse de démarrer s'ils
  devaient voyager avec lui.
- La sécurité physique et celle de l'hôte de l'instance sont hors du périmètre
  de Tobby.

→ [Limites](../../discover/limits/), [Modèle de menaces et flux réseau](../threat-model/)

## Vérifier plutôt que croire

La chaîne de build du binaire que vous exécutez (provenance SLSA Build L3,
builds reproductibles, SBOM signés) peut être vérifiée indépendamment :
[Vérifier une release](../../project/verify-a-release/). Les garanties
ci-dessus s'appuient sur des tests nommés et des recettes d'acceptation :
[Tests et preuves](../../project/tests-and-proofs/).
