---
title: "Theming: the design tokens"
description: The CSS custom properties that make up the web interface's design system, in both themes, as the documented surface of ui.themeOverride — plus the accessibility contract an override takes over.
sidebar:
  order: 9
---

The web interface is styled from a single set of **design tokens**: CSS custom
properties, declared once for the dark theme (the canonical one) and once for
the light theme. Every component reads them; none carries a colour or a size
of its own. That makes the tokens the supported way to rebrand the interface —
and this page the list of what you may override.

## Overriding the tokens

Point [`ui.themeOverride`](../configuration/#ui-logging-shutdown) at a
stylesheet on the instance host:

```yaml
ui:
  themeOverride: /etc/tobby/brand.css
```

```css
/* /etc/tobby/brand.css — redeclare tokens, nothing else */
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

The file is served after the embedded tokens, so a property you redeclare on
`:root` (dark) or `:root[data-theme="light"]` wins by source order, and
everything you leave alone keeps its shipped value. The file is read from
disk on each request, so an edit shows on the next page load; changing the
`ui.themeOverride` path itself needs a restart. Only tokens are a supported surface —
the component rules (`.t-*` classes) are internal and may change between
versions.

## The accessibility contract

The shipped tokens meet WCAG 2.1 level AA (NFR-017) in both themes, and
two tests hold them to it: the token pairs are checked by arithmetic on every
build, and the core screens are audited in a real browser, in English and in
French, in both themes (contrast of the text actually painted, focus
indicators, accessible names, keyboard order).

**An override takes that responsibility over.** When you redeclare a token,
keep these relations:

- every `*-text` token, `--tobby-text` and `--tobby-muted`: at least 4.5:1 on
  both `--tobby-bg` and `--tobby-bg-raise` (and on the tinted `*-bg` washes
  for the `*-text` tokens that sit on them: chips, badges);
- `--tobby-on-accent` on `--tobby-accent` and on `--tobby-success`: at least
  4.5:1;
- `--tobby-border-control` and `--tobby-focus-ring`: at least 3:1 on both
  surfaces (WCAG 1.4.11);
- do not remove the focus ring: `--tobby-focus-ring: none` leaves keyboard
  users without a visible focus.

A quick check of a palette: a contrast checker on each of those pairs, then a
Tab walk through the dashboard, the content list and a recipe mapping page.

## Token reference

Values are the shipped defaults. A token the light theme does not redeclare
keeps its dark value.

### Surfaces and text

| Token | Dark | Light | Role |
|---|---|---|---|
| `--tobby-bg` | `#0b0d12` | `#faf9f6` | Page background. |
| `--tobby-bg-raise` | `#12151d` | `#ffffff` | Raised surfaces: cards, tables, dialogs, the header. |
| `--tobby-text` | `#e8eaf0` | `#1a1d24` | Body text. 7:1 on `--tobby-bg` in the shipped themes. |
| `--tobby-muted` | `#949aab` | `#5f6472` | Secondary text: captions, metadata, placeholders. |

### Accent and status

| Token | Dark | Light | Role |
|---|---|---|---|
| `--tobby-accent` | `#f0a92e` | `#b8710d` | The identity colour as a fill or a graphic: solid buttons, selected states, the focus ring. |
| `--tobby-accent-soft` | `#ffc25c` | `#a9660b` | Hover of solid accent controls. |
| `--tobby-accent-text` | `#f0a92e` | `#8c5a05` | The accent used as text (links, active tabs, partial badges). |
| `--tobby-success` | `#6fce93` | `#259254` | Success as a fill: solid badges. |
| `--tobby-success-text` | `#6fce93` | `#27704a` | Success as text. |
| `--tobby-danger` | `#e0656f` | `#b3323e` | Danger as a fill or border. |
| `--tobby-danger-text` | `#e0656f` | `#b3323e` | Danger as text: errors, destructive actions. |
| `--tobby-on-accent` | `#0b0d12` | *(same)* | Ink painted on solid accent and success fills. Declared once, in the dark block, and shared by both themes. |
| `--tobby-warning-bg` | `rgba(240, 169, 46, 0.1)` | `rgba(201, 126, 18, 0.1)` | Tinted wash behind warnings and the accent badges. |
| `--tobby-danger-bg` | `rgba(224, 101, 111, 0.1)` | `rgba(179, 50, 62, 0.08)` | Tinted wash behind errors. |
| `--tobby-success-bg` | `rgba(111, 206, 147, 0.12)` | `rgba(46, 125, 79, 0.1)` | Tinted wash behind success. |

### Borders, chips and placeholders

| Token | Dark | Light | Role |
|---|---|---|---|
| `--tobby-border-hairline` | `rgba(255, 255, 255, 0.09)` | `rgba(0, 0, 0, 0.1)` | Decorative separators (cards, table rules). Not a control boundary. |
| `--tobby-border-control` | `rgba(255, 255, 255, 0.38)` | `rgba(0, 0, 0, 0.48)` | Boundary of inputs, pills and outline buttons: at least 3:1 on both surfaces. |
| `--tobby-chip-bg` | `rgba(255, 255, 255, 0.05)` | `rgba(0, 0, 0, 0.04)` | Background of chips. |
| `--tobby-skeleton-base` | `rgba(255, 255, 255, 0.06)` | `rgba(0, 0, 0, 0.05)` | Loading placeholders, base. |
| `--tobby-skeleton-sheen` | `rgba(255, 255, 255, 0.12)` | `rgba(0, 0, 0, 0.1)` | Loading placeholders, highlight. |

### Geometry and focus

| Token | Dark | Light | Role |
|---|---|---|---|
| `--tobby-radius` | `0.8rem` | *(same)* | Corner radius of cards, tables and dialogs. |
| `--tobby-radius-sm` | `0.55rem` | *(same)* | Corner radius of buttons and inputs. |
| `--tobby-radius-xs` | `0.3rem` | *(same)* | Corner radius of inline code. |
| `--tobby-focus-ring` | `2px solid var(--tobby-accent)` | *(same)* | The keyboard focus ring, a complete `outline` value. Keep it visible: 3:1 on both surfaces. |

### Typography

| Token | Dark | Light | Role |
|---|---|---|---|
| `--tobby-font-sans` | `ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif` | *(same)* | Interface typeface. |
| `--tobby-font-mono` | `ui-monospace, "SF Mono", Menlo, Consolas, monospace` | *(same)* | Typeface of paths, digests and code. |
| `--tobby-text-2xs` | `0.66rem` | *(same)* | Kickers, table headers, tile labels. |
| `--tobby-text-xs` | `0.72rem` | *(same)* | Chips, form labels, metadata. |
| `--tobby-text-sm` | `0.8rem` | *(same)* | Table cells, buttons, dense body. |
| `--tobby-text-md` | `0.875rem` | *(same)* | Base body text (14 px). |
| `--tobby-text-lg` | `1rem` | *(same)* | Brand, card titles. |
| `--tobby-text-xl` | `1.15rem` | *(same)* | Page titles. |
| `--tobby-text-2xl` | `1.25rem` | *(same)* | Dashboard tile values. |

### Spacing

| Token | Dark | Light | Role |
|---|---|---|---|
| `--tobby-space-1` | `0.25rem` | *(same)* | Spacing scale, smallest step. |
| `--tobby-space-2` | `0.5rem` | *(same)* | Spacing scale. |
| `--tobby-space-3` | `0.75rem` | *(same)* | Spacing scale. |
| `--tobby-space-4` | `1rem` | *(same)* | Spacing scale. |
| `--tobby-space-5` | `1.5rem` | *(same)* | Spacing scale. |
| `--tobby-space-6` | `2.5rem` | *(same)* | Spacing scale, largest step. |
