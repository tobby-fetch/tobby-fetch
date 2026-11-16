---
title: Secrets
description: Tous les secrets que détient une instance Tobby, où ils résident, comment ils sont stockés et pourquoi ils ne peuvent pas fuir par les journaux, les erreurs ou les dumps de configuration.
sidebar:
  order: 6
---

Un auditeur doit pouvoir énumérer chaque secret détenu par une instance et
vérifier où chacun réside. Cette page est cet inventaire — exhaustif en v0.5.x
— suivi des mécanismes qui tiennent les secrets hors de tous les canaux de
sortie.

## Inventaire

| Secret | Où il réside | Forme au repos |
| --- | --- | --- |
| Mots de passe des comptes | racine d'état (`state.root`) | uniquement des hashes argon2id salés (FR-073, NFR-015) |
| Secrets des tokens d'API | racine d'état | uniquement des hashes ; le secret en clair n'est montré qu'une fois, à la création (FR-072) |
| Identifiants de registry | le chemin `registries.credentialsFile` (format `dockerconfigjson`, FR-004), choisi par l'opérateur | tel que fourni ; à garder hors du store |
| Mot de passe du proxy | fichier de configuration ou `TOBBY_NETWORK_PROXY_PASSWORD` (FR-080) | jamais accepté en flag — les flags sont lisibles dans la table des processus |
| Clé privée TLS | le chemin `server.tls.keyFile` (FR-082) | fichier PEM ; jamais renvoyée par aucune surface, sous aucune forme |
| Secret client OIDC | le chemin `auth.oidc.clientSecretFile` (FR-070) | tel que fourni ; jamais dans le fichier de configuration lui-même |
| Clé du fournisseur de service SAML | le chemin `auth.saml.keyFile` (FR-071) | fichier PEM ; seul son certificat est publié, dans le descripteur du SP |
| Matériel de session de l'interface | mémoire uniquement | un redémarrage déconnecte tout le monde ; les cookies portent `Secure`, `HttpOnly`, `SameSite` (NFR-015) |

Deux choses sont volontairement *absentes* de cet inventaire. Les racines de
confiance sont des clés publiques — de la configuration, pas des secrets
([confiance du contenu](../../security/content-trust/)). Et il n'y a pas de clé
de signature, car Tobby ne signe rien, dans aucun mode, jamais (ADR-0007).

**La racine d'état, jamais le store.** Le store (`storage.root`) est autonome
et fait pour voyager — c'est tout l'objet de FR-050. L'identité vit donc
strictement à part : les comptes, les tokens et l'état de l'instance
appartiennent à la racine d'état, et le fichier d'identifiants doit lui aussi se
trouver hors du store. Le chart Helm de référence monte les deux comme des
volumes distincts et refuse de s'afficher s'ils pointent vers le même chemin.

**La séparation est appliquée, pas seulement documentée.** Une instance
**refuse de démarrer** (`TBY-CFG-002`) lorsqu'un chemin de secret configuré se
résout à l'intérieur du store : `state.root`, `registries.credentialsFile`,
`server.tls.keyFile` et — quand leur méthode est activée —
`auth.oidc.clientSecretFile` et `auth.saml.keyFile`. Le contrôle résout à
travers le système de fichiers réel — chemins relatifs, `..` et liens
symboliques compris — de sorte qu'un chemin qui se lit « à l'extérieur » et
atterrit à l'intérieur est attrapé, et le refus nomme à la fois le réglage et le
chemin vers lequel il s'est résolu (NFR-020).

Le mot de passe du proxy n'a pas de forme fichier et donc pas de chemin à
contrôler : c'est une valeur de la configuration qui ne peut pas se sérialiser
elle-même ([masquage par construction](#masquage-par-construction)). Les racines
de confiance et les bundles de CA sont des clés publiques, pas des secrets, et
sont volontairement autorisés n'importe où.

### Permissions des fichiers créés par Tobby

| Fichier | Unix | Windows |
|---|---|---|
| `accounts.yaml` (comptes, digests de tokens) | `0600`, dans un répertoire `0700` | liste d'accès n'accordant que le compte propriétaire |
| Clé TLS auto-signée générée | `0600` | liste d'accès n'accordant que le compte propriétaire |
| Clé privée TLS remplacée | `0600` | liste d'accès n'accordant que le compte propriétaire |
| **Certificat** TLS remplacé | conserve le mode qu'il avait (public par construction) | hérité |

Sous Windows, le mode Unix ne garantit rien : `chmod` y ramène le bit
d'écriture à l'attribut lecture seule et ignore le reste, de sorte qu'un fichier
« créé en 0600 » serait lisible par tous les comptes qu'admet la liste d'accès
héritée. Tobby remplace entièrement la liste de contrôle d'accès discrétionnaire
du fichier par une entrée unique nommant son propre propriétaire, et la marque
protégée pour que les entrées héritables du répertoire parent ne soient pas
refusionnées. Les deux systèmes d'exploitation sont dans le périmètre validé
(NFR-018).

Une clé privée dont un opérateur avait un jour relâché les droits redevient
réservée au propriétaire au remplacement suivant. Le certificat qui l'accompagne
ne le fait pas : il est public par construction, et un déploiement qui l'a
publié à dessein ne doit pas le perdre silencieusement.

## Les hashes sont calculés par l'outil

Aucune surface — CLI, interface ou API — n'accepte un hash de mot de passe
précalculé. `tobby user add`, `tobby user passwd`, les écrans de comptes et
l'API prennent tous un mot de passe en clair par le canal authentifié et
dérivent le hash argon2id en interne (FR-066). Cela ferme le raccourci du
pass-the-hash et garantit que les paramètres de hachage sont ceux de l'outil,
et non ceux qu'un script aurait produits.

## Masquage par construction

NFR-015 exige que les secrets n'apparaissent jamais dans les journaux, les
messages d'erreur, les réponses d'API ni les dumps de configuration. Tobby le
met en œuvre dans le système de types plutôt que par discipline de relecture :
les valeurs de configuration sensibles sont d'un type `Secret` dédié, dont
chaque chemin de sérialisation — formatage en chaîne, YAML, JSON, verbes de
débogage — produit `REDACTED` au lieu de la valeur. Le code qui a légitimement
besoin de la valeur doit appeler un unique accesseur, facile à retrouver par
grep. Qu'un secret atteigne un canal de sortie est donc un acte visible à la
compilation, pas un oubli.

Concrètement :

- `tobby config dump` affiche la configuration effective avec les secrets
  masqués par construction (FR-003) — sans risque à joindre à un ticket de
  support.
- Les identifiants du proxy n'apparaissent jamais dans les journaux ni les
  erreurs (critère d'acceptation de FR-080), et les écrans réseau signalent leur
  présence par un booléen, jamais par une valeur.
- La clé privée TLS n'est renvoyée par rien : ni ses octets, ni sa longueur, ni
  un digest (un digest serait un oracle stable face à une clé candidate). Le
  certificat — public par construction — est ce que rapportent les surfaces
  d'administration.
- L'acceptation de NFR-015 est une analyse des journaux et des réponses de toute
  la suite e2e avec des secrets connus plantés : zéro occurrence, et les chemins
  de masquage sont testés unitairement.

## Quoi sauvegarder, quoi protéger

Protégez la racine d'état comme n'importe quel magasin d'identifiants : elle
contient les hashes et l'identité de l'instance, et c'est l'unique cible de
sauvegarde. Le store contient du contenu public vérifié et des journaux
d'opérations — précieux, mais pas secrets.

:::note[À venir — jalon 7]
Un **bundle de diagnostic** en une commande — version, configuration expurgée,
journaux, rapports, résultats d'intégrité, l'expurgation elle-même étant testée
— arrive avec le jalon 7 (R-30). D'ici là, `tobby config dump` et vos extraits de
journaux en sont les équivalents sûrs.
:::
