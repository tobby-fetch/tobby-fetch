import { defineConfig } from "astro/config";

// GitHub Pages project site for the tobby-fetch/tobby-fetch repository.
// Served at https://tobby-fetch.github.io/tobby-fetch/
// If a custom domain is assigned later, set `site` to it and remove `base`.
export default defineConfig({
  site: "https://tobby-fetch.github.io",
  base: "/tobby-fetch",
});
