---
title: Erreurs et dépannage (TBY-*)
description: La taxonomie d'erreurs complète — quoi, cause probable, action corrective par code, remédiable hors-ligne ou non, et ce que chaque code bloque.
sidebar:
  order: 4
---

Chaque erreur visible de Tobby porte un code court et stable
(`TBY-<domaine>-<nnn>`) et un message structuré — ce qui s'est passé, la
cause probable, l'action corrective — rendu à l'identique par l'interface
web, la CLI et l'API (en documents « problem » RFC 9457). Les codes font
partie du contrat produit : **un code n'est jamais renuméroté ni
réutilisé**, et sa classe détermine le
[code de sortie du processus](#codes-de-sortie).

Cette page est produite depuis le catalogue de la taxonomie embarqué dans
le binaire (`internal/taxonomy`). Chaque code a son propre titre : l'ancre
`#tby-reg-003` est stable — ce sont les mêmes ancres que résoudra le guide
de dépannage embarqué (`/help#TBY-REG-003`).

Un test parcourt le catalogue et fait échouer la compilation dès qu'un code
n'a pas sa section ici, dans l'une ou l'autre langue — le catalogue est la
source de vérité, cette page en est le rendu publié.

**Comment lire chaque entrée :**

- **Remédiable hors-ligne** — si un opérateur en zone isolée peut résoudre
  la condition avec ses seuls moyens locaux (configuration, disque,
  comptes), ou si la correction exige d'atteindre un registre source ou la
  chaîne de qualification amont.
- **Bloque** — la portée : toute l'instance (refus de démarrage), une tâche
  ou une recipe, ou seulement la requête en cours.
- **Dérogation** — les refus de politique se lèvent en modifiant la
  configuration auditée qui les impose (liste blanche, clés de confiance,
  comptes) ; les échecs de vérification ne se dérogent jamais. Les seules
  dérogations à chaud sont réservées à l'admin, auditées, et se situent
  toutes deux à l'import d'un média : la zone qui ne correspond pas
  (TBY-MED-006) et le garde-fou de fraîcheur (TBY-MED-007).

## Codes de sortie

La classe d'un code décide du code de sortie du processus : c'est la
projection en ligne de commande de cette taxonomie (FR-066, amendement
R-08). **La table ci-dessous est produite depuis le code**
(`internal/taxonomy`), et un test fait échouer la compilation dès que les
deux divergent — dans les deux sens : un code que la table n'énumère pas,
ou une ligne que rien ne peut produire.

Elle est couverte par la promesse de versionnement sémantique du projet :
retirer un code ou en renuméroter un est un changement cassant. La colonne
`Nom` est le nom machine stable de la ligne ; il fait partie du contrat et
n'est jamais traduit.

<!-- generated:exit-codes -->
| Code | Nom | Signification |
|---|---|---|
| `0` | `ok` | **Succès** — La commande a fait ce qui était demandé. |
| `1` | `failure` | **Échec opérationnel** — Erreur réseau, stockage ou interne — tout ce qui a échoué sans verdict de politique ni de vérification. Tous les codes de la classe opérationnelle sortent ici, ainsi qu'une commande qui a déclenché une tâche que l'instance a ensuite fait échouer. |
| `2` | `usage` | **Erreur d'usage** — Drapeau erroné, commande inconnue, ou commande à qui l'on demande ce qu'elle ne peut pas honorer. Le message porte l'indication `see 'tobby … --help'` désignant la commande dont l'analyse a échoué. |
| `3` | `policy` | **Refus de politique** — Refusé par une politique ou une autorisation explicite : liste blanche de registres, rôles, refus de démarrage sécurisés par défaut, immutabilité d'un tag de recipe, les deux garde-fous de média levables par un administrateur. |
| `4` | `verification` | **Échec de vérification** — Un contrôle d'intégrité ou d'authenticité a échoué : signatures, digests épinglés, types d'artefact, sommes de contrôle du média. La classe la plus sévère, et la seule qu'aucune levée ne rouvre. |
| `5` | `changes-planned` | **Changements prévus** — Une exécution sans effet de bord a trouvé du travail à faire — `tobby sync --dry-run` sur un Retriever porteur de changements. C'est un succès qui a quelque chose à dire, pour qu'une barrière d'intégration puisse s'y brancher sans y voir une compilation cassée. |
<!-- /generated:exit-codes -->

La [référence CLI](../../reference/cli/) — commandes, drapeaux, contrat
`--output json`, mode non interactif — reste en anglais jusqu'au jalon 7.

## Authentification et comptes (TBY-AUTH)

### TBY-AUTH-001

- **Quoi :** l'instance refuse de démarrer : aucun compte local n'est
  configuré.
- **Cause probable :** aucun compte administrateur n'a encore été créé sur
  cette instance, ou son répertoire d'état a été réinitialisé.
- **Action corrective :** sur l'hôte de l'instance, exécutez
  `tobby user add --role admin <nom>` (l'outil calcule le hachage du mot de
  passe), puis redémarrez l'instance. Tobby ne démarre jamais avec une
  interface ouverte.
- **Remédiable hors-ligne :** oui · **Bloque :** toute l'instance (refus de démarrage sécurisé par défaut — classe politique, sortie 3)

### TBY-AUTH-002

- **Quoi :** l'authentification a échoué.
- **Cause probable :** les identifiants sont inconnus ou le mot de passe
  est erroné. Le message est volontairement sans paramètre : il ne révèle
  jamais si le compte existe.
- **Action corrective :** vérifiez le nom du compte et le mot de passe,
  puis réessayez. Les comptes se gèrent sur l'hôte avec `tobby user`.
- **Remédiable hors-ligne :** oui · **Bloque :** la tentative de connexion seulement

### TBY-AUTH-003

- **Quoi :** cette action n'est pas autorisée pour votre rôle.
- **Cause probable :** elle requiert le rôle *\<rôle\>*.
- **Action corrective :** connectez-vous avec un compte disposant de ce
  rôle, ou demandez à un administrateur de vous l'accorder.
- **Remédiable hors-ligne :** oui · **Bloque :** l'action, pour cette session (classe politique, sortie 3)

### TBY-AUTH-004

- **Quoi :** le formulaire n'a pas pu être soumis en toute sécurité.
- **Cause probable :** le jeton anti-falsification est absent ou a expiré —
  la page est probablement restée ouverte longtemps.
- **Action corrective :** le formulaire a été rechargé avec un jeton neuf :
  soumettez-le à nouveau.
- **Remédiable hors-ligne :** oui · **Bloque :** la requête soumise seulement

### TBY-AUTH-005

- **Quoi :** votre session a expiré.
- **Cause probable :** aucune activité pendant une durée supérieure à la
  vie de la session (`auth.sessionTTL`, 12 h par défaut), ou l'instance a
  redémarré — les sessions vivent en mémoire.
- **Action corrective :** reconnectez-vous ; vous reviendrez à la page où
  vous étiez.
- **Remédiable hors-ligne :** oui · **Bloque :** la session seulement

### TBY-AUTH-006

- **Quoi :** le mot de passe actuel est erroné (changement de mot de passe
  en libre-service).
- **Cause probable :** le mot de passe actuel envoyé avec la demande ne
  correspond pas à celui du compte.
- **Action corrective :** saisissez à nouveau votre mot de passe actuel,
  puis réessayez. S'il est perdu, un administrateur peut en définir un
  nouveau sur l'hôte avec `tobby user passwd <nom>`.
- **Remédiable hors-ligne :** oui · **Bloque :** le changement de mot de passe seulement

### TBY-AUTH-007

- **Quoi :** le nouveau mot de passe a été refusé.
- **Cause probable :** il est vide, identique au mot de passe actuel, ou sa
  confirmation ne correspond pas.
- **Action corrective :** choisissez un mot de passe non vide et différent
  de l'actuel, saisissez la même valeur dans les deux champs, puis
  soumettez à nouveau.
- **Remédiable hors-ligne :** oui · **Bloque :** le changement de mot de passe seulement

### TBY-AUTH-008

- **Quoi :** le compte n'a pas pu être créé ou modifié.
- **Cause probable :** le login est vide, le rôle n'est pas l'un des trois
  (`viewer`, `operator`, `admin`), ou le mot de passe est vide ou mal
  ressaisi dans le champ de confirmation.
- **Action corrective :** indiquez un login non vide, choisissez l'un des
  trois rôles, saisissez le même mot de passe non vide dans les deux
  champs, puis soumettez à nouveau.
- **Remédiable hors-ligne :** oui · **Bloque :** l'opération sur le compte seulement

### TBY-AUTH-009

- **Quoi :** le compte *\<nom\>* existe déjà.
- **Cause probable :** cette instance possède déjà un compte local portant
  ce login ; les logins sont uniques.
- **Action corrective :** choisissez un autre login, ou gérez le compte
  existant depuis l'écran des comptes — son rôle se change et son mot de
  passe se réinitialise sans en créer un second.
- **Remédiable hors-ligne :** oui · **Bloque :** la création du compte seulement

### TBY-AUTH-010

- **Quoi :** aucun compte nommé *\<nom\>* sur cette instance.
- **Cause probable :** le compte a été supprimé, ou le login est mal
  orthographié — l'écran depuis lequel vous avez agi est peut-être
  antérieur à la suppression.
- **Action corrective :** rechargez l'écran des comptes pour voir la liste
  à jour, puis réessayez sur un compte qui y figure.
- **Remédiable hors-ligne :** oui · **Bloque :** l'opération sur le compte seulement

### TBY-AUTH-011

- **Quoi :** refusé : *\<nom\>* est le dernier administrateur de cette
  instance.
- **Cause probable :** le supprimer, ou lui retirer le rôle admin, ne
  laisserait plus personne pour administrer l'instance — et une instance
  sans aucun compte refuse purement et simplement de démarrer.
- **Action corrective :** créez d'abord un second compte admin
  (`tobby user add --role admin <nom>` sur l'hôte donne le même résultat),
  puis réessayez.
- **Remédiable hors-ligne :** oui · **Bloque :** la suppression ou la rétrogradation seulement (classe politique, sortie 3)

### TBY-AUTH-012

- **Quoi :** trop de tentatives d'authentification échouées depuis votre
  adresse réseau ; les tentatives suivantes sont temporairement refusées
  (HTTP 429).
- **Cause probable :** des identifiants erronés présentés à répétition
  depuis la même origine. Chaque vérification échouée coûte un calcul
  argon2id volontairement onéreux : l'instance limite les origines qui
  échouent en boucle plutôt que de brûler du CPU pour elles.
- **Action corrective :** patientez un instant, vérifiez l'identifiant (mot
  de passe du compte ou secret du jeton), puis réessayez. Derrière une
  sortie partagée, un autre client est peut-être mal configuré — le journal
  d'audit liste les tentatives échouées et les noms de compte revendiqués.
- **Remédiable hors-ligne :** oui · **Bloque :** l'origine réseau, temporairement

## Configuration (TBY-CFG)

### TBY-CFG-001

- **Quoi :** la configuration est invalide.
- **Cause probable :** énoncée verbatim dans le message (la contrainte
  violée).
- **Action corrective :** corrigez le réglage indiqué (précédence :
  drapeaux, puis variables `TOBBY_*`, puis fichier YAML), vérifiez le
  résultat avec `tobby config dump`, puis redémarrez. Voir la
  [référence de configuration](../../reference/configuration/).
- **Remédiable hors-ligne :** oui · **Bloque :** toute l'instance (refus de démarrage) ou la commande qui a chargé la configuration

### TBY-CFG-002

- **Ce qui s'est passé :** l'instance refuse de démarrer — un fichier de
  secret est configuré à l'intérieur du store transportable.
- **Cause probable :** `state.root`, `registries.credentialsFile` ou
  `server.tls.keyFile` se résout sous `storage.root`. Le store est confié
  à un porteur puis branché sur une machine d'une autre zone : tout ce qui
  est dessous est réputé lu par quelqu'un d'autre (NFR-020).
- **Action corrective :** déplacez chaque fichier listé hors du store — le
  répertoire d'état est sa place — mettez le réglage à jour, puis
  redémarrez. Le contrôle passe par le système de fichiers : un chemin qui
  atteint le store via un lien symbolique compte comme étant dedans, et le
  message indique le chemin résolu qui a tranché.
- **Corrigeable hors ligne :** oui · **Bloque :** l'instance entière (refus au démarrage)

## Réseau sortant et TLS (TBY-NET)

### TBY-NET-001

- **Quoi :** la configuration du proxy sortant est inutilisable :
  l'instance refuse de démarrer.
- **Cause probable :** *\<réglage\>* vaut *\<proxy\>*, qui n'est pas une
  URL de proxy exploitable (`http://` ou `https://` avec un hôte attendus).
- **Action corrective :** corrigez le réglage sous la forme
  `http://proxy.example.com:3128`, sans identifiants dans l'URL : ceux-ci
  relèvent de `network.proxy.username` et `network.proxy.password`, qui
  n'apparaissent jamais dans les journaux ni dans `tobby config dump`.
  Puis redémarrez.
- **Remédiable hors-ligne :** oui · **Bloque :** toute l'instance (refus de démarrage)

### TBY-NET-002

- **Quoi :** une autorité de certification configurée n'a pas pu être
  chargée.
- **Cause probable :** *\<source\>* est illisible, ne contient aucun bloc
  PEM `CERTIFICATE`, ou n'ajoute aucune autorité que l'instance ne
  connaissait déjà.
- **Action corrective :** vérifiez que le fichier existe, est lisible par
  l'instance et contient le certificat de l'autorité au format PEM
  (`openssl x509 -in <fichier> -noout -subject` doit afficher un sujet),
  puis redémarrez. Faire confiance à une autorité privée est la voie prévue
  pour joindre une registry interne ; aucun réglage ne désactive la
  vérification des certificats.
- **Remédiable hors-ligne :** oui · **Bloque :** toute l'instance (refus de démarrage)

### TBY-NET-003

- **Quoi :** le certificat de l'écouteur est inutilisable : l'instance
  refuse de servir.
- **Cause probable :** *\<source\>* est absent, illisible, n'est pas une
  paire certificat/clé au format PEM, ou la clé ne correspond pas au
  certificat.
- **Action corrective :** vérifiez `server.tls.certFile` et
  `server.tls.keyFile` : deux fichiers PEM lisibles formant une même paire.
  Retirez les deux pour laisser Tobby générer un certificat auto-signé —
  son empreinte est affichée au démarrage. Puis redémarrez.
- **Remédiable hors-ligne :** oui · **Bloque :** toute l'instance (refus de démarrage)

### TBY-NET-004

- **Quoi :** la paire de certificat soumise depuis les surfaces
  d'administration a été refusée ; l'instance continue de servir le
  certificat qu'elle avait déjà.
- **Cause probable :** énoncée verbatim dans le message (paire
  discordante, certificat expiré, chemins non configurés…).
- **Action corrective :** soumettez un certificat PEM et la clé privée
  correspondante, encore valide, sur une instance dont
  `server.tls.certFile` et `server.tls.keyFile` sont configurés. Rien n'a
  été écrit : l'écouteur n'est pas affecté.
- **Remédiable hors-ligne :** oui · **Bloque :** le remplacement soumis seulement — l'instance continue de servir

## Validation des recipes et retrievers (TBY-VAL)

### TBY-VAL-001

- **Quoi :** le fichier recipe ou retriever est invalide.
- **Cause probable :** dans *\<fichier\>*, à *\<chemin\>* : la contrainte
  violée est nommée.
- **Action corrective :** corrigez le champ à ce chemin pour satisfaire la
  contrainte, puis soumettez le fichier à nouveau. La grammaire normative
  est sur le
  [site de la spécification](https://tobby-fetch.github.io/recipe-spec/).
- **Remédiable hors-ligne :** oui · **Bloque :** le fichier soumis / la recipe concernée

## Accès aux registres sources (TBY-REG)

### TBY-REG-001

- **Quoi :** la référence n'a pas pu être interprétée.
- **Cause probable :** *\<référence\>* n'est pas une référence valide
  d'image ou de chart.
- **Action corrective :** utilisez la forme `registry/repository:tag` ou
  `registry/repository@sha256:…` — par exemple
  `docker.io/library/redis:7.2`.
- **Remédiable hors-ligne :** oui · **Bloque :** l'opération utilisant cette référence

### TBY-REG-002

- **Quoi :** le registre source est injoignable.
- **Cause probable :** aucune route réseau vers *\<hôte\>*, ou le registre
  est arrêté (DNS, proxy ou pare-feu sur le chemin).
- **Action corrective :** vérifiez la connectivité vers l'hôte depuis
  l'hôte de l'instance et les réglages de proxy, puis réessayez.
- **Remédiable hors-ligne :** non (il faut la route réseau vers la source) · **Bloque :** la tâche concernée ; réessayée avec repli borné

### TBY-REG-003

- **Quoi :** le registre source a refusé l'authentification.
- **Cause probable :** identifiants absents ou expirés pour *\<hôte\>*.
- **Action corrective :** renseignez les identifiants de cet hôte dans le
  fichier pointé par `registries.credentialsFile`, puis relancez l'import.
- **Remédiable hors-ligne :** non (la correction de configuration est locale, mais le nouvel essai exige le registre source) · **Bloque :** la tâche concernée

### TBY-REG-004

- **Quoi :** l'inspection distante a dépassé le délai.
- **Cause probable :** *\<hôte\>* n'a pas répondu en *\<délai\>*.
  Volontairement distinct d'« injoignable ».
- **Action corrective :** réessayez ; si cela persiste, vérifiez le chemin
  réseau ou augmentez `import.inspectTimeout` dans la configuration.
- **Remédiable hors-ligne :** non (il faut la réponse de la source) · **Bloque :** l'inspection ou l'import concerné

### TBY-REG-005

- **Quoi :** la référence n'existe pas sur le registre source.
- **Cause probable :** *\<référence\>* est introuvable — nom ou tag erroné,
  ou supprimée en amont.
- **Action corrective :** vérifiez le nom du dépôt et le tag sur le
  registre source, puis corrigez la référence.
- **Remédiable hors-ligne :** non (la vérité est sur le registre source) · **Bloque :** la tâche concernée

### TBY-REG-006

- **Quoi :** aucune version disponible ne satisfait l'expression demandée.
- **Cause probable :** pour *\<référence\>*, l'expression
  *\<contrainte\>* ne correspond à aucun des tags disponibles.
- **Action corrective :** confrontez l'expression de version aux tags
  réellement publiés (les contraintes semver ne considèrent que les tags
  interprétables en semver). Tobby ne se replie jamais silencieusement sur
  une autre version.
- **Remédiable hors-ligne :** non (la résolution exige la liste des tags de la source) · **Bloque :** la recipe ou l'ingrédient concerné

### TBY-REG-007

- **Quoi :** la source a renvoyé une réponse partielle inexploitable
  pendant la reprise d'un gros transfert.
- **Cause probable :** pour *\<référence\>* : un 206 démarrant au mauvais
  octet, un `Content-Range` contredisant le manifeste, une plage refusée,
  ou un contenu qui a changé entre deux essais. Le registre source, ou un
  cache placé devant lui, n'honore pas les plages d'octets de manière
  cohérente. Opérationnel, pas un verdict de vérification : rien n'a été
  prouvé faux sur le contenu — la conversation à son sujet s'est rompue.
- **Action corrective :** relancez la tâche : le transfert repart de la
  dernière position vérifiée, ou de zéro si le contenu de la source a
  changé. Si cela se reproduit sur la même source, mettez
  `transfer.resumeThreshold: 0` pour désactiver la reprise intra-blob, et
  vérifiez le proxy cache éventuel sur le chemin.
- **Remédiable hors-ligne :** non (côté source ; le contournement `resumeThreshold: 0` est local) · **Bloque :** la tâche concernée

### TBY-REG-008

- **Quoi :** l'index de la source ne porte pas une plateforme demandée par
  la recipe.
- **Cause probable :** pour *\<référence\>*, aucun enfant de l'index ne
  correspond à *\<plateformes\>* ; l'index publie *\<disponibles\>*. Un
  sélecteur de plateforme s'écrit `os/arch` avec un variant **facultatif**
  (RECIPE-SPEC §7.1) : un variant omis correspond à n'importe lequel, un
  variant nommé doit correspondre exactement. Les registres décrivent
  couramment leur enfant arm64 comme `linux/arm64` avec le variant `v8` :
  un sélecteur nommant un variant que la source ne publie pas ne
  correspond à rien.
- **Action corrective :** confrontez la liste `platforms` de l'ingrédient à
  ce que la source publie réellement (`docker manifest inspect`, ou le
  rapport d'inspection d'un import unitaire) et corrigez-la. Tobby
  n'abandonne jamais silencieusement une plateforme demandée.
- **Remédiable hors-ligne :** non (la vérité est dans l'index de la source) · **Bloque :** l'ingrédient concerné

## Refus de politique (TBY-POL)

### TBY-POL-001

- **Quoi :** le registre n'est pas dans la liste blanche.
- **Cause probable :** *\<hôte\>* ne fait pas partie des registres
  autorisés en source ou en destination ; le transfert a été refusé avant
  tout échange.
- **Action corrective :** si ce registre est légitime, ajoutez-le à
  `registries.allowlist` dans la configuration ; la modification est
  journalisée en audit.
- **Remédiable hors-ligne :** oui · **Bloque :** tout transfert touchant cet hôte, avant toute donnée (classe politique, sortie 3) ; levé par une modification de configuration auditée

### TBY-POL-002

- **Quoi :** ce contenu ne peut pas être supprimé individuellement.
- **Cause probable :** *\<dépôt\>* est géré par les recipes nommées : le
  supprimer ici serait défait par la prochaine synchronisation.
- **Action corrective :** supprimez plutôt la recipe qui le gère — son
  contenu exclusif est nettoyé avec elle. Seul le contenu importé
  unitairement est supprimable individuellement.
- **Remédiable hors-ligne :** oui · **Bloque :** la demande de suppression seulement (classe politique, sortie 3)

### TBY-POL-003

- **Quoi :** ce contenu ne peut pas être supprimé d'ici.
- **Cause probable :** *\<dépôt\>* a été poussé via l'API registre standard
  (`/v2/`) par un client externe : sa provenance n'est ni une recipe ni un
  import unitaire.
- **Action corrective :** la suppression individuelle ne couvre que le
  contenu importé unitairement. Gérez le contenu poussé directement avec
  l'outillage registre standard qui l'a poussé.
- **Remédiable hors-ligne :** oui · **Bloque :** la demande de suppression seulement (classe politique, sortie 3)

### TBY-POL-004

- **Quoi :** cette version de recipe est déjà publiée, avec un contenu
  différent.
- **Cause probable :** *\<référence\>* pointe déjà sur un digest publié ;
  le document proposé en publierait un autre. Une recipe cuite est
  immuable.
- **Action corrective :** publiez le changement sous un nouveau
  `metadata.version` et un nouveau tag. Republier une version sur un
  contenu différent changerait en silence ce que des zones ont déjà
  résolu. Republier le document identique est un no-op, pas cette erreur.
- **Remédiable hors-ligne :** non (une nouvelle version signée vient de la chaîne de qualification) · **Bloque :** la publication seulement (classe politique, sortie 3) ; jamais dérogeable

## Vérification des signatures et digests (TBY-SIG)

### TBY-SIG-001

- **Quoi :** la signature de la recipe n'a pas pu être vérifiée.
- **Cause probable :** aucune clé de confiance configurée ne valide la
  signature de *\<recipe\>* (les empreintes essayées sont listées).
- **Action corrective :** vérifiez que les clés de confiance de la zone
  incluent la clé qui a signé cette recipe (voir
  [Signatures, clés de confiance et liste blanche](../../security/content-trust/)).
  Une recipe non vérifiée n'est jamais admise.
- **Remédiable hors-ligne :** oui, quand la bonne clé publique est disponible localement (les clés de confiance sont la configuration de la destination) ; une recipe réellement non signée ou mal signée doit être re-signée en amont · **Bloque :** la recipe concernée (classe vérification, sortie 4) ; jamais dérogeable

### TBY-SIG-002

- **Quoi :** un digest épinglé ne correspond pas au contenu récupéré.
- **Cause probable :** *\<référence\>* épingle un digest mais le registre
  en a servi un autre — le contenu a changé ou a été altéré.
- **Action corrective :** ne forcez pas le transfert. Vérifiez le registre
  source et la recipe ; si le changement est légitime, une recipe
  re-signée épinglant le nouveau digest est nécessaire.
- **Remédiable hors-ligne :** non · **Bloque :** l'ingrédient ou la recipe concerné (classe vérification, sortie 4) ; jamais dérogeable

### TBY-SIG-003

- **Quoi :** le type de l'artefact ne correspond pas à la déclaration de la
  recipe.
- **Cause probable :** *\<référence\>* déclare un `artifactType` mais le
  registre en a servi un autre — le tag a pu être réutilisé pour un autre
  contenu (garde anti réutilisation de tag et confusion de dépôt).
- **Action corrective :** vérifiez le repository source. Si le changement
  de type est légitime, mettez à jour et re-signez la recipe ; sinon
  considérez la source comme compromise.
- **Remédiable hors-ligne :** non · **Bloque :** l'ingrédient ou la recipe concerné (classe vérification, sortie 4) ; jamais dérogeable

## Limites de la destination (TBY-DST)

### TBY-DST-001

- **Quoi :** le registre de destination ne peut pas stocker cette
  référence.
- **Cause probable :** *\<référence\>* dépasse une limite de la destination
  (la limite est nommée — typiquement une contrainte de longueur ou de
  nommage).
- **Action corrective :** raccourcissez le chemin relocalisé ou ajustez le
  nommage de destination (`destination.basePath`, `storage.basePrefix`) ;
  le refus a eu lieu avant tout push.
- **Remédiable hors-ligne :** oui · **Bloque :** le push de cette référence seulement

## Charts Helm (TBY-CHT)

### TBY-CHT-001

- **Quoi :** le chart Helm n'embarque pas une de ses dépendances.
- **Cause probable :** *\<chart\>* déclare la dépendance *\<dépendance\>*
  mais ne l'embarque pas sous `charts/` — il ne peut pas se déployer
  hors-ligne.
- **Action corrective :** ré-empaquetez le chart avec ses dépendances
  embarquées (`helm dependency build` puis `helm package`), publiez-le,
  puis relancez l'import.
- **Remédiable hors-ligne :** non (le chart doit être ré-empaqueté là où il est construit) · **Bloque :** l'import de ce chart (classe vérification, sortie 4)

## Store local et état (TBY-STO)

### TBY-STO-001

- **Quoi :** le store local n'a pas pu être lu.
- **Cause probable :** énoncée verbatim (volume démonté, permissions,
  erreur d'E/S…).
- **Action corrective :** vérifiez que le répertoire de stockage existe,
  est monté et lisible par l'instance, puis réessayez.
- **Remédiable hors-ligne :** oui · **Bloque :** l'opération concernée ; une condition persistante affecte toute l'instance

### TBY-STO-002

- **Quoi :** l'écriture dans le store local a échoué.
- **Cause probable :** énoncée verbatim — le plus souvent l'espace libre ou
  les permissions.
- **Action corrective :** vérifiez l'espace libre et les permissions du
  répertoire de stockage, puis relancez l'opération.
- **Remédiable hors-ligne :** oui · **Bloque :** l'opération concernée ; une condition persistante affecte toute l'instance

### TBY-STO-003

- **Quoi :** l'écriture du téléchargement partiel dans le répertoire d'état
  a échoué. Volontairement distinct de TBY-STO-002 : le répertoire d'état
  et le store ont des propriétaires, des dimensionnements et des
  corrections différents.
- **Cause probable :** le chemin nommé n'a pas pu être écrit — le plus
  souvent un manque d'espace libre, ou des permissions que l'utilisateur de
  l'instance n'a pas sur la racine d'état.
- **Action corrective :** libérez de l'espace sur le répertoire d'état (il
  héberge temporairement une copie de chaque blob reprenable) ou corrigez
  ses permissions, puis relancez la tâche. Abaissez
  `transfer.resumeThreshold` pour rendre moins de blobs reprenables, ou
  mettez-le à `0` pour transférer chaque blob directement vers le store
  sans fichier temporaire.
- **Remédiable hors-ligne :** oui · **Bloque :** les transferts reprenables concernés

### TBY-STO-004

- **Ce qui s'est passé :** l'opération a été refusée avant de démarrer — la
  cible n'a pas assez d'espace libre (FR-055).
- **Cause probable :** l'écriture projetée dépasse l'espace libre de la
  cible moins la marge de sécurité configurée
  (`preflight.safetyMarginPercent`, 10 % par défaut). Le message énonce le
  manque exact, en octets.
- **Action corrective :** libérez au moins le nombre d'octets annoncé sur
  la cible, visez un volume plus grand, ou supprimez du contenu qui n'est
  plus référencé. Abaisser `preflight.safetyMarginPercent` n'a de sens que
  si vous acceptez de remplir le volume ; `preflight.disabled: true`
  supprime la vérification et l'annonce au démarrage.
- **Corrigeable hors ligne :** oui · **Bloque :** la synchronisation ou l'export concerné ; rien n'est écrit

### TBY-STO-005

- **Ce qui s'est passé :** le système de fichiers de la cible ne peut pas
  contenir un fichier de cette taille (FR-055).
- **Cause probable :** la cible est formatée avec un système de fichiers
  dont la limite par fichier est inférieure au plus gros fichier que
  l'opération écrirait — typiquement FAT32, limité à 4 Gio moins un octet.
  Une archive tar d'export compte pour un seul fichier. Le même code est
  émis quand la condition survient en cours d'écriture plutôt qu'au
  pré-vol : support échangé entre les deux, ou système de fichiers que ce
  build n'a pas su identifier.
- **Action corrective :** reformatez le support avec un système de fichiers
  sans cette limite (exFAT, NTFS, ext4, XFS), ou découpez le transfert pour
  qu'aucun fichier ne dépasse la limite.
- **Corrigeable hors ligne :** oui · **Bloque :** la synchronisation ou l'export concerné ; en cours d'écriture, le store reste intact

### TBY-STO-006

- **Ce qui s'est passé :** le store n'a pas été réinitialisé.
- **Cause probable :** la confirmation saisie ne correspond pas. Une remise
  à zéro demande le mot `RESET`, en majuscules et rien d'autre — une
  commande aussi destructrice n'est pas de celles qu'on valide par
  inadvertance (FR-046).
- **Action corrective :** saisissez `RESET` dans le champ de confirmation
  et validez de nouveau. La remise à zéro supprime tous les artefacts du
  store ; l'historique des opérations, les journaux de tâches et le journal
  d'audit sont **conservés**, parce qu'une trace qu'une action destructrice
  efface n'est pas une trace.
- **Corrigeable hors ligne :** oui · **Bloque :** rien — le store est intact

## Transport sur support amovible (TBY-MED)

Le support est un store qui a changé de mains : tout ce qu'il dit de
lui-même reste une allégation tant que ce côté-ci n'a pas recalculé les
empreintes. Quatre conditions bloquent le support entier ; tout le reste se
décide livraison par livraison, si bien qu'un support partiellement abîmé
remet quand même ses recipes intactes.

### TBY-MED-001

- **Quoi :** le support ne porte aucun manifeste de média.
- **Cause probable :** `meta/media.json` est absent — le store n'a pas été
  produit par une synchronisation miroir menée à son terme, ou la copie sur
  le support est partielle.
- **Action corrective :** recopiez le store depuis l'instance source, ou
  relancez la synchronisation miroir qui le produit.
- **Corrigeable hors ligne :** oui · **Bloque :** le support entier, sans dérogation (classe vérification, code 4)

### TBY-MED-002

- **Quoi :** le manifeste de média est illisible.
- **Cause probable :** il est tronqué, non analysable ou incohérent — un
  chemin qui sort du store, une entrée d'inventaire en double, un nom de
  dépôt qui n'en est pas un.
- **Action corrective :** recopiez le store depuis l'instance source puis
  vérifiez à nouveau.
- **Corrigeable hors ligne :** oui · **Bloque :** le support entier, sans dérogation (classe vérification, code 4)

### TBY-MED-003

- **Quoi :** le manifeste de média utilise une version de format non prise
  en charge.
- **Cause probable :** le support déclare un format de manifeste que cette
  version de Tobby ne lit pas ; les deux versions sont nommées.
- **Action corrective :** utilisez de ce côté une version de Tobby
  correspondant au support, ou reproduisez le support avec la version d'ici.
- **Corrigeable hors ligne :** oui · **Bloque :** le support entier (classe vérification, code 4)

### TBY-MED-004

- **Quoi :** le support utilise une version de format de store non prise en
  charge.
- **Cause probable :** la disposition du store est d'une autre série
  majeure ; les deux versions sont nommées.
- **Action corrective :** utilisez une version de Tobby correspondante, ou
  transférez le contenu via l'OCI image layout avec l'outillage standard.
- **Corrigeable hors ligne :** oui · **Bloque :** le support entier (classe vérification, code 4)

### TBY-MED-005

- **Quoi :** le graphe de recipes du support ne correspond pas à son
  inventaire.
- **Cause probable :** `meta/recipes.json` a été modifié après l'écriture du
  manifeste — la liste de ce que le support livre a changé.
- **Action corrective :** recopiez le store depuis l'instance source. Le
  graphe est ce à partir de quoi chaque verdict par recipe est calculé.
- **Corrigeable hors ligne :** oui · **Bloque :** le support entier, sans dérogation (classe vérification, code 4)

### TBY-MED-006

- **Quoi :** le support est adressé à une autre zone.
- **Cause probable :** le manifeste nomme une zone que cette instance ne
  sert pas ; les deux sont nommées.
- **Action corrective :** vérifiez qu'il s'agit bien du support destiné à
  cette zone. Un administrateur peut lever le refus ; la dérogation est
  consignée au journal d'audit.
- **Corrigeable hors ligne :** oui · **Bloque :** le support entier, dérogation admin possible (classe politique, code 3)

### TBY-MED-007

- **Quoi :** le support est plus ancien que le dernier importé pour cette
  zone.
- **Cause probable :** son horodatage de résolution précède celui du dernier
  import abouti de la zone ; les deux horodatages et le support sont nommés.
  Garde-fou anti-accident, pas contrôle de sécurité : le manifeste n'est pas
  signé.
- **Action corrective :** vérifiez que vous avez branché le support courant.
  Un administrateur peut lever le refus — pour restaurer volontairement une
  livraison antérieure, par exemple ; la dérogation est auditée.
- **Corrigeable hors ligne :** oui · **Bloque :** le support entier, dérogation admin possible (classe politique, code 3)

### TBY-MED-010

- **Quoi :** un fichier nécessaire à la recipe est absent du support.
- **Cause probable :** copie partielle, ou fichier supprimé.
- **Action corrective :** recopiez le store depuis l'instance source.
- **Corrigeable hors ligne :** oui · **Bloque :** cette recipe entière, sans dérogation (classe vérification, code 4)

### TBY-MED-011

- **Quoi :** un fichier du support n'a pas la taille attendue.
- **Cause probable :** il a été tronqué ou modifié après l'écriture du
  manifeste ; les tailles attendue et constatée sont nommées.
- **Action corrective :** recopiez le store depuis l'instance source.
- **Corrigeable hors ligne :** oui · **Bloque :** la recipe qui atteint ce fichier, sans dérogation (classe vérification, code 4)

### TBY-MED-012

- **Quoi :** un fichier du support ne correspond pas à son empreinte
  enregistrée.
- **Cause probable :** le contenu a été corrompu ou altéré pendant le
  transport ; les deux empreintes sont nommées.
- **Action corrective :** recopiez le store depuis l'instance source.
- **Corrigeable hors ligne :** oui · **Bloque :** la recipe qui atteint ce fichier, sans dérogation (classe vérification, code 4)

### TBY-MED-013

- **Quoi :** un fichier nécessaire à la recipe est absent de l'inventaire.
- **Cause probable :** le manifeste ne couvre pas tout ce que les recipes
  atteignent ; il ne peut donc pas en répondre.
- **Action corrective :** reproduisez le support depuis l'instance source.
- **Corrigeable hors ligne :** oui · **Bloque :** cette recipe entière, sans dérogation (classe vérification, code 4)

### TBY-MED-014

- **Quoi :** un manifeste du support est illisible.
- **Cause probable :** le fichier nommé n'est pas lisible comme manifeste ou
  index OCI ; ce que la recipe livre ne peut pas être établi.
- **Action corrective :** recopiez le store depuis l'instance source.
- **Corrigeable hors ligne :** oui · **Bloque :** cette recipe entière (classe vérification, code 4)

### TBY-MED-015

- **Quoi :** un blob du support est rangé sous une mauvaise empreinte.
- **Cause probable :** ses octets n'ont pas l'empreinte que son propre
  chemin annonce — le stockage adressé par contenu se contredit, quoi que
  dise l'inventaire.
- **Action corrective :** recopiez le store depuis l'instance source.
- **Corrigeable hors ligne :** oui · **Bloque :** la recipe qui atteint ce blob, sans dérogation (classe vérification, code 4)

### TBY-MED-020

- **Quoi :** un fichier du support n'est pas couvert par l'inventaire.
- **Cause probable :** il a été ajouté après l'écriture du manifeste.
- **Action corrective :** rien à faire pour continuer — le contenu
  surnuméraire n'est jamais poussé. Cherchez comment il est arrivé là si ce
  n'est pas vous qui l'y avez mis.
- **Corrigeable hors ligne :** oui · **Bloque :** rien (signalé seulement)

### TBY-MED-021

- **Quoi :** un fichier du support n'est atteint par aucune recipe.
- **Cause probable :** le plus souvent, un reliquat d'une livraison
  antérieure.
- **Action corrective :** rien à faire pour continuer — un contenu atteint
  par aucune recipe n'est jamais poussé. Élaguez le store source pour que le
  support porte moins.
- **Corrigeable hors ligne :** oui · **Bloque :** rien (signalé seulement)

### TBY-MED-022

- **Quoi :** un fichier de tenue de registre du support ne correspond pas à
  son empreinte enregistrée.
- **Cause probable :** la comptabilité interne du store — hors graphe de
  recipes, qui bloque globalement — a été modifiée après l'écriture du
  manifeste.
- **Action corrective :** recopiez le store depuis l'instance source si vous
  ne l'avez pas modifié délibérément. Rien n'est poussé depuis ces fichiers.
- **Corrigeable hors ligne :** oui · **Bloque :** rien (signalé seulement)

### TBY-MED-030

- **Quoi :** le support n'a pas encore été vérifié : cette instance n'en sert
  aucun contenu.
- **Cause probable :** le store sur lequel cette instance a été pointée est
  arrivé d'une autre zone sur un support physique, et rien n'en a encore
  recalculé les empreintes ni vérifié les signatures de ce qu'il livre.
  FR-054 exige que la vérification précède toute poussée, tout service et
  toute écriture locale : `/v2/` et `/files/` restent donc fermés tant
  qu'elle n'a pas eu lieu. L'instance, elle, est vivante, prête, et sert
  normalement son interface et son API.
- **Action corrective :** ouvrez l'écran **Média** et lancez *Vérifier* — sur
  un disque plein cela prend plusieurs minutes — ou appelez
  `POST /api/v1/media/verify` sur cette instance. Les surfaces de contenu
  s'ouvrent d'elles-mêmes dès que le support est validé. `tobby media verify`
  rend le même verdict mais s'exécute dans son propre processus sur le
  répertoire : il **n'ouvre pas** les surfaces d'une instance en cours
  d'exécution — le verrou est levé par une vérification que l'instance
  elle-même effectue. Aucun réglage ne permet délibérément de servir un
  support sans le vérifier.
- **Corrigeable hors ligne :** oui · **Bloque :** la registry embarquée et la
  surface de fichiers, pour ce support

### TBY-MED-031

- **Quoi :** une vérification de ce support est déjà en cours.
- **Cause probable :** une seconde vérification a été demandée alors qu'une
  première parcourait le support. Deux parcours du même disque se ralentissent
  mutuellement sans rien apprendre de plus.
- **Action corrective :** attendez la fin de l'exécution en cours. Son verdict
  s'affiche sur l'écran Média et sur `GET /api/v1/media/verification`.
- **Corrigeable hors ligne :** oui · **Bloque :** la seconde vérification
  seulement

### TBY-MED-032

- **Quoi :** le support a été vérifié et n'en est pas ressorti intact : cette
  instance n'en sert aucun contenu.
- **Cause probable :** le verdict est *partiel* ou *bloqué* : au moins une
  livraison a échoué sur sa signature ou sur l'une des empreintes de ses
  ingrédients. Contrairement à la décision de poussée, que R-19 prend recipe
  par recipe, servir engage le store entier — `/v2/` et `/files/` distribuent
  des blobs, et un blob atteint par une livraison bloquée est exactement le
  contenu qui a échoué.
- **Action corrective :** lisez le rapport sur l'écran Média : il nomme chaque
  livraison bloquée et le fichier fautif. Recopiez le support depuis
  l'instance source et vérifiez de nouveau. Les livraisons intactes restent
  poussables vers la registry de zone, qui les sert ensuite.
- **Corrigeable hors ligne :** oui · **Bloque :** la registry embarquée et la
  surface de fichiers, pour ce support

## Export et import OCI image layout (TBY-LAY)

La sortie d'interopérabilité (FR-051) : le store écrit dans le format
standard que `skopeo`, `oras` et `crane` lisent, et relu ensuite. Un layout
importé vient de l'extérieur : il est traité comme n'importe quelle entrée
étrangère.

### TBY-LAY-001

- **Ce qui s'est passé :** ce n'est pas un OCI image layout exploitable.
- **Cause probable :** le chemin n'a pas pu être lu comme tel — pas de
  marqueur `oci-layout`, un `index.json` qui ne s'analyse pas, ou un blob
  dont l'empreinte ne correspond pas au digest qui l'adresse.
- **Action corrective :** vérifiez que le chemin désigne le layout
  lui-même : le répertoire, ou son tar **non compressé**, contenant
  `oci-layout`, `index.json` et `blobs/`. Une archive compressée doit
  d'abord être décompressée — Tobby lit une archive non compressée en se
  positionnant sur un décalage de blob enregistré, ce qui ne laisse rien à
  déployer à une bombe de décompression. `skopeo copy oci:<chemin>:<tag> …`
  sur le même chemin vous dira si un outil OCI y arrive davantage.
- **Corrigeable hors ligne :** oui · **Bloque :** l'import concerné ; rien n'est écrit

### TBY-LAY-002

- **Ce qui s'est passé :** l'archive a été refusée — une de ses entrées n'a
  rien à faire dans un image layout.
- **Cause probable :** l'entrée nommée est un chemin absolu, un chemin qui
  sort de l'archive, ou un lien. Une archive de layout contient
  `oci-layout`, `index.json` et des fichiers
  `blobs/<algorithme>/<digest>`, et rien d'autre : une archive qui porte
  une telle entrée n'a pas été produite par un outil OCI.
- **Action corrective :** rien n'a été écrit. Faites refabriquer le support
  à sa source, et signalez-le comme incident s'il provient de l'extérieur
  de votre organisation. C'est un échec de vérification, pas un transfert
  abîmé.
- **Corrigeable hors ligne :** oui · **Bloque :** l'import concerné ; rien n'est écrit (classe vérification, code 4)

### TBY-LAY-003

- **Ce qui s'est passé :** la destination de l'export existe déjà.
- **Cause probable :** le chemin est déjà présent et cet export n'était pas
  autorisé à le remplacer. Un export écrit dans un chemin de préparation
  puis le renomme en place : il n'écrase donc jamais à moitié.
- **Action corrective :** choisissez une autre destination, ou relancez
  avec l'option de remplacement (`--overwrite`) une fois certain que ce qui
  s'y trouve peut être perdu.
- **Corrigeable hors ligne :** oui · **Bloque :** l'export concerné ; rien n'est écrit

## Empaquetage d'ensembles de fichiers (TBY-FIL)

`tobby fileset pack` transforme un répertoire local en image OCI FileSet et
l'importe par le chemin d'import unitaire (FR-048). Ce que
[RECIPE-SPEC §14.5](https://tobby-fetch.github.io/recipe-spec/#145-extraction-safety)
refuse à l'extraction, l'empaquetage le refuse **d'abord** — là où
l'exploitant peut encore corriger son arbre. Un ensemble qui contiendrait
discrètement moins de fichiers que le répertoire dont il sort serait pire
qu'un refus qui nomme l'entrée fautive.

### TBY-FIL-001

- **Ce qui s'est passé :** l'ensemble de fichiers n'a pas pu être empaqueté.
- **Cause probable :** énoncée telle quelle — le plus souvent un répertoire
  inexistant, vide ou illisible, ou un nom et une version qui ne s'analysent
  pas.
- **Action corrective :** indiquez un répertoire existant et non vide, et
  donnez à l'ensemble un nom en minuscules et une version, par exemple
  `tobby fileset pack ./repo site-docs:1.0.0`. Rien n'a été écrit dans le
  store.
- **Corrigeable hors ligne :** oui · **Bloque :** l'empaquetage concerné ; rien n'est écrit

### TBY-FIL-002

- **Ce qui s'est passé :** le répertoire contient une entrée qui ne peut pas
  être empaquetée sans risque.
- **Cause probable :** l'entrée nommée est un lien symbolique pointant hors
  du répertoire, un fichier spécial (périphérique, FIFO, socket), un fichier
  setuid ou setgid, un nom commençant par `.wh.` — qu'un lecteur de couche
  prendrait pour une suppression — ou un nom portant un antislash ou un
  octet NUL.
- **Action corrective :** supprimez ou remplacez cette entrée puis
  empaquetez à nouveau. Un ensemble de fichiers est extrait et servi sur
  d'autres machines, Windows compris : les refus portent sur l'endroit où
  le contenu atterrit, pas sur cet hôte. Rien n'a été écrit dans le store.
- **Corrigeable hors ligne :** oui · **Bloque :** l'empaquetage concerné ; rien n'est écrit

### TBY-FIL-003

- **Ce qui s'est passé :** l'empaquetage de ce répertoire n'est pas autorisé
  depuis cette surface.
- **Cause probable :** le chemin est en dehors des répertoires que
  `files.packRoots` autorise l'interface web et l'API à lire. Sans entrée
  configurée, elles n'en lisent **aucun** — lire un répertoire arbitraire de
  l'hôte sur requête réseau est une capacité qu'on donne à une instance, pas
  qu'elle possède.
- **Action corrective :** lancez `tobby fileset pack` sur l'hôte de
  l'instance, qui n'est pas restreint parce que celui qui l'exécute détient
  déjà ces droits sur le système de fichiers, ou ajoutez le répertoire à
  `files.packRoots` dans le fichier de configuration puis redémarrez
  l'instance.
- **Corrigeable hors ligne :** oui · **Bloque :** l'empaquetage concerné ; rien n'est écrit

## Tâches (TBY-TSK)

### TBY-TSK-001

- **Quoi :** la tâche n'existe pas.
- **Cause probable :** aucune tâche d'identifiant *\<id\>* sur cette
  instance — lien erroné, ou store réinitialisé.
- **Action corrective :** ouvrez la liste des tâches et suivez le lien
  d'une tâche existante.
- **Remédiable hors-ligne :** oui · **Bloque :** la requête seulement

## Instance (TBY-SRV)

### TBY-SRV-001

- **Quoi :** une erreur interne s'est produite.
- **Cause probable :** une condition inattendue a interrompu la requête ;
  le détail est dans les journaux de l'instance.
- **Action corrective :** réessayez ; si cela persiste, cherchez dans les
  journaux l'identifiant de corrélation affiché avec cette erreur (voir
  [Métriques et logs](../../reference/metrics-logs/)).
- **Remédiable hors-ligne :** oui (le diagnostic est local) · **Bloque :** la requête seulement

### TBY-SRV-002

- **Quoi :** cette ressource n'existe pas.
- **Cause probable :** l'adresse est erronée, ou le contenu a été
  supprimé.
- **Action corrective :** revenez au navigateur de contenu ou utilisez la
  recherche.
- **Remédiable hors-ligne :** oui · **Bloque :** la requête seulement

### TBY-SRV-003

- **Quoi :** l'instance est injoignable. Une condition côté client, rendue
  par la coquille de l'interface sur échec de transport — jamais servie par
  l'instance elle-même ; cataloguée pour que ce guide la documente.
- **Cause probable :** la connexion réseau est tombée, ou l'instance
  redémarre.
- **Action corrective :** vérifiez votre liaison réseau et réessayez ; la
  page reprend dès que l'instance répond.
- **Remédiable hors-ligne :** oui · **Bloque :** l'affichage de la session de navigation seulement
