# Documentation authoring guide

The documentation lives in `src/content/docs/docs/` (English, canonical) and
`src/content/docs/fr/docs/` (French). It is built by Starlight, served under
`/docs/`, and embedded in the Tobby binary — every page must make sense
fully offline.

## Layout and languages

- English is canonical. Slugs are English and never change.
- French exists today for: the docs home, the whole *Try* and *air-gap*
  sections, and `reference/errors`. The rest falls back to English until
  milestone 7. A French page mirrors the exact path under `fr/docs/`.

## Frontmatter

```yaml
---
title: Page title (sentence case)
description: One sentence, used by search and social cards.
sidebar:
  order: 3            # position inside the section
  badge:              # only when the page is not fully available
    text: J5          # "J5"/"J6"/"J7" (upcoming) or "Partial"
    variant: note     # note = upcoming, caution = partial
---
```

## Status rules

- The single source of truth for feature status is `src/data/status.yaml`,
  rendered on *Discover → Project status* and (filtered) on the security
  one-pager. Never hand-maintain a second status table.
- A page that is mostly available but contains upcoming subsections badges
  the **subsections**, not the page. Use an aside right under the heading:

  ```markdown
  :::note[Upcoming — milestone 5]
  This behaviour ships with milestone 5. Track it on the
  [project status](../discover/status/) page.
  :::
  ```

- Never show a command, flag, config key or API route that does not exist
  in the code today without such a badge. When writing about shipped
  behaviour, verify against the source (`internal/cli/`, `internal/`,
  `docs/SRS.md`) — do not invent.

## Links

- Between docs pages: **relative** links with a trailing slash, resolved
  against the page URL (`/docs/<section>/<page>/`): same-section pages are
  `../<page>/`, cross-section pages are `../../<section>/<page>/`. The
  build fails on broken internal links.
- To the recipe format spec: deep links to
  `https://tobby-fetch.github.io/recipe-spec/` (normative content lives
  there, never duplicated here).
- To the repository: full GitHub URLs. External links get an automatic ↗
  marker; offline readers copy them on a connected machine.

## Voice

- Sober, concrete, honest. Limits are stated with their consequence and
  their justification — honesty is a feature of this documentation.
- Short sentences. The reader's vocabulary (an operator manages *zones*,
  *recipes*, *media* — not internal type names).
- No superlatives, no marketing filler, no em-dash walls of adjectives.
- Diagrams are inline SVG following the shared visual language (reference:
  the one in `docs/discover/why-tobby.md` — CSS-variable colors only,
  amber-stroked Tobby boxes, per-diagram unique `<marker>` ids,
  `role="img"` + `aria-label`). **No blank line inside an SVG block**:
  markdown ends the raw-HTML block there and silently breaks the
  rendering.
- Screenshots follow the capture rig in the maintainers' notes; missing
  ones are marked `<!-- TODO: screenshot: … -->` where they belong.

## Build

```bash
mise exec -- pnpm build   # includes strict internal-link validation
```

## Embedding (the offline half)

These same files are served by the binary under `/help`, so an operator in
an isolated zone reads them with no connection to anything but their own
instance (NFR-003, amendment 2026-08-11). The corpus in
`internal/help/corpus/` is a **byte-for-byte copy** of this directory —
never a second edition of it.

```bash
mise run help-sync    # refresh the copy after editing a page
mise run help-check   # fail if the copy has drifted (the CI gate)
```

What that implies when writing:

- Editing a page is not finished until `mise run help-sync` has been run
  and the corpus change committed with it. CI fails otherwise, on the
  documentation workflow rather than on the Go one.
- `index.mdx` and `reference/errors.md` are deliberately **not** embedded.
  The first is replaced by the `/help` home; the second is rendered live
  from the error catalog inside the binary, so links to
  `../../reference/errors/` — anchor included — resolve onto `/help`.
- A screenshot is embedded only if a page references it. An `![…](…)`
  pointing at a file that does not exist fails the build.
- The Markdown subset the binary renders is the one used here: headings,
  paragraphs, lists, GFM tables, fenced code, asides, links, images and
  inline SVG. A construct outside it (a new Astro component, raw HTML)
  fails `internal/help`'s tests rather than rendering as nothing.
- Anchors are computed the same way on both sides, so a `#fragment` link
  that the Astro build accepts resolves offline too — including the French
  slugs of translated headings.
