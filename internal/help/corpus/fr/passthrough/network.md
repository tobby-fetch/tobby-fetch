---
title: "Réseau d'entreprise : proxy, PKI, TLS"
description: Proxys sortants authentifiés, autorités de certification privées sans désactiver TLS, et le certificat que l'instance sert elle-même.
sidebar:
  order: 3
---

Une instance passthrough vit d'ordinaire dans une zone segmentée où la
sortie directe est jetée, pas refusée : une récupération mal configurée
n'échoue pas, elle reste suspendue jusqu'à son délai d'expiration. Tobby
est bâti pour cet environnement. Il n'a **qu'un seul transport sortant**,
partagé par tous les chemins qui quittent le processus — le moteur de
recipes, les imports ponctuels, les dépôts de charts Helm, le document
Retriever, les trust roots récupérées par URL, et les pushes vers la
destination. Configurez-le une fois et tous les chemins l'empruntent ;
un test de la suite prouve qu'aucun chemin sortant ne le contourne
(FR-080).

## Proxy authentifié

```yaml
network:
  proxy:
    url: http://proxy.example.com:3128
    noProxy:
      - .internal.example.com
      - 10.0.0.0/8
    username: tobby
```

- `network.proxy.url` — le proxy sortant. Un proxy en `https://` est
  accepté ; le saut vers le proxy est alors lui-même en TLS, vérifié
  comme toute autre connexion. `--proxy-url` existe comme drapeau pour
  l'URL.
- `network.proxy.httpsURL` — uniquement quand les destinations `https://`
  empruntent une autre route ; vide signifie que `url` sert les deux.
- `network.proxy.noProxy` — les destinations jointes en direct : un hôte,
  un `.suffixe`, un bloc CIDR, ou `*`. Le registre de zone et la boucle
  locale de l'instance y ont généralement leur place.

**Le mot de passe ne passe jamais par une ligne de commande.** Il n'y a
délibérément aucun drapeau pour lui — une valeur de drapeau est lisible
dans la table des processus et dans l'historique du shell (NFR-015). Il
arrive par le fichier de configuration (`network.proxy.password`) ou par
l'environnement :

```yaml
# Kubernetes : depuis un Secret que vous gérez déjà
env:
  - name: TOBBY_NETWORK_PROXY_PASSWORD
    valueFrom:
      secretKeyRef: {name: tobby-proxy, key: password}
```

Quel que soit son chemin d'arrivée, il est masqué dans chaque
enregistrement de journal, dans les messages d'erreur, et dans
`tobby config dump` — le masquage est une propriété du type qui porte le
secret, pas du code qui l'imprime.

## Autorités de certification privées — sans désactiver TLS

Un registre ou un proxy derrière une PKI interne devient joignable en
ajoutant son autorité au magasin de confiance sortant (FR-081) :

```yaml
network:
  tls:
    caFiles:
      - /etc/tobby-ca/internal-root.pem
```

- `network.tls.caFiles` — chemins vers des bundles PEM.
- `network.tls.ca` — la même chose, en ligne, pour les déploiements qui
  injectent la configuration mais ne peuvent pas monter de fichier.
- `network.tls.exclusiveTrust` — retire le magasin de racines publiques
  de l'hôte, ne laissant que vos autorités. Il ne fait jamais que
  restreindre la confiance.

Il n'existe **aucun réglage, nulle part dans Tobby, qui désactive la
vérification des certificats**. FR-081 demande que des autorités privées
soient reconnues, pas que le contrôle soit abandonné — une CA privée
authentifie toujours le pair ; un contrôle désactivé n'authentifie rien.
Le réglage voisin `registries.insecure` répond à une autre question
(« cet hôte nommé parle en HTTP clair »), reste par hôte et explicite, et
devient inutile une fois l'autorité configurée.

Le même magasin de confiance sert tous les bords sortants : registres,
dépôts de charts, source du Retriever, URLs de trust roots, et le saut
TLS vers le proxy lui-même.

## TLS serveur : ce que l'instance présente

Un seul listener porte l'interface, l'API et le registre embarqué, donc
un seul certificat couvre les trois — aucune surface ne peut être
laissée en clair par accident (FR-082). Derrière un ingress ou un reverse
proxy qui termine TLS, vous pouvez laisser cela désactivé ; posez-y
`server.secureCookies: true` pour que les cookies de session soient
marqués `Secure` même si le listener ne voit que du HTTP clair. Quand
rien ne termine devant, l'instance sert TLS elle-même.

### Avec un certificat que vous fournissez

```yaml
server:
  tls:
    certFile: /etc/tobby-tls/tls.crt
    keyFile: /etc/tobby-tls/tls.key
```

Fournir la paire vaut activation. Les fichiers sont relus dès qu'ils
changent sur le disque, si bien qu'une rotation (cert-manager qui
remplace un Secret monté, une cron qui dépose une paire renouvelée) est
prise en compte à la poignée de main suivante, sans redémarrage. Les
drapeaux `--tls-cert-file` / `--tls-key-file` sont équivalents.

### Repli auto-signé, avec empreinte journalisée

Avec `server.tls.enabled: true` et aucune paire, Tobby génère un
certificat auto-signé — émis pour les noms de boucle locale, le nom
d'hôte de la machine, et tous les noms listés dans `server.tls.hosts` —
et journalise son empreinte SHA-256 au démarrage :

```json
{"level":"info","msg":"serving TLS","self_signed":true,
 "fingerprint_sha256":"A1:B2:…","requirement":"FR-082"}
```

Diffusez cette empreinte hors bande et comparez-la à ce que le client a
vu avant de lui faire confiance. La paire générée est persistée sous
`state.root/tls/`, si bien que l'empreinte survit aux redémarrages : un
opérateur qui l'a épinglée reste dans le vrai.

### Remplacer le certificat servi depuis l'interface

L'écran d'administration **Réseau** (`/admin/network`, rôle admin ;
reflété par `GET /api/v1/network` et `PUT /api/v1/network/certificate`)
montre ce que le listener présente réellement — empreinte, SANs,
validité — et signale un repli auto-signé pour la posture dégradée qu'il
est. Le remplacement prend des fichiers, jamais des champs de texte
collés, et la clé privée n'est renvoyée sous aucune forme — ni ses
octets, ni sa longueur, ni un digest. L'écran relit la paire servie après
écriture : ce qu'il confirme est ce que le listener a adopté, pas ce qui
a été soumis.

Une instance démarrée sur le repli généré n'a aucun chemin de certificat
configuré à écraser. Depuis la v0.4.1, l'écran propose une destination
dans le répertoire d'état — le seul endroit où une clé privée peut vivre
— à côté de la paire générée plutôt que par-dessus, et l'adoption est une
étape explicite, distincte de l'écriture des fichiers. Chaque
remplacement, et chaque tentative refusée, est audité avec l'empreinte du
certificat comme cible (FR-094).

![L'écran d'administration Réseau : la posture TLS avec l'empreinte du certificat et le formulaire de remplacement](../../../../../assets/docs/admin-network.png)

Suite : donnez à l'instance quelque chose à promouvoir —
[le Retriever de zone et la cascade](../../passthrough/retriever-cascade/).
