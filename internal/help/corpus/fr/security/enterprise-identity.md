---
title: Identité d'entreprise (OIDC, SAML)
description: Raccorder Tobby à l'annuaire de l'organisation en OpenID Connect ou SAML 2.0, faire correspondre groupes et rôles, borner sessions et jetons, et continuer à travailler quand le fournisseur d'identité est en panne.
sidebar:
  order: 5
---

Tobby parle les deux protocoles qu'offre un annuaire d'entreprise —
**OpenID Connect** (flux « authorization code ») et **SAML 2.0** (initié
par le fournisseur de service) — et garde ses deux méthodes locales,
comptes et jetons statiques, opérationnelles à côté (ADR-0009, FR-070,
FR-071, FR-078). Les deux méthodes fédérées sont **désactivées par
défaut** ; en activer une ne désactive jamais rien d'autre.

## Déroulé d'une connexion fédérée

1. La page de connexion affiche un bouton par fournisseur activé, au-dessus
   du formulaire local.
2. Le bouton lance un aller-retour sous `/auth/` : Tobby retient ce qu'il a
   envoyé (state, nonce et vérificateur PKCE en OIDC ; identifiant de
   requête en SAML) **côté serveur**, et ne confie au navigateur qu'un
   cookie de liaison opaque.
3. Le fournisseur renvoie le navigateur ; Tobby vérifie la réponse, fait
   correspondre les groupes de la personne à un rôle, et ouvre une session
   Tobby ordinaire — même délai d'inactivité, même protection CSRF, mêmes
   planchers de rôle qu'une session locale.

Une réponse qui ne revient pas au navigateur qui a lancé la connexion est
refusée, quel que soit son contenu. Après connexion, le navigateur arrive
sur la page demandée, via le même contrôle anti-redirection-ouverte que
le formulaire local.

Les identités fédérées apparaissent dans le journal d'audit et sur l'écran
du compte sous la forme `oidc:<identifiant>` ou `saml:<NameID>` : jamais
confondues avec un compte local du même nom. Elles n'ont pas de mot de
passe local à changer.

## OpenID Connect

Déclarez Tobby chez le fournisseur comme **client confidentiel**, flux
authorization code, **PKCE S256** obligatoire, avec l'URI de redirection
`https://<hôte-tobby>/auth/oidc/callback`. Demandez que les groupes de
l'utilisateur figurent dans l'**ID token** (claim `groups` par défaut).

```yaml
auth:
  oidc:
    enabled: true
    displayName: Annuaire de l'entreprise   # le bouton affiche « Se connecter avec … »
    issuer: https://idp.example.com/realms/corp
    clientID: tobby
    clientSecretFile: /etc/tobby/oidc-client-secret   # un chemin, jamais la valeur
    redirectURL: https://tobby.example.com/auth/oidc/callback
    scopes: [profile]                       # « openid » est toujours demandé
    groupsClaim: groups
    # signingAlgorithms: [RS256, ES256]     # défaut : tous les algorithmes asymétriques
    roles:
      viewer:   [tobby-viewers]
      operator: [tobby-operators]
      admin:    [tobby-admins]
```

Ce que Tobby vérifie, et refuse sinon avec **TBY-AUTH-014** (le contrôle
précis figure dans le journal de l'instance, jamais sur la page) :

- découverte depuis l'issuer configuré uniquement, dont le document doit
  nommer exactement cet issuer ;
- `state`, `nonce` et vérificateur PKCE de la connexion lancée par ce
  navigateur — chacun à usage unique ;
- la signature de l'ID token, contre les clés publiées par le fournisseur,
  avec un algorithme de la liste blanche : `none` et la famille `HS*` sont
  refusés en configuration comme dans le code ;
- l'issuer et l'audience (l'identifiant client ; un jeton émis pour
  plusieurs clients doit désigner Tobby comme partie autorisée), et
  l'expiration.

`issuer` doit être en `https://` ; l'URL de redirection doit être le
chemin `/auth/oidc/callback` de l'instance. Tout appel au fournisseur
passe par les [réglages réseau sortants](../../passthrough/network/) de
l'instance — proxy, autorités de certification privées — et ne suit
aucune redirection.

## SAML 2.0

Déclarez Tobby auprès du fournisseur d'identité comme fournisseur de
service : son descripteur est servi à
`https://<hôte-tobby>/auth/saml/metadata` (entity ID, service
consommateur d'assertions, certificat). Côté fournisseur, exigez des
**assertions signées** (ou des réponses signées), la liaison **HTTP-POST**
pour la réponse, un `NameID` stable, un **attribut de groupes**
multivalué, et désactivez la connexion initiée par le fournisseur.

```yaml
auth:
  saml:
    enabled: true
    displayName: SSO de l'entreprise
    entityID: https://tobby.example.com/auth/saml/metadata
    acsURL: https://tobby.example.com/auth/saml/acs
    idpMetadataFile: /etc/tobby/idp-metadata.xml   # épinglé
    # idpCertificateFile: /etc/tobby/idp-signing.pem  # facultatif avec un fichier
    certFile: /etc/tobby/saml-sp.crt
    keyFile: /etc/tobby/saml-sp.key                 # secret : hors du store
    groupsAttribute: groups
    clockSkew: 90s                                  # au plus 5m
    roles:
      viewer:   [tobby-viewers]
      operator: [tobby-operators]
      admin:    [tobby-admins]
```

**Épingler, pas télécharger.** Récupérez une fois les métadonnées du
fournisseur, vérifiez-les, et pointez `idpMetadataFile` dessus : le
certificat de signature qu'elles portent est la seule clé contre laquelle
les assertions sont vérifiées. Certains fournisseurs — Keycloak entre
autres — ne signent pas leur propre descripteur : un descripteur obtenu
par le réseau ne prouve donc rien sur cette clé. `idpMetadataURL` existe
pour les fournisseurs qui font tourner leurs points d'accès, mais c'est
une connexion sortante que vous choisissez explicitement (NFR-019), et
elle **exige** `idpCertificateFile` : le document téléchargé peut alors
mettre à jour les points d'accès, jamais la clé.

Ce que Tobby vérifie, et refuse sinon avec **TBY-AUTH-015** :

- **initié par Tobby uniquement** : la réponse doit répondre à une requête
  envoyée par cette instance (`InResponseTo`) et revenir avec son
  `RelayState` opaque ; une réponse non sollicitée, initiée par le
  fournisseur, est refusée avant toute lecture du XML ;
- la signature XML, contre le certificat épinglé (SHA-1 refusé) ;
- `Audience` (l'entity ID), `Destination` (toujours exigée) et `Recipient`
  (l'URL de l'ACS), `Issuer` (l'entity ID du fournisseur) ;
- la fenêtre de validité, avec `clockSkew` de tolérance dans les deux sens ;
- le **rejeu** : chaque identifiant d'assertion est retenu tant qu'il
  pourrait être accepté, et un second usage est refusé ;
- le corps est borné (256 Kio) avant toute analyse.

La réponse est un POST inter-site, qui ne porte aucun cookie
`SameSite=Lax`. Tobby la vérifie sur `/auth/saml/acs`, puis envoie le
navigateur sur `/auth/saml/finish`, une simple navigation qui porte le
cookie de liaison : la session ne s'ouvre qu'à ce moment.

## Correspondance groupes → rôles

`roles` liste, par rôle, les noms de groupes (valeurs du claim OIDC,
valeurs d'attribut SAML) qui l'accordent — égalité exacte. Une personne
membre de plusieurs groupes reçoit le rôle **le plus élevé**. Une personne
membre d'**aucun** groupe listé est refusée avec **TBY-AUTH-017** et
n'obtient pas de session : il n'y a pas de rôle par défaut. Une section de
fournisseur dont la correspondance est vide est refusée au démarrage.

Le rôle est évalué à chaque connexion ; un changement de groupe chez le
fournisseur s'applique à la suivante. Les sessions en cours gardent leur
rôle jusqu'à leur fin.

## Sessions et jetons bornés

Ces réglages valent pour toutes les méthodes (R-14) :

| Clé | Défaut | Effet |
| --- | --- | --- |
| `auth.session.idleTimeout` | `30m` | Une session sans requête pendant cette durée prend fin ; toute requête la renouvelle. La page suivante affiche **TBY-AUTH-005** et y revient après connexion. |
| `auth.sessionTTL` | `12h` | Durée de vie absolue de la session : l'activité ne la prolonge jamais. |
| `auth.tokens.defaultLifetime` | aucune | Expiration donnée aux jetons d'API créés sans. Un jeton peut aussi recevoir la sienne (durée en jours sur `/admin/accounts`, `expires` dans `POST /api/v1/tokens`). |

Un jeton expiré est refusé avec **TBY-AUTH-013** — distinct d'un jeton
faux ou révoqué. Le journal d'audit consigne `session.expire` (cible
`idle` ou `lifetime`) et, à chaque refus d'un jeton expiré, `token.expire`
au nom du jeton. Renouvelez les jetons par recouvrement : créez le
remplaçant, basculez les clients, révoquez ou laissez expirer l'ancien.

## Quand le fournisseur d'identité est en panne

La fédération ne devient jamais un point de défaillance unique (R-20,
FR-078) :

- L'instance **démarre** que le fournisseur réponde ou non : rien au
  démarrage ne le contacte.
- Une sonde en arrière-plan surveille le fournisseur (découverte OIDC ;
  l'URL de métadonnées en SAML quand elle est configurée). Tant qu'il ne
  répond pas, la page de connexion l'annonce avec **TBY-AUTH-016** en
  nommant le fournisseur, et garde le formulaire local. Le bouton reste :
  il réessaie.
- Les **sessions existantes** continuent ; les **jetons statiques**
  continuent d'autoriser `/api/v1`, `/v2/` et `/files/` ; les **comptes
  locaux** se connectent.
- Chaque connexion par compte local alors qu'une méthode fédérée est
  activée est consignée en `auth.local_under_federation` — sur le
  formulaire comme sur les surfaces machine — en plus de l'événement
  ordinaire : l'usage de secours se filtre en une fois.
  `tobby_auth_idp_reachable` et
  `tobby_auth_local_logins_under_federation_total` donnent la même image à
  la supervision ([métriques](../../reference/metrics-logs/)).
- La connexion fédérée revient **sans redémarrage** dès que le fournisseur
  répond.

En SAML avec métadonnées épinglées, Tobby n'appelle jamais le fournisseur :
une panne se joue entre le navigateur et le fournisseur, l'instance n'est
pas touchée.

**Gardez un administrateur local.** Sur une instance fédérée, le compte
local `admin` est l'identifiant de secours : conservez son mot de passe
comme tel, et surveillez les événements `auth.local_under_federation`.

## Secrets

`auth.oidc.clientSecretFile` et `auth.saml.keyFile` sont des identifiants
de l'instance auprès du fournisseur. Comme tout chemin secret, ils sont
refusés au démarrage s'ils se résolvent à l'intérieur du store
transportable ([secrets](../secrets/)).

## Dépannage

| Code | Signification | Où regarder d'abord |
| --- | --- | --- |
| TBY-AUTH-014 | La réponse OIDC a échoué à un contrôle | le champ `check` de la ligne de journal « federated sign-in refused » : `state` ou `binding` (connexion lancée dans un autre onglet ou navigateur), `nonce`, `id-token` (signature, issuer, audience, expiration), `token-exchange` |
| TBY-AUTH-015 | La réponse SAML a été refusée | même ligne : `in-response-to`, `signature`, `audience`, `destination`, `issuer`, `clock`, `replay` |
| TBY-AUTH-016 | Le fournisseur est injoignable | réglages proxy et AC privée, disponibilité du fournisseur ; la connexion locale fonctionne pendant ce temps |
| TBY-AUTH-017 | Connecté, mais sans rôle | les groupes de la personne face à `roles` ; les noms de groupes sont sensibles à la casse |

Voir la [référence des erreurs](../../reference/errors/) pour tous les codes.
