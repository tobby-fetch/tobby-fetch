---
title: "Thème : les jetons de design"
description: Les propriétés CSS personnalisées qui forment le système de design de l'interface web, dans les deux thèmes, comme surface documentée de ui.themeOverride — et le contrat d'accessibilité qu'une surcharge reprend à son compte.
sidebar:
  order: 8
---

L'interface web est habillée à partir d'un seul jeu de **jetons de design** :
des propriétés CSS personnalisées, déclarées une fois pour le thème sombre (le
thème canonique) et une fois pour le thème clair. Chaque composant les lit ;
aucun ne porte de couleur ni de taille en propre. Les jetons sont donc la
façon prise en charge de reprendre l'interface à vos couleurs — et cette page
la liste de ce que vous pouvez surcharger.

## Surcharger les jetons

Faites pointer [`ui.themeOverride`](../configuration/#interface-journalisation-arrêt)
vers une feuille de style de l'hôte de l'instance :

```yaml
ui:
  themeOverride: /etc/tobby/brand.css
```

```css
/* /etc/tobby/brand.css — redéclarer des jetons, rien d'autre */
:root {
  --tobby-accent: #2f6fdb;
  --tobby-accent-text: #6aa0ff;
  --tobby-focus-ring: 2px solid #6aa0ff;
}
:root[data-theme="light"] {
  --tobby-accent: #1f56b3;
  --tobby-accent-text: #1f56b3;
  --tobby-focus-ring: 2px solid #1f56b3;
}
```

Le fichier est servi après les jetons embarqués : une propriété redéclarée sur
`:root` (sombre) ou `:root[data-theme="light"]` l'emporte par l'ordre des
sources, et tout ce que vous ne touchez pas garde sa valeur livrée. Le fichier est lu
sur le disque à chaque requête : une modification apparaît au chargement de
page suivant ; changer le chemin `ui.themeOverride` lui-même demande un
redémarrage. Seuls les
jetons sont une surface prise en charge — les règles des composants (classes
`.t-*`) sont internes et peuvent changer d'une version à l'autre.

## Le contrat d'accessibilité

Les jetons livrés respectent le niveau AA de WCAG 2.1 (NFR-017) dans les deux
thèmes, et deux tests les y tiennent : les paires de jetons sont vérifiées par
le calcul à chaque build, et les écrans cœur sont audités dans un vrai
navigateur, en français et en anglais, dans les deux thèmes (contraste du
texte réellement peint, indicateurs de focus, noms accessibles, ordre au
clavier).

**Une surcharge reprend cette responsabilité à son compte.** Quand vous
redéclarez un jeton, conservez ces relations :

- tout jeton `*-text`, `--tobby-text` et `--tobby-muted` : au moins 4,5:1 sur
  `--tobby-bg` et sur `--tobby-bg-raise` (et sur les teintes `*-bg` pour les
  jetons `*-text` qui s'y posent : puces, badges) ;
- `--tobby-on-accent` sur `--tobby-accent` et sur `--tobby-success` : au moins
  4,5:1 ;
- `--tobby-border-control` et `--tobby-focus-ring` : au moins 3:1 sur les deux
  surfaces (WCAG 1.4.11) ;
- ne supprimez pas l'anneau de focus : `--tobby-focus-ring: none` prive les
  utilisateurs du clavier de tout focus visible.

Pour vérifier rapidement une palette : un vérificateur de contraste sur chacune
de ces paires, puis un parcours à la touche Tab du tableau de bord, de la liste
du contenu et d'une page de correspondance de recipe.

## Référence des jetons

Les valeurs sont les défauts livrés. Un jeton que le thème clair ne redéclare
pas garde sa valeur sombre.

### Surfaces et texte

| Jeton | Sombre | Clair | Rôle |
|---|---|---|---|
| `--tobby-bg` | `#0b0d12` | `#faf9f6` | Fond de page. |
| `--tobby-bg-raise` | `#12151d` | `#ffffff` | Surfaces surélevées : cartes, tableaux, dialogues, en-tête. |
| `--tobby-text` | `#e8eaf0` | `#1a1d24` | Texte courant. 7:1 sur `--tobby-bg` dans les thèmes livrés. |
| `--tobby-muted` | `#949aab` | `#5f6472` | Texte secondaire : légendes, métadonnées, indications. |

### Accent et statuts

| Jeton | Sombre | Clair | Rôle |
|---|---|---|---|
| `--tobby-accent` | `#f0a92e` | `#b8710d` | La couleur d'identité en aplat ou en graphisme : boutons pleins, états sélectionnés, anneau de focus. |
| `--tobby-accent-soft` | `#ffc25c` | `#a9660b` | Survol des contrôles pleins en accent. |
| `--tobby-accent-text` | `#f0a92e` | `#8c5a05` | L'accent utilisé comme texte (liens, onglets actifs, badges partiels). |
| `--tobby-success` | `#6fce93` | `#259254` | Succès en aplat : badges pleins. |
| `--tobby-success-text` | `#6fce93` | `#27704a` | Succès en texte. |
| `--tobby-danger` | `#e0656f` | `#b3323e` | Danger en aplat ou en bordure. |
| `--tobby-danger-text` | `#e0656f` | `#b3323e` | Danger en texte : erreurs, actions destructrices. |
| `--tobby-on-accent` | `#0b0d12` | *(identique)* | Encre posée sur les aplats d'accent et de succès. Déclarée une fois, dans le bloc sombre, et partagée par les deux thèmes. |
| `--tobby-warning-bg` | `rgba(240, 169, 46, 0.1)` | `rgba(201, 126, 18, 0.1)` | Teinte de fond des avertissements et des badges d'accent. |
| `--tobby-danger-bg` | `rgba(224, 101, 111, 0.1)` | `rgba(179, 50, 62, 0.08)` | Teinte de fond des erreurs. |
| `--tobby-success-bg` | `rgba(111, 206, 147, 0.12)` | `rgba(46, 125, 79, 0.1)` | Teinte de fond du succès. |

### Bordures, puces et indicateurs

| Jeton | Sombre | Clair | Rôle |
|---|---|---|---|
| `--tobby-border-hairline` | `rgba(255, 255, 255, 0.09)` | `rgba(0, 0, 0, 0.1)` | Séparateurs décoratifs (cartes, filets de tableau). Pas une limite de contrôle. |
| `--tobby-border-control` | `rgba(255, 255, 255, 0.38)` | `rgba(0, 0, 0, 0.48)` | Limite des champs, pastilles et boutons à contour : au moins 3:1 sur les deux surfaces. |
| `--tobby-chip-bg` | `rgba(255, 255, 255, 0.05)` | `rgba(0, 0, 0, 0.04)` | Fond des puces. |
| `--tobby-skeleton-base` | `rgba(255, 255, 255, 0.06)` | `rgba(0, 0, 0, 0.05)` | Indicateurs de chargement, fond. |
| `--tobby-skeleton-sheen` | `rgba(255, 255, 255, 0.12)` | `rgba(0, 0, 0, 0.1)` | Indicateurs de chargement, reflet. |

### Géométrie et focus

| Jeton | Sombre | Clair | Rôle |
|---|---|---|---|
| `--tobby-radius` | `0.8rem` | *(identique)* | Rayon des coins des cartes, tableaux et dialogues. |
| `--tobby-radius-sm` | `0.55rem` | *(identique)* | Rayon des coins des boutons et champs. |
| `--tobby-radius-xs` | `0.3rem` | *(identique)* | Rayon des coins du code en ligne. |
| `--tobby-focus-ring` | `2px solid var(--tobby-accent)` | *(identique)* | L'anneau de focus clavier, une valeur `outline` complète. Le garder visible : 3:1 sur les deux surfaces. |

### Typographie

| Jeton | Sombre | Clair | Rôle |
|---|---|---|---|
| `--tobby-font-sans` | `ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif` | *(identique)* | Police de l'interface. |
| `--tobby-font-mono` | `ui-monospace, "SF Mono", Menlo, Consolas, monospace` | *(identique)* | Police des chemins, digests et du code. |
| `--tobby-text-2xs` | `0.66rem` | *(identique)* | Surtitres, en-têtes de tableau, libellés de tuile. |
| `--tobby-text-xs` | `0.72rem` | *(identique)* | Puces, libellés de formulaire, métadonnées. |
| `--tobby-text-sm` | `0.8rem` | *(identique)* | Cellules de tableau, boutons, corps dense. |
| `--tobby-text-md` | `0.875rem` | *(identique)* | Texte courant de base (14 px). |
| `--tobby-text-lg` | `1rem` | *(identique)* | Marque, titres de carte. |
| `--tobby-text-xl` | `1.15rem` | *(identique)* | Titres de page. |
| `--tobby-text-2xl` | `1.25rem` | *(identique)* | Valeurs des tuiles du tableau de bord. |

### Espacement

| Jeton | Sombre | Clair | Rôle |
|---|---|---|---|
| `--tobby-space-1` | `0.25rem` | *(identique)* | Échelle d'espacement, plus petit pas. |
| `--tobby-space-2` | `0.5rem` | *(identique)* | Échelle d'espacement. |
| `--tobby-space-3` | `0.75rem` | *(identique)* | Échelle d'espacement. |
| `--tobby-space-4` | `1rem` | *(identique)* | Échelle d'espacement. |
| `--tobby-space-5` | `1.5rem` | *(identique)* | Échelle d'espacement. |
| `--tobby-space-6` | `2.5rem` | *(identique)* | Échelle d'espacement, plus grand pas. |
