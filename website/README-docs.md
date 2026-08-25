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
- Screenshots and diagrams come in a later pass: leave
  `<!-- TODO: screenshot: … -->` or `<!-- TODO: diagram: … -->` markers
  where they belong.

## Build

```bash
mise exec -- pnpm build   # includes strict internal-link validation
```
