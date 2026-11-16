---
title: Journal d'audit
description: Le schéma d'audit versionné à six champs, le catalogue d'événements daté par version livrée, les garanties de durabilité et la manière d'ingérer la piste dans un SIEM.
sidebar:
  order: 5
---

Tobby émet une catégorie dédiée d'événements d'audit de sécurité, avec un
schéma stable et versionné (FR-094 — livré en v0.1.x, catalogue enrichi à
chaque jalon). Les événements d'audit sont des enregistrements de journal JSON
structurés ordinaires, sur les canaux de journalisation habituels, que l'on
sépare des journaux opérationnels grâce à un seul champ marqueur — pas de
second pipeline à déployer, rien de plus qui puisse tomber en panne.

## Le schéma

Six champs sur chaque enregistrement, plus le marqueur et la version du
schéma :

```json
{
  "ts": "2026-08-22T09:14:03Z",
  "level": "INFO",
  "msg": "token.create",
  "log_type": "audit",
  "audit_schema": 1,
  "actor": "alice",
  "action": "token.create",
  "target": "ci-deploy",
  "outcome": "success",
  "origin": "10.20.30.40:52114"
}
```

- **actor** — l'identité authentifiée, `local` pour la CLI côté hôte et le
  cycle de vie du processus, ou le contexte anonyme explicite sous la
  dérogation FR-075.
- **action** — tirée d'un catalogue fermé et documenté (ci-dessous).
- **target** — ce à quoi l'action s'est appliquée : un compte, un nom de
  token, un dépôt, une entrée de configuration.
- **outcome** — `success`, `failure` ou `denied` (bloqué par la politique ou
  l'autorisation).
- **timestamp** (`ts`) et **origin** — l'adresse réseau du client, ou `local`.
  Les en-têtes de transfert ne sont volontairement pas crus pour l'origine :
  ils sont fournis par le client.

Deux champs facultatifs s'y ajoutent quand ils s'appliquent : `actor_name`,
le nom lisible d'une identité fédérée, et `setting`, la clé de configuration
qui a décidé de l'issue d'un événement `hardening.refused`.

Le schéma est versionné par `audit_schema` et n'évolue que par ajout, avec la
même discipline de compatibilité que l'API REST
([publication et compatibilité](../../project/release-compatibility/)).

## Catalogue d'événements, daté par version livrée

| Action | Signification | Depuis |
| --- | --- | --- |
| `instance.start`, `instance.stop` | cycle de vie du processus | v0.1.x |
| `auth.override_active` | démarrage alors que `auth.disabled` est positionné (FR-075) | v0.4.x |
| `account.create`, `account.password_change`, `account.delete`, `account.role_change` | cycle de vie des comptes — CLI hôte, libre-service, surfaces d'administration | v0.2.x–v0.4.x |
| `session.login`, `session.logout` | sessions interactives de l'interface — locales et, depuis v0.6.x, fédérées (`target` vaut `oidc` ou `saml` ; `actor` est la clé d'identité `oidc:<sub>` / `saml:<NameID>`, avec le `actor_name` lisible à côté) | v0.4.x |
| `auth.authenticate` | une vérification d'identifiant sur une surface machine (`/v2/`, `/api/v1`, `/files/` depuis v0.6.x) | v0.4.x |
| `ui.access`, `api.access`, `registry.access` | refus d'autorisation, par surface — les refus uniquement, par conception : le trafic permis relève du journal opérationnel | v0.4.x |
| `token.create`, `token.revoke` | cycle de vie des tokens statiques (FR-072) | v0.4.x |
| `import.create`, `sync.create`, `recipe.publish`, `content.delete` | actions qui touchent au contenu, avec leur acteur | v0.4.x |
| `config.promotion_interval`, `config.server_certificate` | modifications sensibles de la configuration d'une instance en cours d'exécution (FR-013, FR-082) | v0.4.x |
| `media.import` | un support transporté poussé dans la zone (FR-052) | v0.5.x |
| `media.override` | l'une des deux gardes de média dont on peut s'affranchir, tentée ou appliquée (FR-054) | v0.5.x |
| `store.reset` | la réinitialisation confirmée du store, y compris une confirmation refusée (FR-046) | v0.5.x |
| `layout.export`, `layout.import` | un layout d'image OCI qui quitte ou entre dans le store (FR-051) | v0.5.x |
| `fileset.pack` | un répertoire de l'hôte empaqueté dans le store comme FileSet (FR-048) | v0.5.x |
| `prune.active` | démarrage alors que le prune aligné sur le retriever est actif (FR-045) | v0.5.x |
| `store.verify` | une vérification du store à la demande terminée (FR-057) : le demandeur, le store et le verdict comme issue — `success` pour un store sain, `failure` quand des défauts ont été trouvés. Écrit à la fin de la vérification — y compris quand elle échoue ou est annulée, en `failure` — avec l'origine de la requête (une adresse client, ou `local` pour `tobby store verify`) | v0.6.x |
| `session.expire` | une session de l'interface terminée par expiration ; la cible est la raison, `idle` (`auth.session.idleTimeout`) ou `lifetime` (`auth.sessionTTL`) (R-14) | v0.6.x |
| `token.expire` | chaque refus d'un token échu, sous le nom du token (R-14) ; le refus d'un token révoqué est un échec `auth.authenticate` sous son nom | v0.6.x |
| `auth.local_under_federation` | une connexion par compte local alors que OIDC ou SAML est activé — usage de secours (break-glass), gardé filtrable à part ; cible `ui` ou la surface machine (FR-078, R-20) | v0.6.x |
| `scan.block` | un élément refusé par la politique de vulnérabilités — violation, non analysé sous une politique bloquante, ou base obsolète sous l'action block (FR-031) ; la cible est le `reference@digest` de l'élément, l'issue `denied` | v0.6.x |
| `hardening.refused` | une écriture refusée par un interrupteur de durcissement (FR-098) : un import unitaire ou son inspection, un empaquetage de FileSet (écran, API ou `tobby fileset pack`), ou une écriture sur `/v2/` — upload de blob, montage, push de manifeste, suppression — par tout client et tout rôle. La cible est la référence, la FileSet empaquetée ou le dépôt ; l'issue `denied` ; le champ supplémentaire `setting` nomme l'interrupteur (`hardening.disableUnitImport` ou `hardening.disableRegistryPush`). Un refus, un enregistrement : aucun second refus n'est écrit à côté | v1.0.x |
| `scan.scanner.repaired` | un binaire d'analyseur livré par tobby-system, trouvé modifié sur le disque depuis sa vérification et dépaqueté à nouveau depuis la livraison vérifiée, sans avoir été exécuté (FR-031) ; la cible est le chemin du binaire, l'issue `success`, ou `failure` quand la livraison n'a pas pu être dépaquetée à nouveau (l'analyseur est alors indisponible). Un enregistrement WARN à côté nomme les digests attendu et trouvé | v0.6.x |

Les événements de média ont rejoint le catalogue avec la v0.5.0 : les deux
dérogations auditées d'un import de média — un support adressé à une autre zone
et un support plus ancien que le dernier importé ici — et la réinitialisation
confirmée du store (FR-046). La tentative comme la dérogation appliquée sont
consignées, avec l'acteur et l'origine réseau dont elles viennent.

## Durabilité — et la piste qui traverse l'air gap

En mode passthrough, les enregistrements d'audit vont sur la sortie standard
avec tout le reste (FR-090) : la durabilité est celle de votre collecteur de
journaux, ce qui est sa place pour un service de longue durée.

En mode mirror, le fichier de journal vit **sur le support de transport**, sous
`_tobby/logs/` et donc hors de la couverture du manifeste du média — un journal
écrit dans la couverture invaliderait, ligne après ligne, l'inventaire que la
destination vérifie. Il a une rotation par taille et un fsync explicite aux
frontières de tâche : un support arraché perd au plus la tâche en cours
(FR-053, FR-056), ce qu'un test prouve en tuant le processus net. Ce fichier
n'est pas un confort : c'est la piste d'audit qui franchit physiquement l'air
gap avec le contenu, porteuse de l'identifiant de run que l'instance de
destination réutilise, de sorte qu'une synchronisation se retrace du côté
connecté au côté isolé à travers le support (FR-090). Voir
[tracer un transfert](../../air-gap/traceability/).

## La position sur l'absence de signature

Le journal d'audit **n'est pas signé**. C'est une position assumée, pas un
oubli (FR-094) : Tobby ne signe rien, et une signature produite par le même
processus que celui qui écrit les enregistrements n'attesterait rien qu'un
attaquant maîtrisant ce processus ne pourrait aussi forger. La piste est une
preuve opérationnelle ; l'ancre de confiance du *contenu* est, et reste, la
vérification cosign contre les racines de confiance de la destination
([confiance du contenu](../../security/content-trust/)).

Mesures compensatoires, avec leurs limites : envoyer le flux de la sortie
standard vers votre SIEM en quasi temps réel, pour qu'une altération après coup
exige de compromettre aussi le collecteur ; sur les supports, la discipline du
fsync aux frontières de tâche borne ce qu'un plantage peut perdre ; chaque
enregistrement est corrélé par l'identifiant de run, de sorte qu'un trou se voit
comme un trou. Si votre homologation exige des journaux à preuve d'altération,
cette propriété doit venir de votre plateforme de journalisation — le rôle de
Tobby est de lui fournir un flux stable, complet et analysable.

## Ingestion dans un SIEM

Toute la piste est à un filtre de distance — pas d'expressions régulières, pas
de format à deviner :

```sh
# La piste de sécurité extraite d'un flux de journaux mêlés
kubectl logs deploy/tobby | jq -c 'select(.log_type == "audit")'

# Seulement les événements en échec ou refusés
jq -c 'select(.log_type == "audit" and .outcome != "success")'
```

Pointez votre collecteur sur la sortie standard du conteneur (passthrough),
associez directement les six champs — ils sont stables sur toute la série 1.x —
et alertez au minimum sur : les rafales de `outcome == "denied"` par `origin`,
`auth.override_active`, toute action `config.*`, et les actions `account.*` ou
`token.*` en dehors des fenêtres de changement. Le jeu de clés est petit et
fixe à dessein : un schéma d'audit qu'il faut analyser avec imagination est un
schéma que l'on analysera mal.
