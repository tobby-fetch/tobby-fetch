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

Cette page est générée depuis le catalogue de la taxonomie embarqué dans
le binaire (`internal/taxonomy`) : ce qui s'est passé, la cause probable et
l'action corrective sont, mot pour mot, ce que l'instance affiche. Chaque
code a son propre titre : l'ancre `#tby-reg-003` est stable — ce sont les
mêmes ancres que résout le guide de dépannage embarqué
(`/help#TBY-REG-003`).

Un contrôle régénère la page et échoue dès qu'elle s'écarte du catalogue,
dans l'une ou l'autre langue — le catalogue est la source de vérité, cette
page en est le rendu publié.

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
(`internal/taxonomy`), et un test échoue dès que les deux divergent —
dans les deux sens : un code que la table n'énumère pas, ou une ligne que
rien ne peut produire.

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

La [référence CLI](../../reference/cli/#codes-de-sortie) reprend cette table
avec chaque commande, le contrat `--output json` et le mode non interactif.

## Index par domaine

| Domaine | Codes |
|---|---|
| Authentification et comptes (`TBY-AUTH`) | [`TBY-AUTH-001`](#tby-auth-001) … [`TBY-AUTH-019`](#tby-auth-019) (19) |
| Configuration (`TBY-CFG`) | [`TBY-CFG-001`](#tby-cfg-001) … [`TBY-CFG-002`](#tby-cfg-002) (2) |
| Réseau sortant et TLS (`TBY-NET`) | [`TBY-NET-001`](#tby-net-001) … [`TBY-NET-004`](#tby-net-004) (4) |
| Validation des recipes et retrievers (`TBY-VAL`) | [`TBY-VAL-001`](#tby-val-001) (1) |
| Accès aux registres sources (`TBY-REG`) | [`TBY-REG-001`](#tby-reg-001) … [`TBY-REG-008`](#tby-reg-008) (8) |
| Refus de politique (`TBY-POL`) | [`TBY-POL-001`](#tby-pol-001) … [`TBY-POL-006`](#tby-pol-006) (6) |
| Vérification des signatures et digests (`TBY-SIG`) | [`TBY-SIG-001`](#tby-sig-001) … [`TBY-SIG-003`](#tby-sig-003) (3) |
| Analyse de vulnérabilités (`TBY-SCN`) | [`TBY-SCN-001`](#tby-scn-001) … [`TBY-SCN-028`](#tby-scn-028) (17) |
| Limites de la destination (`TBY-DST`) | [`TBY-DST-001`](#tby-dst-001) (1) |
| Charts Helm (`TBY-CHT`) | [`TBY-CHT-001`](#tby-cht-001) (1) |
| Store local et état (`TBY-STO`) | [`TBY-STO-001`](#tby-sto-001) … [`TBY-STO-006`](#tby-sto-006) (6) |
| Transport sur support amovible (`TBY-MED`) | [`TBY-MED-001`](#tby-med-001) … [`TBY-MED-034`](#tby-med-034) (23) |
| Intégrité du store (`TBY-INT`) | [`TBY-INT-001`](#tby-int-001) … [`TBY-INT-006`](#tby-int-006) (6) |
| Export et import OCI image layout (`TBY-LAY`) | [`TBY-LAY-001`](#tby-lay-001) … [`TBY-LAY-003`](#tby-lay-003) (3) |
| Empaquetage d'ensembles de fichiers (`TBY-FIL`) | [`TBY-FIL-001`](#tby-fil-001) … [`TBY-FIL-003`](#tby-fil-003) (3) |
| Aides à la consommation (`TBY-AID`) | [`TBY-AID-001`](#tby-aid-001) (1) |
| Bundle de diagnostic (`TBY-DIA`) | [`TBY-DIA-001`](#tby-dia-001) … [`TBY-DIA-003`](#tby-dia-003) (3) |
| Cohérence d'horloge (`TBY-CLK`) | [`TBY-CLK-001`](#tby-clk-001) … [`TBY-CLK-002`](#tby-clk-002) (2) |
| Répertoire d'état de l'instance (`TBY-STA`) | [`TBY-STA-001`](#tby-sta-001) … [`TBY-STA-003`](#tby-sta-003) (3) |
| Tâches (`TBY-TSK`) | [`TBY-TSK-001`](#tby-tsk-001) (1) |
| Instance (`TBY-SRV`) | [`TBY-SRV-001`](#tby-srv-001) … [`TBY-SRV-005`](#tby-srv-005) (5) |

## Authentification et comptes (TBY-AUTH)

### TBY-AUTH-001

- **Ce qui s'est passé :** L'instance refuse de démarrer : aucun compte local n'est configuré.
- **Cause probable :** Aucun compte administrateur n'a encore été créé sur cette instance, ou son répertoire d'état a été réinitialisé.
- **Action corrective :** Sur l'hôte de l'instance, exécutez `tobby user add --role admin <nom>` (l'outil calcule le hachage du mot de passe), puis redémarrez l'instance. Tobby ne démarre jamais avec une interface ouverte.
- **Classe :** refus de politique (sortie 3)
- **Remédiable hors-ligne :** oui · **Bloque :** toute l'instance (refus de démarrage sécurisé par défaut)

### TBY-AUTH-002

- **Ce qui s'est passé :** L'authentification a échoué.
- **Cause probable :** Les identifiants sont inconnus ou le mot de passe est erroné.
- **Action corrective :** Vérifiez le nom du compte et le mot de passe, puis réessayez. Les comptes se gèrent sur l'hôte avec `tobby user`.
- **Note :** Le message est volontairement sans paramètre : il ne révèle jamais si le compte existe.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 401
- **Remédiable hors-ligne :** oui · **Bloque :** la tentative de connexion seulement

### TBY-AUTH-003

- **Ce qui s'est passé :** Cette action n'est pas autorisée pour votre rôle.
- **Cause probable :** Elle requiert le rôle *\<role\>*.
- **Action corrective :** Connectez-vous avec un compte disposant du rôle *\<role\>*, ou demandez à un administrateur de vous l'accorder.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 403
- **Remédiable hors-ligne :** oui · **Bloque :** l'action, pour cette session

### TBY-AUTH-004

- **Ce qui s'est passé :** Le formulaire n'a pas pu être soumis en toute sécurité.
- **Cause probable :** Le jeton anti-falsification est absent, erroné ou expiré, ou la requête vient d'une page d'un autre site — la page est probablement restée ouverte longtemps.
- **Action corrective :** Rechargez la page et soumettez à nouveau. Un script ou un outil qui appelle l'API s'authentifie par un jeton (Authorization: Bearer), pas par la session du navigateur.
- **Note :** Toute requête qui modifie l'état et que la session du navigateur authentifie porte le jeton, formulaires de l'interface comme appels d'API (en-tête `X-CSRF-Token` sur l'API), et elle est refusée quand le navigateur indique qu'elle vient d'une autre origine.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 403
- **Remédiable hors-ligne :** oui · **Bloque :** la requête soumise seulement

### TBY-AUTH-005

- **Ce qui s'est passé :** Votre session a expiré.
- **Cause probable :** Aucune activité pendant plus que le délai d'inactivité, la session a atteint sa durée de vie maximale, ou l'instance a redémarré.
- **Action corrective :** Reconnectez-vous ; vous reviendrez à la page où vous étiez.
- **Note :** Le délai d'inactivité est `auth.session.idleTimeout` (30 min par défaut) et la durée de vie absolue `auth.sessionTTL` (12 h par défaut). Les sessions vivent en mémoire, d'où le cas du redémarrage.
- **Note :** Chaque expiration est consignée par un événement d'audit `session.expire` avec sa raison, `idle` ou `lifetime`.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 401
- **Remédiable hors-ligne :** oui · **Bloque :** la session seulement

### TBY-AUTH-006

- **Ce qui s'est passé :** Le mot de passe actuel est erroné.
- **Cause probable :** Le mot de passe actuel envoyé avec la demande de changement ne correspond pas à celui du compte.
- **Action corrective :** Saisissez à nouveau votre mot de passe actuel, puis réessayez. S'il est perdu, un administrateur peut en définir un nouveau sur l'hôte de l'instance avec `tobby user passwd <nom>`.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** le changement de mot de passe seulement

### TBY-AUTH-007

- **Ce qui s'est passé :** Le nouveau mot de passe a été refusé.
- **Cause probable :** Il est vide, identique au mot de passe actuel, ou sa confirmation ne correspond pas.
- **Action corrective :** Choisissez un mot de passe non vide et différent de l'actuel, saisissez la même valeur dans les deux champs du nouveau mot de passe, puis soumettez à nouveau.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** le changement de mot de passe seulement

### TBY-AUTH-008

- **Ce qui s'est passé :** Le compte n'a pas pu être créé ou modifié.
- **Cause probable :** Le login est vide, le rôle n'est pas l'un des trois (viewer, operator, admin), ou le mot de passe est vide ou mal ressaisi dans le champ de confirmation.
- **Action corrective :** Indiquez un login non vide, choisissez l'un des trois rôles, et saisissez le même mot de passe non vide dans les deux champs, puis soumettez à nouveau.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** l'opération sur le compte seulement

### TBY-AUTH-009

- **Ce qui s'est passé :** Le compte *\<name\>* existe déjà.
- **Cause probable :** Cette instance possède déjà un compte local portant ce login ; les logins sont uniques.
- **Action corrective :** Choisissez un autre login, ou gérez le compte existant depuis l'écran des comptes — son rôle se change et son mot de passe se réinitialise sans en créer un second.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 409
- **Remédiable hors-ligne :** oui · **Bloque :** la création du compte seulement

### TBY-AUTH-010

- **Ce qui s'est passé :** Aucun compte nommé *\<name\>* sur cette instance.
- **Cause probable :** Le compte a été supprimé, ou le login est mal orthographié — l'écran depuis lequel vous avez agi est peut-être antérieur à la suppression.
- **Action corrective :** Rechargez l'écran des comptes pour voir la liste à jour, puis réessayez sur un compte qui y figure.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 404
- **Remédiable hors-ligne :** oui · **Bloque :** l'opération sur le compte seulement

### TBY-AUTH-011

- **Ce qui s'est passé :** Refusé : *\<name\>* est le dernier administrateur de cette instance.
- **Cause probable :** Le supprimer, ou lui retirer le rôle admin, ne laisserait plus personne pour administrer l'instance — et une instance sans aucun compte refuse purement et simplement de démarrer.
- **Action corrective :** Créez d'abord un second compte admin, puis réessayez. Sur l'hôte de l'instance, `tobby user add --role admin <nom>` donne le même résultat.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 409
- **Remédiable hors-ligne :** oui · **Bloque :** la suppression ou la rétrogradation seulement

### TBY-AUTH-012

- **Ce qui s'est passé :** Trop de tentatives d'authentification échouées depuis votre adresse réseau ; les tentatives suivantes sont temporairement refusées.
- **Cause probable :** Des identifiants erronés ont été présentés à répétition depuis la même origine. Chaque vérification échouée est volontairement coûteuse, l'instance limite donc les origines qui échouent en boucle plutôt que de brûler CPU et mémoire pour elles.
- **Action corrective :** Patientez un instant, vérifiez l'identifiant (mot de passe du compte ou secret du jeton), puis réessayez. Si cette adresse est une sortie partagée, un autre client derrière elle est peut-être mal configuré — le journal d'audit liste les tentatives échouées et les noms de compte revendiqués.
- **Note :** Le contrôle coûteux est un calcul argon2id.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 429
- **Remédiable hors-ligne :** oui · **Bloque :** l'origine réseau, temporairement

### TBY-AUTH-013

- **Ce qui s'est passé :** Le jeton d'API a expiré.
- **Cause probable :** Le jeton a été créé avec une date d'expiration, et cette date est passée. Un jeton expiré est refusé comme un jeton révoqué, et le refus est journalisé dans l'audit.
- **Action corrective :** Demandez un nouveau jeton à un administrateur (écran des comptes ou `POST /api/v1/tokens`), puis remplacez le jeton expiré dans le client qui l'a présenté.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 401
- **Remédiable hors-ligne :** oui · **Bloque :** la requête ; le jeton reste refusé

### TBY-AUTH-014

- **Ce qui s'est passé :** La connexion via *\<provider\>* a échoué.
- **Cause probable :** La réponse du fournisseur d'identité n'a pas passé les contrôles de Tobby (state, nonce, signature, émetteur ou audience) — souvent une connexion commencée dans un autre onglet, un écart d'horloge, ou un client enregistré avec une autre URL de retour.
- **Action corrective :** Recommencez la connexion depuis cette page. Si elle échoue encore, un administrateur trouve le contrôle en échec dans le journal de l'instance sous le même identifiant de corrélation ; la connexion locale reste disponible entre-temps.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 401
- **Remédiable hors-ligne :** oui · **Bloque :** la tentative de connexion

### TBY-AUTH-015

- **Ce qui s'est passé :** La connexion via *\<provider\>* a échoué.
- **Cause probable :** La réponse SAML a été refusée : non signée, déjà utilisée, ne répondant pas à une connexion lancée par cette instance, hors de sa fenêtre de validité, ou destinée à une autre audience ou destination.
- **Action corrective :** Recommencez la connexion depuis cette page — Tobby n'accepte que les connexions qu'il a lui-même lancées. Si elle échoue encore, un administrateur trouve le contrôle refusé dans le journal de l'instance sous le même identifiant de corrélation ; la connexion locale reste disponible entre-temps.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 401
- **Remédiable hors-ligne :** oui · **Bloque :** la tentative de connexion

### TBY-AUTH-016

- **Ce qui s'est passé :** La connexion via *\<provider\>* est indisponible : le fournisseur d'identité est injoignable.
- **Cause probable :** Le fournisseur n'a pas répondu à temps, ou le chemin réseau vers lui est coupé. Les sessions en cours et les jetons d'API ne sont pas affectés.
- **Action corrective :** Utilisez la connexion locale entre-temps — elle reste disponible quand la fédération est en panne, et son usage est consigné dans le journal d'audit. Vérifiez le chemin réseau vers le fournisseur (proxy, AC privée) et sa disponibilité ; la connexion fédérée revient sans redémarrage.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 502
- **Remédiable hors-ligne :** selon : la connexion locale fonctionne hors-ligne ; la connexion fédérée a besoin du fournisseur · **Bloque :** la connexion fédérée seulement

### TBY-AUTH-017

- **Ce qui s'est passé :** Vous êtes authentifié chez *\<provider\>*, mais aucun rôle Tobby ne vous est attribué.
- **Cause probable :** Aucun des groupes transmis par le fournisseur d'identité pour votre compte n'est associé à un rôle sur cette instance. Tobby n'attribue jamais de rôle par défaut.
- **Action corrective :** Demandez à un administrateur d'ajouter l'un de vos groupes à la correspondance des rôles de la configuration (`roles.viewer`, `roles.operator` ou `roles.admin` dans la section du fournisseur), ou de vous ajouter à un groupe déjà associé.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 403
- **Remédiable hors-ligne :** oui · **Bloque :** la tentative de connexion

### TBY-AUTH-018

- **Ce qui s'est passé :** Trop de connexions par *\<provider\>* ont été entamées depuis votre adresse réseau ; en entamer une autre est temporairement refusé.
- **Cause probable :** Chaque connexion entamée retient un peu de mémoire sur cette instance jusqu'à son terme, et peut contacter le fournisseur d'identité, avant que quiconque soit authentifié. L'instance borne la fréquence à laquelle une même adresse réseau peut en entamer, pour qu'un afflux depuis une adresse ne chasse jamais les connexions que d'autres personnes ont en cours.
- **Action corrective :** Patientez quelques secondes puis reconnectez-vous — une fois. S'il s'agit d'une adresse de sortie partagée, un autre client derrière elle boucle peut-être sur la page de connexion.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 429
- **Remédiable hors-ligne :** oui · **Bloque :** l'ouverture d'une connexion fédérée depuis cette adresse, quelques secondes ; les comptes locaux et les tokens ne sont pas touchés

### TBY-AUTH-019

- **Ce qui s'est passé :** Le nom *\<name\>* ne peut pas désigner un compte ou un token local.
- **Cause probable :** Le caractère « : » est réservé aux identités fédérées, dont la clé est `oidc:<sujet>` ou `saml:<NameID>` ; un compte ou un token local nommé comme l'une d'elles s'en distinguerait mal dans la piste d'audit.
- **Action corrective :** Choisissez un nom sans « : » (par exemple un tiret ou un point à la place).
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** la création de ce compte ou de ce token

## Configuration (TBY-CFG)

### TBY-CFG-001

- **Ce qui s'est passé :** La configuration est invalide.
- **Cause probable :** *\<detail\>*
- **Action corrective :** Corrigez le réglage indiqué (précédence : drapeaux, puis variables d'environnement TOBBY_*, puis fichier YAML), vérifiez le résultat avec `tobby config dump`, puis redémarrez.
- **Voir aussi :** Voir la [référence de configuration](../../reference/configuration/).
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** toute l'instance (refus de démarrage) ou la commande qui a chargé la configuration

### TBY-CFG-002

- **Ce qui s'est passé :** L'instance refuse de démarrer : un fichier de secret est configuré à l'intérieur du store transportable.
- **Cause probable :** *\<paths\>* se résout sous la racine du store *\<root\>*. Le store est confié à un porteur puis branché sur une machine d'une autre zone : tout ce qui est dessous est réputé lu par quelqu'un d'autre — identifiants de registre, clés privées TLS, base d'utilisateurs locale et jetons statiques ne doivent jamais voyager dessus.
- **Action corrective :** Déplacez chaque fichier listé dans un répertoire hors du store — le répertoire d'état (`state.root`) est sa place — mettez le réglage à jour vers le nouvel emplacement, puis redémarrez l'instance. Un chemin qui se résout dans le store via un lien symbolique compte comme étant dedans : c'est le chemin résolu indiqué qui tranche.
- **Note :** Les réglages contrôlés sont `state.root`, `registries.credentialsFile` et `server.tls.keyFile`, chacun par rapport à `storage.root` (NFR-020).
- **Classe :** refus de politique (sortie 3)
- **Remédiable hors-ligne :** oui · **Bloque :** l'instance entière (refus au démarrage)

## Réseau sortant et TLS (TBY-NET)

### TBY-NET-001

- **Ce qui s'est passé :** La configuration du proxy sortant est inutilisable : l'instance refuse de démarrer.
- **Cause probable :** *\<setting\>* vaut *\<proxy\>*, qui n'est pas une URL de proxy exploitable (http:// ou https:// avec un hôte attendus).
- **Action corrective :** Corrigez *\<setting\>* sous la forme http://proxy.example.com:3128, sans identifiants dans l'URL : ceux-ci relèvent de network.proxy.username et network.proxy.password, qui n'apparaissent jamais dans les journaux ni dans `tobby config dump`. Puis redémarrez.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** toute l'instance (refus de démarrage)

### TBY-NET-002

- **Ce qui s'est passé :** Une autorité de certification configurée n'a pas pu être chargée.
- **Cause probable :** *\<source\>* est illisible, ne contient aucun bloc PEM CERTIFICATE, ou n'ajoute aucune autorité que l'instance ne connaissait déjà.
- **Action corrective :** Vérifiez que *\<source\>* existe, est lisible par l'instance et contient bien le certificat de l'autorité au format PEM (`openssl x509 -in <fichier> -noout -subject` doit afficher un sujet). Puis redémarrez. Faire confiance à une autorité privée est la voie prévue pour joindre une registry interne ; aucun réglage ne désactive la vérification des certificats.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** toute l'instance (refus de démarrage)

### TBY-NET-003

- **Ce qui s'est passé :** Le certificat de l'écouteur est inutilisable : l'instance refuse de servir.
- **Cause probable :** *\<source\>* est absent, illisible, n'est pas une paire certificat/clé au format PEM, ou la clé ne correspond pas au certificat.
- **Action corrective :** Vérifiez server.tls.certFile et server.tls.keyFile : deux fichiers PEM lisibles formant une même paire (`openssl x509 -noout -modulus` et `openssl rsa -noout -modulus` doivent concorder). Retirez les deux pour laisser Tobby générer un certificat auto-signé — son empreinte est affichée au démarrage. Puis redémarrez.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** toute l'instance (refus de démarrage)

### TBY-NET-004

- **Ce qui s'est passé :** La paire de certificat soumise a été refusée ; l'instance continue de servir le certificat qu'elle avait déjà.
- **Cause probable :** *\<detail\>*
- **Action corrective :** Soumettez un certificat PEM et la clé privée correspondante (`openssl x509 -noout -pubkey` et `openssl pkey -pubout` doivent afficher la même clé publique), encore valide, sur une instance dont server.tls.certFile et server.tls.keyFile sont configurés. Rien n'a été écrit : l'écouteur n'est pas affecté.
- **Note :** Émis pour un remplacement de certificat soumis depuis les surfaces d'administration.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** le remplacement soumis seulement — l'instance continue de servir

## Validation des recipes et retrievers (TBY-VAL)

### TBY-VAL-001

- **Ce qui s'est passé :** Le fichier recipe ou retriever est invalide.
- **Cause probable :** Dans *\<file\>*, à *\<path\>* : *\<constraint\>*.
- **Action corrective :** Corrigez le champ *\<path\>* pour satisfaire la contrainte, puis soumettez le fichier à nouveau.
- **Voir aussi :** La grammaire normative est sur le [site de la spécification](https://tobby-fetch.github.io/recipe-spec/).
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** le fichier soumis / la recipe concernée

## Accès aux registres sources (TBY-REG)

### TBY-REG-001

- **Ce qui s'est passé :** La référence n'a pas pu être interprétée.
- **Cause probable :** *\<reference\>* n'est pas une référence valide d'image ou de chart.
- **Action corrective :** Utilisez la forme registry/repository:tag ou registry/repository@sha256:… — par exemple docker.io/library/redis:8.10.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 400
- **Remédiable hors-ligne :** oui · **Bloque :** l'opération utilisant cette référence

### TBY-REG-002

- **Ce qui s'est passé :** Le registre source est injoignable.
- **Cause probable :** Aucune route réseau vers *\<host\>*, ou le registre est arrêté (DNS, proxy ou pare-feu sur le chemin).
- **Action corrective :** Vérifiez la connectivité vers *\<host\>* depuis l'hôte de l'instance et les réglages de proxy, puis réessayez.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 502
- **Remédiable hors-ligne :** non (il faut la route réseau vers la source) · **Bloque :** la tâche concernée ; réessayée avec repli borné

### TBY-REG-003

- **Ce qui s'est passé :** Le registre source a refusé l'authentification.
- **Cause probable :** Identifiants absents ou expirés pour *\<host\>*.
- **Action corrective :** Renseignez registries.credentials pour *\<host\>* dans la configuration, puis relancez l'import.
- **Note :** Les identifiants se trouvent dans le fichier désigné par `registries.credentialsFile`, une entrée par hôte de registre : y ajouter ou renouveler l'entrée de cet hôte. Voir [Secrets](../../security/secrets/).
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 502
- **Remédiable hors-ligne :** non (la correction de configuration est locale, mais le nouvel essai exige le registre source) · **Bloque :** la tâche concernée

### TBY-REG-004

- **Ce qui s'est passé :** L'inspection a dépassé le délai.
- **Cause probable :** *\<host\>* n'a pas répondu en *\<timeout\>*.
- **Action corrective :** Réessayez ; si cela persiste, vérifiez le chemin réseau vers *\<host\>* ou augmentez import.inspectTimeout dans la configuration.
- **Note :** Volontairement distinct d'« injoignable ».
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 504
- **Remédiable hors-ligne :** non (il faut la réponse de la source) · **Bloque :** l'inspection ou l'import concerné

### TBY-REG-005

- **Ce qui s'est passé :** La référence n'existe pas sur le registre source.
- **Cause probable :** *\<reference\>* est introuvable — nom ou tag erroné, ou supprimée en amont.
- **Action corrective :** Vérifiez le nom du dépôt et le tag sur le registre source, puis corrigez la référence.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 502
- **Remédiable hors-ligne :** non (la vérité est sur le registre source) · **Bloque :** la tâche concernée

### TBY-REG-006

- **Ce qui s'est passé :** Aucune version disponible ne satisfait l'expression demandée.
- **Cause probable :** Pour *\<reference\>*, l'expression *\<constraint\>* ne correspond à aucun des tags disponibles : *\<detail\>*.
- **Action corrective :** Confrontez l'expression de version aux tags réellement publiés (les contraintes semver ne considèrent que les tags interprétables en semver). Tobby ne se replie jamais silencieusement sur une autre version.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** non (la résolution exige la liste des tags de la source) · **Bloque :** la recipe ou l'ingrédient concerné

### TBY-REG-007

- **Ce qui s'est passé :** La source a renvoyé une réponse partielle inexploitable pendant la reprise d'un gros transfert.
- **Cause probable :** Pour *\<reference\>* : *\<detail\>*. Le registre source, ou un cache placé devant lui, n'honore pas les plages d'octets de manière cohérente.
- **Action corrective :** Relancez la tâche : le transfert repart de la dernière position vérifiée, ou de zéro si le contenu de la source a changé. Si cela se reproduit sur la même source, mettez transfer.resumeThreshold à 0 pour désactiver la reprise intra-blob sur cette instance, et vérifiez le proxy cache éventuel sur le chemin vers ce registre.
- **Note :** Causes typiques : un 206 démarrant au mauvais octet, un `Content-Range` contredisant le manifeste, une plage refusée, ou un contenu qui a changé entre deux essais.
- **Note :** Opérationnel, pas un verdict de vérification : rien n'a été prouvé faux sur le contenu — la conversation à son sujet s'est rompue.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 502
- **Remédiable hors-ligne :** non (côté source ; le contournement `resumeThreshold: 0` est local) · **Bloque :** la tâche concernée

### TBY-REG-008

- **Ce qui s'est passé :** L'index de la source ne porte pas une plateforme demandée par la recipe.
- **Cause probable :** Pour *\<reference\>*, aucun enfant de l'index ne correspond à *\<platforms\>* ; l'index publie *\<available\>*. Un sélecteur de plateforme s'écrit os/arch avec un variant FACULTATIF (RECIPE-SPEC §7.1) : un variant omis correspond à n'importe lequel, un variant nommé doit correspondre exactement.
- **Action corrective :** Confrontez la liste platforms de l'ingrédient à ce que la source publie réellement (docker manifest inspect, ou le rapport d'inspection d'un import unitaire) et corrigez-la. Tobby n'abandonne jamais silencieusement une plateforme demandée.
- **Note :** Les registres décrivent couramment leur enfant arm64 comme `linux/arm64` avec le variant `v8` : un sélecteur nommant un variant que la source ne publie pas ne correspond à rien.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** non (la vérité est dans l'index de la source) · **Bloque :** l'ingrédient concerné

## Refus de politique (TBY-POL)

### TBY-POL-001

- **Ce qui s'est passé :** Le registre n'est pas dans la liste blanche.
- **Cause probable :** *\<host\>* ne fait pas partie des registres autorisés en source ou en destination ; le transfert a été refusé avant tout échange.
- **Action corrective :** Si ce registre est légitime, ajoutez *\<host\>* à registries.allowlist dans la configuration ; la modification est journalisée en audit.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 403
- **Remédiable hors-ligne :** oui · **Bloque :** tout transfert touchant cet hôte, avant toute donnée ; levé par une modification de configuration auditée

### TBY-POL-002

- **Ce qui s'est passé :** Ce contenu ne peut pas être supprimé individuellement.
- **Cause probable :** *\<repository\>* est géré par *\<recipes\>* : le supprimer ici serait défait par la prochaine synchronisation.
- **Action corrective :** Supprimez plutôt la recipe qui le gère — son contenu exclusif est nettoyé avec elle. Seul le contenu importé unitairement est supprimable individuellement.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 403
- **Remédiable hors-ligne :** oui · **Bloque :** la demande de suppression seulement

### TBY-POL-003

- **Ce qui s'est passé :** Ce contenu ne peut pas être supprimé d'ici.
- **Cause probable :** *\<repository\>* a été poussé via l'API registre standard (/v2/) par un client externe : sa provenance n'est ni une recipe ni un import unitaire.
- **Action corrective :** La suppression individuelle ne couvre que le contenu importé unitairement (FR-044). Gérez le contenu poussé directement avec l'outillage registre standard qui l'a poussé.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 403
- **Remédiable hors-ligne :** oui · **Bloque :** la demande de suppression seulement

### TBY-POL-004

- **Ce qui s'est passé :** Cette version de recipe est déjà publiée, avec un contenu différent.
- **Cause probable :** *\<reference\>* pointe déjà sur *\<published\>* ; le document proposé publierait *\<candidate\>*. Une recipe cuite est immuable (RECIPE-SPEC §8).
- **Action corrective :** Publiez le changement sous un nouveau metadata.version et un nouveau tag. Republier une version sur un contenu différent changerait en silence ce que des zones ont déjà résolu.
- **Note :** Republier le document identique est un no-op, pas cette erreur.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 409
- **Remédiable hors-ligne :** non (une nouvelle version signée vient de la chaîne de qualification) · **Bloque :** la publication seulement ; jamais dérogeable

### TBY-POL-005

- **Ce qui s'est passé :** Cette instance n'accepte pas d'import unitaire.
- **Cause probable :** Elle est durcie par *\<setting\>* : le contenu n'entre dans son store que par des recipes vérifiées, et un import unitaire ou un empaquetage local serait un contenu qu'aucune recipe ne garantit.
- **Action corrective :** Livrez ce contenu par une recipe signée. Si l'import unitaire est nécessaire sur cette instance, un administrateur retire *\<setting\>* de sa configuration et la redémarre.
- **Où :** l'écran d'import et `POST /api/v1/import` avec son inspection (HTTP 403), l'empaquetage de FileSets à l'écran, par l'API et par `tobby fileset pack` ; chaque refus est un événement d'audit `hardening.refused` qui nomme le réglage
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 403
- **Remédiable hors-ligne :** oui · **Bloque :** l'import ou l'empaquetage ; les synchronisations de recipes ne sont pas concernées

### TBY-POL-006

- **Ce qui s'est passé :** Ce registre est en lecture seule : les push sont refusés.
- **Cause probable :** L'instance est durcie par *\<setting\>* : sa registry embarquée sert du contenu et n'en accepte d'aucun client, quel que soit son rôle. Les synchronisations de recipes écrivent toujours dans le store.
- **Action corrective :** Tirez depuis ce registre, ou poussez vers la registry de la zone. Alimenter cette instance par push exige qu'un administrateur retire *\<setting\>* et la redémarre.
- **Où :** toute écriture sur `/v2/` — upload de blob, morceau, validation, montage, push de manifeste, suppression — pour tous les clients et tous les rôles, administrateurs compris, sous la forme de l'erreur OCI `DENIED` (HTTP 403) dont le message commence par le code, que `docker`, `helm`, `oras` et `skopeo` affichent ; chaque refus est un événement d'audit `hardening.refused` qui nomme le réglage
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 403
- **Remédiable hors-ligne :** oui · **Bloque :** le push ; les tirages et les synchronisations de recipes ne sont pas concernés

## Vérification des signatures et digests (TBY-SIG)

### TBY-SIG-001

- **Ce qui s'est passé :** La signature de la recipe n'a pas pu être vérifiée.
- **Cause probable :** Aucune clé de confiance autorisée par le périmètre de confiance *\<scope\>* ne valide la signature de *\<recipe\>* (clés essayées : *\<fingerprints\>*). Une clé valide pour un autre périmètre ne s'applique pas dans celui-ci. La signature est pourtant valide pour la clé de confiance *\<reserved\>*, réservée au périmètre de confiance *\<reservedto\>*.
- **Action corrective :** La clé qui a signé cette recipe est réservée au périmètre de confiance *\<reservedto\>* : si la recipe doit être admise sous cette clé, ajoutez son repository aux repositories de ce périmètre (trust.scopes[].repositories) ; sinon faites-la re-signer avec une clé qui s'applique ici. Une recipe non vérifiée n'est jamais admise.
- **Ingrédients :** la signature propre d'un ingrédient ne lève ce code que sur une instance qui règle `trust.requireIngredientSignatures: true`. Le sujet se lit alors *ingredient \<nom\> of \<recipe\> (\<dépôt\>@\<digest\>)*, les empreintes sont celles des clés applicables à l'ingrédient (ou « no signature artifact found »), et la recipe entière est retenue. Sans ce réglage, une signature d'ingrédient non vérifiable est consignée et ne bloque rien.
- **Note :** Quand toutes les clés configurées sont réservées, rien n'est essayé et le message le dit.
- **Voir aussi :** [Signatures, clés de confiance et liste blanche](../../security/content-trust/).
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui, quand la bonne clé publique est disponible localement (les clés de confiance sont la configuration de la destination) ; une recipe réellement non signée ou mal signée doit être re-signée en amont · **Bloque :** la recipe concernée ; jamais dérogeable

### TBY-SIG-002

- **Ce qui s'est passé :** Un digest épinglé ne correspond pas au contenu récupéré.
- **Cause probable :** *\<reference\>* épingle *\<expected\>* mais le registre a servi *\<actual\>* — le contenu a changé ou a été altéré.
- **Action corrective :** Ne forcez pas le transfert. Vérifiez le registre source et la recipe ; si le changement est légitime, une recipe re-signée épinglant le nouveau digest est nécessaire.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** non · **Bloque :** l'ingrédient ou la recipe concerné ; jamais dérogeable

### TBY-SIG-003

- **Ce qui s'est passé :** Le type de l'artefact ne correspond pas à la déclaration de la recipe.
- **Cause probable :** *\<reference\>* déclare l'artifactType *\<expected\>* mais le registre a servi *\<actual\>* — le tag a pu être réutilisé pour un autre contenu.
- **Action corrective :** Vérifiez le repository source. Si le changement de type est légitime, mettez à jour et re-signez la recipe ; sinon considérez la source comme compromise.
- **Note :** C'est la garde contre la réutilisation de tag et la confusion de dépôt.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** non · **Bloque :** l'ingrédient ou la recipe concerné ; jamais dérogeable

## Analyse de vulnérabilités (TBY-SCN)

### TBY-SCN-001

- **Ce qui s'est passé :** L'analyseur de vulnérabilités est indisponible : le contenu n'est pas analysé.
- **Cause probable :** *\<detail\>*
- **Action corrective :** Rendez l'analyseur disponible pour cette plateforme : il est intégré à l'image conteneur ; sur un hôte Linux ou Windows, synchronisez la recipe tobby-system qui le transporte ; sous macOS, installez trivy dans le PATH. Sous `scan.mode: block`, rien de ce qui n'a pu être analysé n'est poussé d'ici là.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui, une fois la recipe tobby-system transférée · **Bloque :** l'analyse ; en mode bloquant, tout push

### TBY-SCN-002

- **Ce qui s'est passé :** Le binaire de l'analyseur *\<path\>* ne correspond pas à son digest épinglé et n'a pas été exécuté.
- **Cause probable :** Attendu *\<expected\>*, trouvé *\<actual\>* : le fichier a été remplacé ou endommagé après son épinglage.
- **Action corrective :** Restaurez le binaire depuis la recipe tobby-system (ou depuis la release épinglée), ou mettez à jour `scan.trivy.digest` si le changement est voulu. Le contenu est signalé non analysé d'ici là.
- **Classe :** échec de vérification (sortie 4)
- **Remédiable hors-ligne :** oui · **Bloque :** l'analyse

### TBY-SCN-003

- **Ce qui s'est passé :** Aucune base de vulnérabilités n'est disponible : le contenu n'est pas analysé.
- **Cause probable :** Le store ne contient aucune base sous *\<repository\>*.
- **Action corrective :** Synchronisez d'abord la recipe tobby-system qui transporte la base — par le support miroir en zone isolée. La vérification des supports n'a pas besoin de la base.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui, une fois la recipe tobby-system transférée · **Bloque :** l'analyse ; en mode bloquant, tout push

### TBY-SCN-004

- **Ce qui s'est passé :** La base de vulnérabilités a *\<age\>*, au-delà des *\<max\>* acceptés par cette instance.
- **Cause probable :** La base n'a pas été rafraîchie assez récemment : les vulnérabilités publiées depuis sa construction sont inconnues de l'analyseur.
- **Action corrective :** Synchronisez la recipe tobby-system pour apporter une base fraîche — par le prochain support en zone isolée. Sous `scan.maxDbAgeAction: block`, les éléments analysés avec une base périmée ne sont pas poussés d'ici là.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui, une fois une base fraîche transférée · **Bloque :** rien en mode avertir ; le push de l'élément en mode bloquer

### TBY-SCN-005

- **Ce qui s'est passé :** *\<reference\>* enfreint la politique de vulnérabilités et n'a pas été poussé.
- **Cause probable :** Des vulnérabilités au niveau du seuil de *\<threshold\>* ou au-delà (posé au niveau *\<level\>*) ne sont couvertes par aucune exception : *\<violations\>*.
- **Action corrective :** Publiez une version corrigée en amont et mettez à jour la recipe, ou — si le risque est accepté dans cette zone — listez la CVE en exception dans le `spec.scanPolicy` du Retriever ou dans `scan.exceptions`, avec sa justification.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** non : le correctif vient de l'amont, ou d'une exception décidée par la zone · **Bloque :** l'élément et sa recipe

### TBY-SCN-006

- **Ce qui s'est passé :** *\<reference\>* enfreint la politique de vulnérabilités ; il a été poussé car la politique est consultative.
- **Cause probable :** Des vulnérabilités au niveau du seuil de *\<threshold\>* ou au-delà (posé au niveau *\<level\>*) ne sont couvertes par aucune exception : *\<violations\>*.
- **Action corrective :** Examinez les vulnérabilités. Pour refuser un tel contenu plutôt que le signaler, réglez `scan.mode: block` ; pour accepter une CVE précise dans cette zone, listez-la en exception avec sa justification.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien

### TBY-SCN-007

- **Ce qui s'est passé :** *\<reference\>* n'a pas pu être analysé.
- **Cause probable :** L'analyseur s'est arrêté sur une erreur : *\<detail\>*
- **Action corrective :** Relancez la synchronisation. Si l'erreur persiste, le journal de l'instance porte la sortie de l'analyseur sous le même identifiant de corrélation. Sous `scan.mode: block`, l'élément n'est pas poussé tant qu'il n'est pas analysé.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** l'élément en mode bloquer ; rien en mode avertir

### TBY-SCN-008

- **Ce qui s'est passé :** La base de vulnérabilités se dit construite le *\<built_at\>*, après l'horloge de cette instance (*\<now\>*).
- **Cause probable :** Son âge ne peut pas être établi ; elle est donc traitée comme périmée : soit l'horloge de cet hôte retarde, soit les métadonnées de la base sont fausses.
- **Action corrective :** Vérifiez l'horloge de cet hôte (et de la zone qui a cuisiné la recipe tobby-system), puis synchronisez une base dont la date de construction est passée. Sous `scan.maxDbAgeAction: block`, les éléments analysés avec elle ne sont pas poussés d'ici là.
- **Note :** Une date de construction en avance d'une heure au plus est tolérée comme décalage d'horloge.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** les éléments analysés avec elle sous `scan.maxDbAgeAction: block` ; rien en mode avertir

### TBY-SCN-020

- **Ce qui s'est passé :** Le seuil d'analyse porté par *\<recipe\>* a été ignoré.
- **Cause probable :** La recipe demande *\<annotation\>*, au-dessus des *\<threshold\>* en vigueur pour cette zone ; une recipe peut abaisser le seuil, jamais le relever.
- **Action corrective :** Rien à faire sur cette instance : le seuil de la zone s'applique. L'auteur de la recipe peut retirer ou abaisser l'annotation `recipe.tobbyfetch.dev/scan-threshold`.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien

### TBY-SCN-021

- **Ce qui s'est passé :** L'exception pour *\<id\>* a expiré le *\<expires\>* et ne s'applique plus.
- **Cause probable :** L'exception déclarée au niveau *\<level\>* porte une date d'expiration dépassée.
- **Action corrective :** Renouvelez l'exception avec un nouveau `expires` et sa justification si le risque reste accepté, ou retirez-la et traitez la vulnérabilité.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien en soi

### TBY-SCN-022

- **Ce qui s'est passé :** *\<reference\>* porte des vulnérabilités sans score de criticité : *\<ids\>*.
- **Cause probable :** Les sources d'avis ne donnent à ces vulnérabilités ni score CVSS ni sévérité : la politique ne peut pas les comparer à son seuil.
- **Action corrective :** Examinez-les à la main. Elles ne comptent pas face au seuil ; si l'une doit être refusée, faites corriger le contenu en amont.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien

### TBY-SCN-023

- **Ce qui s'est passé :** *\<reference\>*, déclaré *\<declared\>*, a été analysé comme une image de conteneur, détecté comme image d'après son contenu (*\<evidence\>*).
- **Cause probable :** Ce qui est analysé est décidé par le contenu, jamais par le type déclaré : cet ingrédient est, ou contient, une image qu'un runtime pourrait exécuter ; la politique de vulnérabilités s'y applique comme à toute image.
- **Action corrective :** Rien à faire si ce contenu est attendu. Sinon, demandez à l'auteur de la recipe pourquoi une image est publiée sous ce type ; le verdict associé à cette note s'applique normalement.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien en soi ; le verdict associé décide

### TBY-SCN-024

- **Ce qui s'est passé :** *\<reference\>*, déclaré *\<declared\>*, ne contient aucune image de conteneur : il n'est pas analysé.
- **Cause probable :** Son manifeste n'a ni configuration d'image ni couche de système de fichiers, et aucun de ses fichiers n'est une archive d'image : l'analyseur n'a rien à y lire.
- **Action corrective :** Rien à faire. Un contenu qui n'est pas une image (un modèle, une base de données, un document) est hors du périmètre de l'analyse de vulnérabilités ; examinez-le par les moyens propres à son format.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien

### TBY-SCN-025

- **Ce qui s'est passé :** *\<reference\>*, livré par la recipe signée *\<recipe\>*, est évalué sous une politique consultative.
- **Cause probable :** C'est une livraison de l'analyseur lui-même. La refuser au nom de la politique qu'elle arme laisserait la zone sans moyen d'armer un analyseur, ni de mettre à jour sa base.
- **Action corrective :** Rien à faire. Ses vulnérabilités restent rapportées ; la confiance dans ce contenu repose sur la signature de la recipe tobby-system.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien

### TBY-SCN-026

- **Ce qui s'est passé :** La dérogation pour *\<id\>* ne couvre pas *\<reference\>*.
- **Cause probable :** La dérogation est limitée à *\<scope\>*, et cet élément est hors de cette portée : la vulnérabilité est jugée comme si aucune dérogation n'existait.
- **Action corrective :** Rien à faire si la portée est la bonne. Si cet élément devait être couvert, élargissez la portée de la dérogation dans la configuration — elle reste bornée par son expiration.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien par lui-même — la vulnérabilité est jugée sans la dérogation

### TBY-SCN-027

- **Ce qui s'est passé :** Le document OpenVEX *\<path\>* est inutilisable.
- **Cause probable :** *\<detail\>*
- **Action corrective :** Corrigez ou retirez le document listé sous scan.vex, puis redémarrez. Tobby lit les documents OpenVEX et ne les télécharge jamais : le fichier doit être un document local valide.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** le démarrage de l'instance

### TBY-SCN-028

- **Ce qui s'est passé :** La dérogation du Retriever pour *\<id\>* n'a pas d'expiration.
- **Cause probable :** Elle s'applique, mais sans limite dans le temps : les dérogations déclarées par une instance doivent porter une expiration, et celles des Retrievers le devront aussi à partir du format v1.
- **Action corrective :** Demandez au responsable du Retriever de la zone de donner à la dérogation une date expires et une justification.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien — la dérogation s'applique et est signalée sans limite

## Limites de la destination (TBY-DST)

### TBY-DST-001

- **Ce qui s'est passé :** Le registre de destination ne peut pas stocker cette référence.
- **Cause probable :** *\<reference\>* dépasse une limite de la destination : *\<limit\>*.
- **Action corrective :** Raccourcissez le chemin relocalisé ou ajustez le nommage de destination ; le refus a eu lieu avant tout push.
- **Note :** La limite est typiquement une contrainte de longueur ou de nommage. Les réglages qui façonnent le chemin sont `destination.basePath` et `storage.basePrefix`.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** le push de cette référence seulement

## Charts Helm (TBY-CHT)

### TBY-CHT-001

- **Ce qui s'est passé :** Le chart Helm n'embarque pas une de ses dépendances.
- **Cause probable :** *\<chart\>* déclare la dépendance *\<dependency\>* mais ne l'embarque pas sous charts/ — il ne peut pas se déployer hors-ligne.
- **Action corrective :** Ré-empaquetez le chart avec ses dépendances embarquées (helm dependency build puis helm package), publiez-le, puis relancez l'import.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** non (le chart doit être ré-empaqueté là où il est construit) · **Bloque :** l'import de ce chart

## Store local et état (TBY-STO)

### TBY-STO-001

- **Ce qui s'est passé :** Le store local n'a pas pu être lu.
- **Cause probable :** *\<detail\>*
- **Action corrective :** Vérifiez que le répertoire de stockage existe, est monté et lisible par l'instance, puis réessayez.
- **Note :** Causes typiques : un volume démonté, les permissions, une erreur d'E/S.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 500
- **Remédiable hors-ligne :** oui · **Bloque :** l'opération concernée ; une condition persistante affecte toute l'instance

### TBY-STO-002

- **Ce qui s'est passé :** L'écriture dans le store local a échoué.
- **Cause probable :** *\<detail\>*
- **Action corrective :** Vérifiez l'espace libre et les permissions du répertoire de stockage, puis relancez l'opération.
- **Note :** Le plus souvent l'espace libre ou les permissions.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 500
- **Remédiable hors-ligne :** oui · **Bloque :** l'opération concernée ; une condition persistante affecte toute l'instance

### TBY-STO-003

- **Ce qui s'est passé :** L'écriture du téléchargement partiel dans le répertoire d'état a échoué.
- **Cause probable :** *\<path\>* n'a pas pu être écrit : *\<detail\>* — le plus souvent un manque d'espace libre, ou des permissions que l'utilisateur de l'instance n'a pas sur la racine d'état.
- **Action corrective :** Libérez de l'espace sur le répertoire d'état (il héberge temporairement une copie de chaque blob reprenable) ou corrigez ses permissions, puis relancez la tâche. Abaissez transfer.resumeThreshold pour rendre moins de blobs reprenables, ou mettez-le à 0 pour transférer chaque blob directement vers le store sans fichier temporaire.
- **Note :** Volontairement distinct de [TBY-STO-002](#tby-sto-002) : le répertoire d'état et le store ont des propriétaires, des dimensionnements et des corrections différents.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 500
- **Remédiable hors-ligne :** oui · **Bloque :** les transferts reprenables concernés

### TBY-STO-004

- **Ce qui s'est passé :** L'opération a été refusée avant de démarrer : la cible n'a pas assez d'espace libre.
- **Cause probable :** *\<path\>* demande *\<needed\>* octets et seuls *\<available\>* sont utilisables — *\<free\>* octets sont libres, moins la marge de sécurité de *\<margin\>* %. Il manque *\<shortfall\>* octets.
- **Action corrective :** Libérez au moins *\<shortfall\>* octets sur la cible, visez un volume plus grand, ou supprimez du contenu qui n'est plus référencé. N'abaissez preflight.safetyMarginPercent que si vous acceptez de remplir le volume.
- **Note :** La marge de sécurité est `preflight.safetyMarginPercent`, 10 % par défaut.
- **Note :** `preflight.disabled: true` supprime entièrement la vérification et l'annonce au démarrage.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 507
- **Remédiable hors-ligne :** oui · **Bloque :** la synchronisation ou l'export concerné ; rien n'est écrit

### TBY-STO-005

- **Ce qui s'est passé :** Le système de fichiers de la cible ne peut pas contenir un fichier de cette taille.
- **Cause probable :** *\<path\>* est formaté en *\<filesystem\>*, dont le plus gros fichier possible fait *\<limit\>* octets ; le transfert (*\<what\>*) écrirait un fichier de *\<size\>* octets.
- **Action corrective :** Reformatez le support avec un système de fichiers sans cette limite (exFAT, NTFS, ext4, XFS), ou découpez le transfert pour qu'aucun fichier ne dépasse la limite.
- **Note :** Le cas typique est FAT32, limité à 4 Gio moins un octet. Une archive tar d'export compte pour un seul fichier.
- **Note :** Le même code est émis quand la condition survient en cours d'écriture plutôt qu'au pré-vol : support échangé entre les deux, ou système de fichiers que ce build n'a pas su identifier.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 507
- **Remédiable hors-ligne :** oui · **Bloque :** la synchronisation ou l'export concerné ; en cours d'écriture, le store reste intact

### TBY-STO-006

- **Ce qui s'est passé :** Le store n'a pas été réinitialisé.
- **Cause probable :** La confirmation saisie ne correspond pas : la remise à zéro du store exige de taper *\<phrase\>* exactement, en majuscules, et rien d'autre.
- **Action corrective :** Saisissez *\<phrase\>* dans le champ de confirmation et validez de nouveau. Une remise à zéro supprime tous les artefacts de ce store ; l'historique des opérations et les journaux de tâches sont conservés.
- **Note :** Le journal d'audit est conservé lui aussi : une trace qu'une action destructrice efface n'est pas une trace.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** rien — le store est intact

## Transport sur support amovible (TBY-MED)

### TBY-MED-001

- **Ce qui s'est passé :** Le support ne porte aucun manifeste de média.
- **Cause probable :** *\<path\>* est absent — le store n'a pas été produit par une synchronisation miroir menée à son terme, ou la copie sur le support est partielle.
- **Action corrective :** Recopiez le store depuis l'instance source, ou relancez la synchronisation miroir qui le produit. Rien ne peut être poussé depuis un support sans inventaire, et ce refus n'admet aucune dérogation.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** le support entier, sans dérogation

### TBY-MED-002

- **Ce qui s'est passé :** Le manifeste de média est illisible.
- **Cause probable :** *\<path\>* est tronqué, non analysable ou incohérent : *\<detail\>*.
- **Action corrective :** Recopiez le store depuis l'instance source puis vérifiez à nouveau. Un manifeste qui ne s'analyse pas ne laisse rien à quoi confronter le support, et ce refus n'admet aucune dérogation.
- **Note :** Incohérences typiques : un chemin qui sort du store, une entrée d'inventaire en double, un nom de dépôt qui n'en est pas un.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** le support entier, sans dérogation

### TBY-MED-003

- **Ce qui s'est passé :** Le manifeste de média utilise une version de format non prise en charge.
- **Cause probable :** Le support déclare le format de manifeste *\<found\>* ; cette version de Tobby lit le format *\<supported\>*.
- **Action corrective :** Utilisez de ce côté une version de Tobby correspondant au support, ou reproduisez le support avec la version qui tourne ici.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** le support entier

### TBY-MED-004

- **Ce qui s'est passé :** Le support utilise une version de format de store non prise en charge.
- **Cause probable :** Le support déclare le format de store *\<found\>* ; cette version de Tobby lit le format *\<supported\>*.
- **Action corrective :** Utilisez une version de Tobby correspondant au support, ou transférez le contenu via le format OCI image layout avec l'outillage standard (skopeo, oras, crane).
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** le support entier

### TBY-MED-005

- **Ce qui s'est passé :** Le graphe de recipes du support ne correspond pas à son inventaire.
- **Cause probable :** *\<path\>* a pour empreinte *\<actual\>* alors que le manifeste enregistre *\<expected\>* — la liste de ce que le support livre a changé depuis l'écriture du manifeste.
- **Action corrective :** Recopiez le store depuis l'instance source. Le graphe de recipes est ce à partir de quoi chaque verdict par recipe est calculé : un graphe altéré bloque le support entier, sans dérogation.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** le support entier, sans dérogation

### TBY-MED-006

- **Ce qui s'est passé :** Le support est adressé à une autre zone.
- **Cause probable :** Le manifeste nomme la zone *\<found\>* ; cette instance sert la zone *\<expected\>*.
- **Action corrective :** Vérifiez qu'il s'agit bien du support destiné à cette zone. Un administrateur peut lever le refus délibérément ; la dérogation est consignée au journal d'audit.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 409
- **Remédiable hors-ligne :** oui · **Bloque :** le support entier, dérogation admin possible

### TBY-MED-007

- **Ce qui s'est passé :** Le support est plus ancien que le dernier importé pour cette zone.
- **Cause probable :** Le support *\<media\>* a été résolu le *\<resolved\>*, alors que la zone *\<zone\>* a importé en dernier un contenu résolu le *\<recorded\>* — l'importer ferait reculer la zone.
- **Action corrective :** Vérifiez que vous avez branché le support courant. Un administrateur peut lever le refus délibérément — pour restaurer volontairement une livraison antérieure, par exemple ; la dérogation est consignée au journal d'audit.
- **Note :** Garde-fou anti-accident, pas contrôle de sécurité : le manifeste n'est pas signé.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 409
- **Remédiable hors-ligne :** oui · **Bloque :** le support entier, dérogation admin possible

### TBY-MED-010

- **Ce qui s'est passé :** Un fichier nécessaire à la recipe est absent du support.
- **Cause probable :** *\<recipe\>* atteint *\<path\>*, que le support ne porte pas — copie partielle, ou fichier supprimé.
- **Action corrective :** Recopiez le store depuis l'instance source. La recipe est bloquée entière, sans dérogation ; les recipes intactes du même support restent poussables.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** cette recipe entière, sans dérogation

### TBY-MED-011

- **Ce qui s'est passé :** Un fichier du support n'a pas la taille attendue.
- **Cause probable :** *\<path\>* contient *\<actual\>* octets alors que le manifeste enregistre *\<expected\>* — le fichier a été tronqué ou modifié après l'écriture du manifeste.
- **Action corrective :** Recopiez le store depuis l'instance source. La recipe qui atteint ce fichier est bloquée entière, sans dérogation.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** la recipe qui atteint ce fichier, sans dérogation

### TBY-MED-012

- **Ce qui s'est passé :** Un fichier du support ne correspond pas à son empreinte enregistrée.
- **Cause probable :** *\<path\>* a pour empreinte *\<actual\>* alors que le manifeste enregistre *\<expected\>* — le contenu a été corrompu ou altéré pendant le transport.
- **Action corrective :** Recopiez le store depuis l'instance source. La recipe qui atteint ce fichier est bloquée entière, sans dérogation.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** la recipe qui atteint ce fichier, sans dérogation

### TBY-MED-013

- **Ce qui s'est passé :** Un fichier nécessaire à la recipe est absent de l'inventaire.
- **Cause probable :** *\<recipe\>* atteint *\<path\>*, que le manifeste de média ne liste pas — le manifeste ne peut donc pas en répondre.
- **Action corrective :** Reproduisez le support depuis l'instance source pour que le manifeste couvre tout ce que les recipes atteignent. La recipe est bloquée entière, sans dérogation.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** cette recipe entière, sans dérogation

### TBY-MED-014

- **Ce qui s'est passé :** Un manifeste du support est illisible.
- **Cause probable :** *\<path\>* n'est pas lisible comme manifeste ou index OCI : *\<detail\>*.
- **Action corrective :** Recopiez le store depuis l'instance source. Ce que la recipe livre ne peut pas être établi à partir d'un manifeste illisible : la recipe est bloquée entière.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** cette recipe entière

### TBY-MED-015

- **Ce qui s'est passé :** Un blob du support est rangé sous une mauvaise empreinte.
- **Cause probable :** *\<path\>* est adressé comme *\<expected\>* mais ses octets ont pour empreinte *\<actual\>* — le stockage adressé par contenu se contredit.
- **Action corrective :** Recopiez le store depuis l'instance source. La recipe qui atteint ce blob est bloquée entière, sans dérogation.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** la recipe qui atteint ce blob, sans dérogation

### TBY-MED-016

- **Ce qui s'est passé :** Le graphe de recipes du support ne correspond pas à la recipe signée.
- **Cause probable :** Pour *\<recipe\>*, la comptabilité propre du support liste des ingrédients que le document de recipe signé n'épingle pas : *\<detail\>*. La comptabilité n'est pas signée ; le document de recipe l'est.
- **Action corrective :** N'importez pas cette livraison : la comptabilité a été modifiée, ou le support a été assemblé à la main. Reproduisez le support depuis l'instance source. La recipe est bloquée entière, sans dérogation.
- **Note :** La comptabilité est `meta/recipes.json`, recopiée dans `meta/media.json`. Le code est aussi émis quand l'artefact porte le document d'une autre recipe, ou quand un digest ou un emplacement diffère de ce que le document signé épingle.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** cette recipe entière, sans dérogation

### TBY-MED-020

- **Ce qui s'est passé :** Un fichier du support n'est pas couvert par l'inventaire.
- **Cause probable :** *\<path\>* se trouve sous la couverture du manifeste mais celui-ci ne le liste pas — il a été ajouté après l'écriture du manifeste.
- **Action corrective :** Rien à faire pour continuer : le contenu surnuméraire n'est jamais poussé. Cherchez comment il est arrivé sur le support si ce n'est pas vous qui l'y avez mis.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien (signalé seulement)

### TBY-MED-021

- **Ce qui s'est passé :** Un fichier du support n'est atteint par aucune recipe.
- **Cause probable :** *\<path\>* est inventorié mais aucune recipe de ce support ne le livre — le plus souvent, un reliquat d'une livraison antérieure.
- **Action corrective :** Rien à faire pour continuer : un contenu atteint par aucune recipe n'est jamais poussé. Élaguez le store source si vous voulez que le support porte moins.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien (signalé seulement)

### TBY-MED-022

- **Ce qui s'est passé :** Un fichier de tenue de registre du support ne correspond pas à son empreinte enregistrée.
- **Cause probable :** *\<path\>* ne correspond pas à son entrée d'inventaire — la comptabilité interne du store a été modifiée après l'écriture du manifeste.
- **Action corrective :** Rien n'est poussé depuis ce fichier, la vérification continue ; recopiez le store depuis l'instance source si vous ne l'avez pas modifié délibérément.
- **Note :** Cela couvre la comptabilité interne du store hors graphe de recipes, dont l'altération bloque le support entier ([TBY-MED-005](#tby-med-005)).
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien (signalé seulement)

### TBY-MED-023

- **Ce qui s'est passé :** Le support porte un cache d'extraction de FileSet, ignoré et reconstruit.
- **Cause probable :** *\<path\>* contient *\<files\>* fichiers (*\<bytes\>* octets) extraits ailleurs. Le cache est hors de la couverture du manifeste, donc rien n'en a été haché ; cette instance ne le sert jamais et le reconstruit depuis les blobs vérifiés.
- **Action corrective :** Rien à faire pour l'import. Si vous n'attendiez pas d'extraction sur ce support, inspectez ou supprimez ce répertoire avant que le support ne reparte : il franchit la coupure sans vérification.
- **Note :** L'instance source servait des FileSets depuis ce store.
- **Note :** Tout ce qui, dessous, n'a pas la forme d'une extraction est signalé à part en [TBY-MED-020](#tby-med-020).
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien (signalé seulement)

### TBY-MED-030

- **Ce qui s'est passé :** Ce support n'a pas encore été vérifié : rien n'en est servi.
- **Cause probable :** *\<surface\>* est fermé parce que le support *\<media\>* vient d'une autre zone et qu'aucune vérification n'a établi que son contenu est intact et signé (FR-054). L'instance, elle, fonctionne normalement.
- **Action corrective :** Ouvrez *\<screen\>* dans l'interface web et lancez Vérifier — la vérification recalcule les empreintes du support et peut prendre plusieurs minutes sur un disque plein — ou appelez POST /api/v1/media/verify sur cette instance. Cette surface s'ouvre d'elle-même dès que le support est vérifié. `tobby media verify` rend le même verdict mais s'exécute dans son propre processus et n'ouvre pas les surfaces de cette instance : seule une vérification exécutée par l'instance elle-même le fait.
- **Note :** FR-054 exige que la vérification précède toute poussée, tout service et toute écriture locale : `/v2/` et `/files/` restent donc fermés tant qu'elle n'a pas eu lieu.
- **Note :** Aucun réglage ne permet délibérément de servir un support sans le vérifier.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 403
- **Remédiable hors-ligne :** oui · **Bloque :** la registry embarquée et la surface de fichiers, pour ce support

### TBY-MED-031

- **Ce qui s'est passé :** Une vérification de ce support est déjà en cours.
- **Cause probable :** Une exécution parcourt le support (étape : *\<stage\>*) ; en lancer une seconde ralentirait les deux sans rien apprendre de plus.
- **Action corrective :** Attendez la fin de l'exécution en cours. Son verdict s'affiche sur l'écran Média et sur GET /api/v1/media/verification.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 409
- **Remédiable hors-ligne :** oui · **Bloque :** la seconde vérification seulement

### TBY-MED-032

- **Ce qui s'est passé :** Ce support n'a pas passé la vérification : rien n'en est servi.
- **Cause probable :** *\<surface\>* est fermé parce que le support *\<media\>* a été vérifié « *\<verdict\>* » et non intact. Servir engage le store entier — un blob atteint par une recipe bloquée est exactement le contenu qui a échoué — donc, contrairement à la décision de poussée, cela ne se décide pas recipe par recipe.
- **Action corrective :** Lisez le rapport sur *\<screen\>* : il nomme chaque recipe bloquée et le fichier fautif. Recopiez le support depuis l'instance source, puis vérifiez de nouveau. Les recipes intactes restent poussables vers la registry de zone, qui les sert ensuite.
- **Note :** Le verdict est *partiel* ou *bloqué* : au moins une livraison a échoué sur sa signature ou sur l'une des empreintes de ses ingrédients. `/v2/` et `/files/` distribuent des blobs.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 403
- **Remédiable hors-ligne :** oui · **Bloque :** la registry embarquée et la surface de fichiers, pour ce support

### TBY-MED-033

- **Ce qui s'est passé :** Cette requête sort du contenu que la vérification du support a validé.
- **Cause probable :** *\<surface\>* du support *\<media\>* ne sert, en lecture seule, que ce qu'atteignent les recipes poussables, et *\<path\>* n'en fait pas partie — contenu atteint par aucune recipe vérifiée, tag ajouté après la vérification, ou écriture.
- **Action corrective :** Lisez le rapport sur *\<screen\>* : le contenu hors de l'ensemble vérifié figure dans ses constats et n'est jamais servi. Livrez-le par une recipe sur un nouveau support, ou poussez-le dans la registry de zone.
- **Note :** Une fois la porte ouverte, `/v2/` et `/files/` servent les artefacts, signatures, ingrédients et signatures propres des ingrédients des recipes poussables. Un tag ajouté ou déplacé après la vérification et toute écriture sont refusés.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 403
- **Remédiable hors-ligne :** oui · **Bloque :** cette requête seulement

### TBY-MED-034

- **Ce qui s'est passé :** La vérification de ce support n'a validé aucun contenu : rien n'en est servi.
- **Cause probable :** Le support *\<media\>* ne liste aucune recipe : rien n'y est atteint par une livraison et rien n'a été recalculé. *\<surface\>* ne sert que ce qu'atteignent les recipes poussables, c'est-à-dire ici rien.
- **Action corrective :** Vérifiez que la synchronisation source a livré ses recipes avant l'écriture du support, puis recopiez-le et vérifiez-le de nouveau sur *\<screen\>*.
- **Note :** Le verdict est *poussable*, sans rien à pousser. Un tel support ouvrait auparavant la porte et servait tout son store.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 403
- **Remédiable hors-ligne :** oui · **Bloque :** la registry embarquée et la surface de fichiers, pour ce support

## Intégrité du store (TBY-INT)

### TBY-INT-001

- **Ce qui s'est passé :** Le blob *\<path\>* ne correspond pas à son digest.
- **Cause probable :** Son contenu donne *\<actual\>* au lieu de *\<expected\>* : le fichier a été endommagé ou modifié sur le disque.
- **Action corrective :** La vérification ne modifie rien. Re-synchronisez les recipes qui atteignent ce blob pour le récupérer à nouveau, ou restaurez le store depuis une copie saine ; vérifiez le disque.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** selon : re-télécharger demande une source ; restaurer une copie non · **Bloque :** rien — le rapport le constate

### TBY-INT-002

- **Ce qui s'est passé :** *\<reference\>* est incomplet dans le store.
- **Cause probable :** Il référence un contenu absent du store : *\<missing\>*.
- **Action corrective :** Re-synchronisez la recipe qui l'atteint pour récupérer le contenu manquant, ou restaurez le store depuis une copie saine.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** selon : re-télécharger demande une source ; restaurer une copie non · **Bloque :** rien — le rapport le constate

### TBY-INT-003

- **Ce qui s'est passé :** *\<path\>* n'est atteint par aucune recipe.
- **Cause probable :** Aucune recipe enregistrée ni aucune racine protégée (imports unitaires, base de vulnérabilités, contenu amorcé) n'atteint ce contenu.
- **Action corrective :** Rien n'est requis. Pour récupérer l'espace, élaguez selon le Retriever ou retirez la recipe qui le portait ; la vérification elle-même ne supprime jamais rien.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien

### TBY-INT-004

- **Ce qui s'est passé :** Une vérification du store est déjà en cours.
- **Cause probable :** La vérification *\<task\>* parcourt le store ; en lancer deux à la fois diviserait leur débit par deux sans rien apprendre de plus.
- **Action corrective :** Suivez la vérification en cours sur l'écran des tâches, ou attendez sa fin pour en lancer une nouvelle.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 409
- **Remédiable hors-ligne :** oui · **Bloque :** la requête

### TBY-INT-005

- **Ce qui s'est passé :** *\<path\>* ne peut pas être lu comme un manifest.
- **Cause probable :** *\<detail\>*
- **Action corrective :** Re-synchronisez la recipe qui l'atteint, ou restaurez le store depuis une copie saine ; ce que ce manifest référence n'a pas pu être contrôlé.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** selon : re-télécharger demande une source ; restaurer une copie non · **Bloque :** rien — le rapport le constate

### TBY-INT-006

- **Ce qui s'est passé :** La vérification du store a été annulée.
- **Cause probable :** La vérification *\<task\>* a été arrêtée avant d'atteindre un verdict : elle ne dit rien du store.
- **Action corrective :** Lancez une nouvelle vérification quand vous voulez une réponse. Rien n'a été modifié : une vérification ne fait que lire.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien

## Export et import OCI image layout (TBY-LAY)

### TBY-LAY-001

- **Ce qui s'est passé :** Ce n'est pas un OCI image layout exploitable.
- **Cause probable :** *\<path\>* n'a pas pu être lu comme tel : *\<detail\>*
- **Action corrective :** Vérifiez que le chemin désigne bien le layout lui-même — le répertoire (ou son tar non compressé) contenant `oci-layout`, `index.json` et `blobs/`. Une archive compressée doit d'abord être décompressée. `skopeo copy oci:<chemin>:<tag> …` sur le même chemin vous dira si un outil OCI y arrive davantage.
- **Note :** Causes typiques : pas de marqueur `oci-layout`, un `index.json` qui ne s'analyse pas, ou un blob dont l'empreinte ne correspond pas au digest qui l'adresse.
- **Note :** Tobby lit une archive non compressée en se positionnant sur un décalage de blob enregistré, ce qui ne laisse rien à déployer à une bombe de décompression.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** l'import concerné ; rien n'est écrit

### TBY-LAY-002

- **Ce qui s'est passé :** L'archive a été refusée : une de ses entrées n'a rien à faire dans un image layout.
- **Cause probable :** Dans *\<path\>*, l'entrée *\<entry\>* a été refusée — *\<reason\>*.
- **Action corrective :** Rien n'a été écrit. Une archive OCI image layout ne contient que `oci-layout`, `index.json` et des fichiers `blobs/<algorithme>/<digest>` : une archive qui porte en plus des liens ou des chemins sortant d'elle-même n'a pas été produite par un outil OCI. Faites refabriquer le support à sa source, et signalez-le comme incident s'il provient de l'extérieur de votre organisation.
- **Note :** C'est un échec de vérification, pas un transfert abîmé.
- **Classe :** échec de vérification (sortie 4) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** l'import concerné ; rien n'est écrit

### TBY-LAY-003

- **Ce qui s'est passé :** La destination de l'export existe déjà.
- **Cause probable :** *\<path\>* est déjà présent, et cet export n'était pas autorisé à le remplacer.
- **Action corrective :** Choisissez une autre destination, ou relancez l'export avec l'option de remplacement (`--overwrite` en ligne de commande) une fois certain que ce qui s'y trouve peut être perdu.
- **Note :** Un export écrit dans un chemin de préparation puis le renomme en place : il n'écrase donc jamais à moitié.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 409
- **Remédiable hors-ligne :** oui · **Bloque :** l'export concerné ; rien n'est écrit

## Empaquetage d'ensembles de fichiers (TBY-FIL)

### TBY-FIL-001

- **Ce qui s'est passé :** L'ensemble de fichiers n'a pas pu être empaqueté.
- **Cause probable :** *\<detail\>*
- **Action corrective :** Indiquez à `tobby fileset pack` un répertoire existant et non vide, et donnez à l'ensemble un nom en minuscules et une version (par exemple `tobby fileset pack ./repo site-docs:1.0.0`). Rien n'a été écrit dans le magasin.
- **Note :** Le plus souvent un répertoire inexistant, vide ou illisible, ou un nom et une version qui ne s'analysent pas.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** l'empaquetage concerné ; rien n'est écrit

### TBY-FIL-002

- **Ce qui s'est passé :** Le répertoire contient une entrée qui ne peut pas être empaquetée sans risque.
- **Cause probable :** *\<detail\>*
- **Action corrective :** Supprimez ou remplacez cette entrée, puis empaquetez à nouveau : un ensemble de fichiers est extrait et servi sur d'autres machines, donc les liens symboliques doivent rester dans le répertoire, les fichiers spéciaux et les bits setuid ne sont pas transportés, et les noms ne doivent pas commencer par `.wh.`. Rien n'a été écrit dans le magasin.
- **Note :** Sont aussi refusés : un nom portant un antislash ou un octet NUL. Un nom commençant par `.wh.` est refusé parce qu'un lecteur de couche le prendrait pour une suppression.
- **Note :** Un ensemble de fichiers est extrait et servi sur d'autres machines, Windows compris : les refus portent sur l'endroit où le contenu atterrit, pas sur cet hôte.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 422
- **Remédiable hors-ligne :** oui · **Bloque :** l'empaquetage concerné ; rien n'est écrit

### TBY-FIL-003

- **Ce qui s'est passé :** L'empaquetage de ce répertoire n'est pas autorisé depuis cette surface.
- **Cause probable :** *\<path\>* est en dehors des répertoires que `files.packRoots` autorise l'interface web et l'API à lire ; sans entrée configurée, elles n'en lisent aucun.
- **Action corrective :** Lancez `tobby fileset pack` sur l'hôte de l'instance, qui n'est pas restreint, ou ajoutez le répertoire à `files.packRoots` dans le fichier de configuration puis redémarrez l'instance.
- **Note :** Lire un répertoire arbitraire de l'hôte sur requête réseau est une capacité qu'on donne à une instance, pas qu'elle possède.
- **Note :** `tobby fileset pack` n'est pas restreint sur l'hôte de l'instance parce que celui qui l'exécute détient déjà ces droits sur le système de fichiers.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 403
- **Remédiable hors-ligne :** oui · **Bloque :** l'empaquetage concerné ; rien n'est écrit

## Aides à la consommation (TBY-AID)

### TBY-AID-001

- **Ce qui s'est passé :** Aucune aide à la consommation ne peut être générée : cette instance ne nomme aucune registry depuis laquelle la zone tire.
- **Cause probable :** La table de correspondance et l'extrait registries.yaml pointent vers la registry de la zone. Cette instance ne pousse vers aucune destination.registry, et consumption.registry n'est pas renseigné.
- **Action corrective :** Renseignez destination.registry si cette instance pousse vers la registry de la zone, ou consumption.registry avec l'adresse depuis laquelle les clients de la zone tirent — cette instance elle-même quand elle sert la zone.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 409
- **Remédiable hors-ligne :** oui · **Bloque :** la génération de l'aide

## Bundle de diagnostic (TBY-DIA)

### TBY-DIA-001

- **Ce qui s'est passé :** Le bundle de diagnostic n'a pas pu être produit.
- **Cause probable :** *\<detail\>*
- **Action corrective :** Réessayez ; en cas de nouvel échec, collectez les journaux de l'instance par les moyens du déploiement. Rien n'a été écrit ni envoyé : un bundle est uniquement local.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 500
- **Remédiable hors-ligne :** oui · **Bloque :** le bundle

### TBY-DIA-002

- **Ce qui s'est passé :** Le bundle de diagnostic a été retenu : *\<member\>* contenait encore un secret.
- **Cause probable :** Avant de livrer un bundle, Tobby cherche dans chaque membre les valeurs secrètes qu'il connaît. L'une d'elles a été trouvée : le bundle n'a pas été écrit plutôt que de l'être avec elle.
- **Action corrective :** Signalez ce défaut aux mainteneurs de Tobby avec le nom du membre — jamais avec le bundle. En attendant, collectez les informations à la main.
- **Classe :** refus de politique (sortie 3) · **Statut HTTP :** 500
- **Remédiable hors-ligne :** oui · **Bloque :** le bundle — jamais livré avec un secret dedans

### TBY-DIA-003

- **Ce qui s'est passé :** Un bundle de diagnostic est déjà en cours de construction sur cette instance.
- **Cause probable :** Un seul bundle est construit à la fois : chacun garde ses membres en mémoire et y cherche les secrets, et des demandes simultanées multiplieraient ce coût.
- **Action corrective :** Attendez quelques secondes la fin du bundle en cours, puis redemandez. Rien n'a été écrit ni envoyé.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 409
- **Remédiable hors-ligne :** oui · **Bloque :** ce bundle seulement — rien n'a été écrit ni envoyé

## Cohérence d'horloge (TBY-CLK)

### TBY-CLK-001

- **Ce qui s'est passé :** L'horloge locale indique *\<now\>*, une date antérieure à la construction de ce binaire (*\<anchor\>*).
- **Cause probable :** L'horloge retarde de *\<gap\>* sur une date que ce binaire prouve passée. Les expirations, les contrôles de fraîcheur et la validité des certificats sont jugés sur cette horloge.
- **Action corrective :** Réglez l'horloge de l'hôte (source de temps de la zone, ou à la main depuis une référence de confiance), puis redémarrez. Tobby avertit et consigne ; il ne corrige pas l'horloge.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien — un avertissement, consigné au journal d'audit

### TBY-CLK-002

- **Ce qui s'est passé :** L'horloge locale indique *\<now\>*, une date antérieure à la production du support *\<medium\>* (*\<anchor\>*).
- **Cause probable :** Le support a été résolu *\<gap\>* après l'heure que croit avoir cet hôte : l'une des deux horloges est fausse, et les contrôles de fraîcheur et d'expiration en dépendent.
- **Action corrective :** Vérifiez l'horloge de cet hôte contre une référence de confiance avant de vous fier aux verdicts d'expiration et de fraîcheur. Tobby avertit et consigne ; il ne corrige pas l'horloge.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien — un avertissement, consigné au journal d'audit

## Répertoire d'état de l'instance (TBY-STA)

### TBY-STA-001

- **Ce qui s'est passé :** L'instance refuse de démarrer : le répertoire d'état *\<root\>* a une organisation inconnue (*\<version\>*).
- **Cause probable :** Il a été écrit par une autre version de Tobby, ou ce n'est pas un répertoire d'état de Tobby.
- **Action corrective :** Démarrez la version qui l'a écrit, ou restaurez le répertoire d'état depuis une sauvegarde faite avec cette version. L'organisation documentée est le contrat de sauvegarde.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** le démarrage de l'instance

### TBY-STA-002

- **Ce qui s'est passé :** L'instance refuse de démarrer : *\<path\>* est ouvert à tous les utilisateurs de l'hôte (*\<mode\>*).
- **Cause probable :** C'est une entrée secrète du répertoire d'état — empreintes de mots de passe ou de jetons, clé privée — lisible par d'autres utilisateurs, ou un répertoire qui en contient une et où d'autres utilisateurs peuvent écrire (donc remplacer le secret), ou une entrée dont la liste d'accès n'a pas pu être lue, que Tobby refuse plutôt que de la supposer restreinte. Tobby les crée pour leur seul propriétaire ; un mode plus large vient en général d'une restauration qui n'a pas conservé les droits.
- **Action corrective :** Retirez l'accès des autres utilisateurs (chmod o-rwx sous Unix, sur l'entrée et sur les répertoires qui la contiennent, et pas de racine d'état dans un répertoire partagé à sticky bit appartenant à un autre compte ; sous Windows, retirez Tout le monde, Utilisateurs, Utilisateurs authentifiés et les autres principaux larges de sa liste d'accès), vérifiez qui a pu la lire ou la remplacer entre-temps, puis redémarrez. L'accès de groupe accordé par le volume (fsGroup Kubernetes) est toléré.
- **Classe :** refus de politique (sortie 3)
- **Remédiable hors-ligne :** oui · **Bloque :** le démarrage de l'instance

### TBY-STA-003

- **Ce qui s'est passé :** Le répertoire d'état ne contient pas *\<entry\>*.
- **Cause probable :** Une entrée d'identité manque — en général après une restauration incomplète. L'instance démarre sans elle, et se comporte comme une instance neuve pour ce qu'elle contenait.
- **Action corrective :** Si cette instance a été restaurée, restaurez l'entrée manquante depuis la même sauvegarde et redémarrez ; sinon, rien à faire.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** rien par lui-même — l'instance démarre sans l'entrée

## Tâches (TBY-TSK)

### TBY-TSK-001

- **Ce qui s'est passé :** La tâche n'existe pas.
- **Cause probable :** Aucune tâche d'identifiant *\<id\>* sur cette instance — lien erroné, ou store réinitialisé.
- **Action corrective :** Ouvrez la liste des tâches et suivez le lien d'une tâche existante.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 404
- **Remédiable hors-ligne :** oui · **Bloque :** la requête seulement

## Instance (TBY-SRV)

### TBY-SRV-001

- **Ce qui s'est passé :** Une erreur interne s'est produite.
- **Cause probable :** Une condition inattendue a interrompu la requête ; le détail est dans les journaux de l'instance.
- **Action corrective :** Réessayez ; si cela persiste, cherchez dans les journaux de l'instance l'identifiant de corrélation affiché avec cette erreur.
- **Voir aussi :** [Métriques et logs](../../reference/metrics-logs/).
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 500
- **Remédiable hors-ligne :** oui (le diagnostic est local) · **Bloque :** la requête seulement

### TBY-SRV-002

- **Ce qui s'est passé :** Cette ressource n'existe pas.
- **Cause probable :** L'adresse est erronée, ou le contenu a été supprimé.
- **Action corrective :** Revenez au navigateur de contenu ou utilisez la recherche.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 404
- **Remédiable hors-ligne :** oui · **Bloque :** la requête seulement

### TBY-SRV-003

- **Ce qui s'est passé :** L'instance est injoignable.
- **Cause probable :** La connexion réseau est tombée, ou l'instance redémarre.
- **Action corrective :** Vérifiez votre liaison réseau et réessayez ; la page reprend dès que l'instance répond.
- **Note :** Une condition côté client, rendue par la coquille de l'interface sur échec de transport — jamais servie par l'instance elle-même ; cataloguée pour que ce guide la documente.
- **Classe :** échec opérationnel (sortie 1)
- **Remédiable hors-ligne :** oui · **Bloque :** l'affichage de la session de navigation seulement

### TBY-SRV-004

- **Ce qui s'est passé :** La requête est trop volumineuse.
- **Cause probable :** Son corps dépasse les *\<limit\>* octets que ce point d'accès accepte ; elle a été refusée avant d'être lue.
- **Action corrective :** Envoyez un corps plus petit. Un document au-delà de la borne n'est pas de ceux que ce point d'accès traite : vérifiez que le bon fichier a été envoyé.
- **Note :** Chaque point d'accès de l'API a une borne dimensionnée sur son plus grand document légitime : voir le tableau de [validation des entrées](../../security/input-validation/) (NFR-023).
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 413
- **Remédiable hors-ligne :** oui · **Bloque :** la requête seulement

### TBY-SRV-005

- **Ce qui s'est passé :** Le corps de la requête n'est pas d'un type que ce point d'accès accepte.
- **Cause probable :** Il est déclaré *\<type\>* ; ce point d'accès ne décode que *\<want\>*. Une page d'une autre origine ne peut pas envoyer ce type sans une vérification préalable (preflight) que l'instance n'accorde jamais : c'est pourquoi rien d'autre n'est lu.
- **Action corrective :** Envoyez le corps avec l'en-tête Content-Type: *\<want\>* (curl : -H 'Content-Type: *\<want\>*'). La CLI et l'interface le font déjà.
- **Note :** Une requête sans `Content-Type` est refusée de la même façon. Tous les points d'accès de l'API qui lisent un document décodent `application/json`.
- **Classe :** échec opérationnel (sortie 1) · **Statut HTTP :** 415
- **Remédiable hors-ligne :** oui · **Bloque :** la requête seulement
