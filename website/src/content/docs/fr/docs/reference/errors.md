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
[code de sortie CLI](../../reference/cli/#exit-codes).

Cette page est produite depuis le catalogue de la taxonomie embarqué dans
le binaire (`internal/taxonomy`). Chaque code a son propre titre : l'ancre
`#tby-reg-003` est stable — ce sont les mêmes ancres que résoudra le guide
de dépannage embarqué (`/help#TBY-REG-003`).

:::note[À venir — jalon 5]
Le guide `/help` embarqué (R-05) et les codes d'erreur du parcours média
(export, pré-vol, verdicts d'import) arrivent au jalon 5, sur ces mêmes
ancres. À suivre sur la page
[État du projet](../../discover/status/).
:::

**Comment lire chaque entrée :**

- **Remédiable hors-ligne** — si un opérateur en zone isolée peut résoudre
  la condition avec ses seuls moyens locaux (configuration, disque,
  comptes), ou si la correction exige d'atteindre un registre source ou la
  chaîne de qualification amont.
- **Bloque** — la portée : toute l'instance (refus de démarrage), une tâche
  ou une recipe, ou seulement la requête en cours.
- **Dérogation** — aucun code n'a de dérogation à chaud aujourd'hui. Les
  refus de politique se lèvent en modifiant la configuration auditée qui
  les impose (liste blanche, clés de confiance, comptes) ; les échecs de
  vérification ne se dérogent jamais. L'unique dérogation à chaud —
  réservée à l'admin, auditée, à l'import d'un média — arrive au jalon 5.

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
