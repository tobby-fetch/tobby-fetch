---
title: Validation des entrées
description: Chaque entrée qui franchit une frontière de confiance, avec sa borne de taille, son décodeur et son contrôle de grammaire — corps de requête, paramètres de chemin et de requête, configuration, documents lus dans un store ou sur un support, réponses des registries et des fournisseurs d'identité (NFR-023).
sidebar:
  order: 9
---

Tobby lit des entrées venant de parties qu'il ne contrôle pas : navigateurs
et clients d'API, fournisseur d'identité, registries amont, et un support
qu'une autre personne a transporté à travers la coupure réseau. Chaque
entrée suit trois règles (NFR-023) :

1. **Bornée avant d'être analysée.** Une limite de taille s'applique avant
   qu'un décodeur voie l'entrée : aucune entrée ne peut faire mettre en
   mémoire à l'instance une quantité non bornée.
2. **Décodée strictement.** Les champs inconnus sont refusés partout où le
   format le permet. Une option mal orthographiée est une erreur, pas une
   valeur par défaut silencieuse.
3. **Contrôlée selon sa grammaire** avant d'atteindre un chemin du système
   de fichiers, un argument de processus ou un enregistrement de journal.
   Les grammaires sont celles des références et digests OCI, des chemins de
   dépôt, des identifiants et des horodatages.

Un refus porte un [code de la taxonomie](../../reference/errors/). Il ne
porte jamais de trace d'appel.

Cette page consigne la revue, point d'entrée par point d'entrée.
« Strict » signifie que les champs inconnus sont refusés.

## L'écouteur HTTP

| Entrée | Borne | Décodeur | Contrôle de grammaire | Refus |
|---|---|---|---|---|
| Ligne de requête et en-têtes (toutes les surfaces) | 1 Mio d'en-têtes, 10 s pour les envoyer | net/http | par surface, ci-dessous | `431` / connexion fermée |
| Corps de formulaire de l'interface (toutes les routes) | 16 Mio par requête. Les formulaires encodés en URL sont aussi plafonnés à 10 Mio par net/http. L'envoi de certificat garde 2 Mio en mémoire et 1 Mio par document PEM | formulaire / multipart | par écran | le code de l'écran |
| Corps JSON de l'API (toutes les routes) | 16 Mio par requête : un `Content-Length` déclaré au-delà est refusé sans être lu, et un corps transmis par morceaux est coupé à la borne. Chaque point d'accès applique ensuite sa propre borne (tableau ci-dessous) | JSON, strict, déclaré `Content-Type: application/json` (un paramètre `charset` est accepté). Un corps qui se poursuit après son document est refusé | par point d'accès | `TBY-SRV-004` (413) pour la taille, `TBY-SRV-005` (415) pour tout autre type de média, puis le code du point d'accès |
| Nom d'utilisateur de connexion, nom d'utilisateur Basic | 256 octets, UTF-8 valide, aucun caractère de contrôle, contrôlé **avant** le mot de passe | — | le nom n'est jamais vérifié ni journalisé tel quel ; l'acteur d'audit est `invalid-name` | `TBY-AUTH-002` |
| Cibles de redirection `next` / `back` | URL | — | doit être un chemin local (`/`, pas `//`, pas de `\`, aucun caractère de contrôle) ; sinon la cible est `/` | — |
| Requêtes qui modifient l'état, authentifiées par le cookie de session | — | — | jeton anti-falsification et contrôle d'origine (NFR-012) | `TBY-AUTH-004` |

Bornes de corps par point d'accès :

| Point d'accès | Borne |
|---|---|
| `POST /api/v1/accounts`, `PATCH /api/v1/accounts/{name}`, `POST /api/v1/tokens`, `POST /api/v1/account/password` | 8 Kio |
| `POST /api/v1/filesets/pack` | 8 Kio |
| `POST /api/v1/sync`, `PUT /api/v1/retriever/interval`, `POST /api/v1/media/verify`, `POST /api/v1/media/import` | 4 Kio |
| `POST /api/v1/import` | 64 Kio |
| `POST /api/v1/oci-layout/{plan,export,import}`, `POST /api/v1/store/reset` | 64 Kio |
| `POST /api/v1/plan` | 256 Kio |
| `PUT /api/v1/network/certificate` | 2 Mio + 8 Kio (deux documents PEM) |
| `POST /api/v1/recipes/publish` | 4 Mio + 64 Kio (la borne des documents de cookbook) |

## Paramètres de chemin et de requête

| Entrée | Contrôle de grammaire | Refus |
|---|---|---|
| Chemin de dépôt (`/content/{repo...}`, API `/content/{repo}`) | nom de dépôt OCI (`reference.WithName`) ; un tag ne contient pas de `/` | `TBY-SRV-002` |
| Suppression de contenu | le dépôt doit être un import unitaire enregistré avant toute suppression | `TBY-POL-*` |
| Identifiant de tâche (`/tasks/{id}`, journaux) | recherché dans la file en mémoire avant de nommer un fichier | `TBY-TSK-001` |
| Suivi du journal d'une tâche (`?after=`) | curseur entier ; au plus 1 Mio par appel, le curseur porte la suite | `TBY-STO-001` |
| Noms de compte et de jeton (chemin et corps) | recherchés dans le magasin de comptes ; `:` refusé à la création | `TBY-AUTH-008`, `TBY-AUTH-010` |
| Nom de recipe (`/recipes/{recipe}/mapping`) | recherché parmi les recipes enregistrées | `TBY-SRV-002` |
| Noms de page d'aide et de capture d'écran | recherchés dans l'ensemble embarqué ; jamais un chemin de fichier | `TBY-SRV-002` |
| `format`, `lang` du bordereau de transfert (interface et API) | ensembles fermés | `TBY-VAL-001` |
| Référence d'import (`reference=`, corps) | grammaire des références OCI (`name.ParseReference`) | `TBY-REG-001` |
| `/files/<set>/<path>` | NUL refusé ; aucun segment vide, `.` ou `..` ; le nom du FileSet est recherché ; le fichier est ouvert par `os.Root` | `400` / `404` nus (les clients de téléchargement lisent des statuts, pas des documents) |
| Noms et digests de `/v2/` | grammaires de noms et de digests du routeur de distribution | documents d'erreur OCI |
| Chemins d'import et d'export de disposition OCI | aucun : un administrateur nomme un chemin de l'hôte à dessein (FR-051) ; consigné au journal d'audit | — |

## Fournisseurs d'identité

| Entrée | Borne | Décodeur | Contrôle | Refus |
|---|---|---|---|---|
| Toute réponse du fournisseur (découverte, JWKS, jeton, métadonnées SAML) | 2 Mio, appliqués sur le transport, sous toutes les bibliothèques. Un corps plus grand échoue ; il n'est jamais tronqué | JSON / XML | — | `TBY-AUTH-014`/`015`/`016` |
| `state`, `code` du retour OIDC | URL | requête | `state` doit correspondre à une connexion en attente et à son cookie de liaison | `TBY-AUTH-014` |
| Jeton d'identité OIDC | borne du point d'accès jeton | JWT | signature, liste d'algorithmes autorisés, émetteur, audience, `azp`, nonce | `TBY-AUTH-014` |
| `SAMLResponse`, `RelayState` SAML | 256 Kio, avant le décodage base64 et à nouveau après | XML (signature vérifiée) | le relais doit correspondre à une connexion en attente avant toute analyse XML ; `Destination` requis | `TBY-AUTH-015` |
| Identité affirmée (`sub` OIDC, NameID SAML) | 255 octets, aucun caractère de contrôle | — | avant qu'elle ne devienne un nom de session ou un acteur d'audit | `TBY-AUTH-014`/`015` |
| Nom affiché, groupes | 128 caractères ; 1 024 groupes | — | caractères de contrôle retirés | — |

## Configuration et état local

| Entrée | Borne | Décodeur | Contrôle |
|---|---|---|---|
| Configuration de l'instance | taille du fichier | YAML, strict | validation complète avant le démarrage (`TBY-CFG-001`) |
| Documents Retriever et Recipe (fichier, URL, OCI) | 4 Mio | YAML via le JSON Schema publié, puis JSON strict | schéma et règles sémantiques (`TBY-VAL-001`) |
| Documents de cookbook | 4 Mio | même analyseur | emplacement de publication |
| Identifiants de registry (`dockerconfigjson`) | taille du fichier | JSON, permissif : le format appartient à la chaîne d'outils Docker | clés d'hôte |
| Enregistrements de tâches (`_tobby/tasks/*.json`) | 4 Mio par enregistrement | JSON | l'identifiant doit être égal au nom du fichier et ne contenir aucun séparateur ; sinon l'enregistrement est ignoré avec un avertissement |
| Fichier des comptes | taille du fichier | YAML | version du schéma |

## Store et support

Un support est un store qui a changé de mains. Tout ce qu'il dit de
lui-même reste une affirmation tant que la destination ne l'a pas haché à
nouveau (FR-054).

| Entrée | Borne | Décodeur | Contrôle de grammaire | Refus |
|---|---|---|---|---|
| Manifeste du support (`meta/media.json`) | 64 Mio | JSON, strict. Un manifeste qui se poursuit après son document est refusé | chemins d'inventaire confinés au store, noms de dépôt, tags, digests | `TBY-MED-002` et suivants |
| Manifestes, liens de tags, blobs de signature lus pendant le parcours | 1 Mio (128 octets pour un lien de tag) | JSON, permissif (OCI autorise les champs inconnus) | digests | `TBY-MED-*` |
| Import de disposition d'image OCI | 1 Mio pour les marqueurs et l'index ; nombre d'entrées plafonné ; gzip refusé | JSON, permissif (OCI) | grammaires de dépôt et de tag, classement des entrées | `TBY-LAY-*` |
| Extraction des couches de FileSet | budgets par fichier, en nombre de fichiers et en profondeur | tar, gzip, zstd | chemins d'entrée nettoyés, aucune évasion, whiteouts contrôlés, bits set-uid retirés, `os.Root` | `TBY-FIL-*` |
| Revérification d'intégrité du store | 1 Mio par manifeste | JSON, permissif (OCI) | digests | `TBY-INT-*` |
| Bundles de signature | 1 Mio de charge utile, sur la taille déclarée et sur la taille réelle | JSON | liaison au digest | `TBY-SIG-*` |
| Sortie de l'analyseur | 128 Mio | JSON, permissif (le schéma propre de l'analyseur), version de schéma 2 | — | `TBY-SCN-*` |

## Registries amont

| Entrée | Borne | Contrôle |
|---|---|---|
| Manifestes et index | 100 Mio (go-containerregistry) | digest |
| Blobs | la taille déclarée ; spool reprenable | digest et taille |
| Index de dépôt Helm, archives de charts | 32 Mio, 256 Mio | structure de l'archive |

## Exemptions et points ouverts

Ils sont énoncés plutôt que cachés :

- **Les envois de blobs vers `/v2/` n'ont pas de borne de taille.** Un push
  transporte par conception des couches de plusieurs gigaoctets. Le rôle
  `operator` le contrôle, et le digest est vérifié quand l'envoi est
  validé.
- **Les chemins de disposition OCI sont des chemins arbitraires de
  l'hôte.** Un administrateur les choisit à dessein (FR-051), et ce choix
  est consigné au journal d'audit.
- **Des décodeurs permissifs** sont conservés là où le format l'exige :
  documents OCI (la spécification autorise les champs inconnus),
  `dockerconfigjson`, le rapport de l'analyseur et les index Helm.
- **Points ouverts de cette revue**, suivis pour une version ultérieure :
  - Le fichier de configuration n'est pas borné en taille, et un second
    document YAML qu'il contiendrait est ignoré.
  - Un blob de configuration d'image n'est borné que par la taille que
    déclare son manifeste.
  - `Chart.yaml` est tronqué à 1 Mio au lieu d'être refusé.
  - Les noms de compte ne refusent que `:`.
  - Le fichier des comptes est décodé de façon permissive.
  - Certains refus reprennent le texte de l'erreur sous-jacente dans leur
    paramètre de détail.
