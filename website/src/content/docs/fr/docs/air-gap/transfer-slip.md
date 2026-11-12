---
title: Le bordereau de transfert
description: Le compte rendu imprimable et bilingue d'un transfert physique — support, fraîcheur, inventaire, vérification, analyse de vulnérabilités et marqueurs d'assurance — en HTML ou en texte brut, depuis l'écran Média, l'API ou la ligne de commande, et pourquoi c'est une aide non signée et jamais une preuve.
sidebar:
  order: 9
---

Un disque passe entre des personnes qui ne partagent pas d'écran : celle qui
l'a écrit, celle qui le transporte, celle qui l'importe, et quiconque doit
valider le transfert. Le **bordereau de transfert** est la page qu'elles
peuvent tenir en main. Il reprend ce que l'[écran Média](../../air-gap/import-destination/)
affiche déjà, dans un seul document imprimable, en français ou en anglais.

Il ne décide rien et n'ajoute aucun comportement. Chaque ligne est lue dans un
enregistrement que Tobby tient déjà — le manifeste, le dernier rapport de
vérification, les rapports d'analyse des documents de tâche, les marqueurs
d'assurance — et établir un bordereau **ne calcule aucune empreinte et ne
lance aucune vérification**.

## Ce qu'il n'est pas

Chaque bordereau s'ouvre et se ferme sur le même avertissement, dans la langue
du bordereau :

> **Document d'aide non signé — ne vaut pas preuve d'authenticité.**

Tobby ne détient aucune clé privée et ne signe rien ([ADR-0006, ADR-0007](../../reference/srs-adr/)).
Le bordereau ne porte aucune signature, pas plus que le manifeste qu'il
résume : cet inventaire est une **allégation non signée** tant que la
vérification n'a pas recalculé les empreintes du support. L'authenticité vient
des signatures des recipes, vérifiées contre les trust roots de l'instance de
destination, et le bordereau nomme l'empreinte de la clé qui a vérifié
chacune d'elles, pour que l'affirmation soit contrôlable plutôt que
décorative. Ne classez pas un bordereau comme preuve qu'un support est
authentique ; classez-le à côté du support qu'il décrit.

L'avertissement ne peut pas être désactivé, et il figure dans chaque format :
la page HTML, le texte brut, et le `"signed": false` de la forme JSON.

## Ce qu'il contient

| Section | Contenu |
| --- | --- |
| **Le support** | Côté (source ou destination), zone servie, zone destinataire du support, identifiant du support, dates de résolution et d'écriture, version de Tobby et identifiant de run producteurs, volumes. |
| **Fraîcheur** | Le dernier support importé pour cette zone, la conformité de l'identité de zone, et si le support est plus ancien que le dernier import. |
| **Inventaire** | Une ligne par recipe (digest, date de résolution, assurance) et une par ingrédient (type, tag, digest). |
| **Rapport de vérification** | Le verdict, les refus portant sur le support entier (avec la possibilité pour un administrateur de les outrepasser, et s'il l'a fait), les trois étapes — manifeste, sommes de contrôle, signatures — et une ligne par livraison qui nomme le fichier fautif quand elle est bloquée. |
| **Analyse de vulnérabilités** | La politique en vigueur et la base utilisée avec son âge ; puis, **par recipe et par élément**, le verdict, le mode d'évaluation du rapport (**consultatif** ou **bloquant**), le seuil, l'âge de la base *contre laquelle ce rapport a été produit*, les comptes par sévérité, les violations et les principaux résultats. |
| **Assurance réduite** | Chaque recipe admise sous un scope de confiance assoupli, avec le scope et ce qu'il a levé ([R-22](../../security/reduced-assurance/)). |

Les **marqueurs d'assurance réduite** ne sont pas seulement rassemblés dans
leur propre section. Ils figurent aussi à côté de la recipe qu'ils qualifient
— dans l'inventaire, dans le tableau de vérification et dans la section
d'analyse — parce qu'un marqueur qu'il faut aller chercher est un marqueur
qu'on manque. Côté destination, le bordereau place le verdict propre de
l'instance à côté de l'allégation de la zone source, étiquetée comme telle.

### À propos de la section d'analyse

L'analyse s'exécute là où le contenu est récupéré, côté source. Les rapports
sont conservés dans les documents de tâche de l'instance qui a analysé, et le
manifeste ne les porte pas. Un bordereau établi sur l'instance qui a récupéré
le contenu liste les rapports de la tâche la plus récente qui a analysé chaque
élément. Un bordereau établi à destination indique, pour chaque recipe, qu'il
**ne détient aucun rapport d'analyse** — il n'en invente pas. Remettez le
bordereau côté source avec le disque quand les relecteurs de la destination
ont besoin des résultats d'analyse.

L'analyse s'exécute par défaut en **mode consultatif** : les résultats sont
rapportés et rien n'est retenu. La colonne « Mode » le rend visible par
élément, de sorte qu'un bordereau ne laisse jamais croire qu'une page
d'apparence propre correspond à une politique bloquante. Voir
[l'analyse de vulnérabilités](../../security/vulnerability-scanning/) pour
poser un seuil bloquant.

## L'obtenir

**Depuis l'écran Média** — la carte *Bordereau de transfert* propose
*Ouvrir le bordereau* (une page pour la boîte d'impression du navigateur —
utilisez l'impression du navigateur et « Enregistrer au format PDF » si vous
avez besoin d'un PDF), la même dans l'autre langue, et *Enregistrer en HTML* /
*Enregistrer en texte*. Lire l'écran est une action de `viewer`, le bordereau
aussi.

**Depuis l'API** — `GET /api/v1/media/slip`, rôle `viewer` :

```sh
curl -u viewer:… 'https://tobby.zone.example/api/v1/media/slip?format=text&lang=fr'
curl -u viewer:… -OJ 'https://tobby.zone.example/api/v1/media/slip?format=html&download=1'
```

| Paramètre | Valeurs |
| --- | --- |
| `format` | `html` (défaut), `text`, `json` — le document neutre vis-à-vis de la langue, des codes et des états plutôt que des phrases. |
| `lang` | `en` ou `fr` ; l'en-tête `Accept-Language` en son absence. |
| `download` | `1` ajoute un en-tête `Content-Disposition: attachment`. |

**Depuis la ligne de commande** — sur l'hôte qui détient le support, sans
instance en service :

```sh
tobby media slip --storage-root /mnt/usb --zone production --lang fr > bordereau.txt
tobby media slip --storage-root /mnt/usb --zone production --output html > slip.html
```

Avec une identité de zone, la commande vérifie d'abord le support, exactement
comme `tobby media verify` et sans rien écrire, de sorte que le bordereau
porte le verdict. Sans identité de zone, c'est un bordereau côté source. Elle
sort avec le code `0` quel que soit le verdict : un support bloqué donne un
bordereau réussi d'un mauvais support. La commande ne lit pas le scanner : sa
section d'analyse indique que l'état du scanner et de la base n'a pas été lu ;
les rapports par élément se suffisent à eux-mêmes.

## Le fichier HTML

L'export HTML est **autonome** : la feuille de style d'impression est
intégrée, et la page ne charge rien — ni police, ni image, ni script, ni lien
— et le dit dans sa propre `Content-Security-Policy`. Il se lit de la même
façon sur un portable dans une zone sans réseau, en pièce jointe et dans une
archive. Ses règles d'impression fixent une page A4, gardent entières les
lignes de tableau et les deux avertissements, et n'ont pas besoin de couleur :
tout ce qui demande de l'attention est aussi un mot en capitales.
