# Project assets

A mockup that shows the product's real logo, icons, screenshots, and stylesheet is easier for the human to judge than one built on kit placeholders. Every successful `render_view` result returns the kit catalog, and its `usage.assets` note states the asset grammar; [the kit cheatsheet](references/kit-cheatsheet.md) has the reject codes.

## Discovery checklist

There is no pre-render asset inventory, and a wrong path fails the whole render.

1. `assets/`, `public/`, `static/`, `img/`, `images/`, `media/` at the root and under the web app's package.
2. `docs/` for screenshots and marketing art; `brand/`, `design/`, `.github/` for logos.
3. Name search for `logo`, `wordmark`, `favicon`, `icon`, `og-image`, `screenshot`.
4. For stylesheets, the app's entry CSS (`src/styles/`, `app.css`, `main.css`, `index.css`) — not a preprocessor source (`.scss`/`.less` are not servable; use the built `.css` or write the delta yourself).
5. Confirm the exact path exists (`find` or `list_dir`) before authoring.

## Prefer real marks over placeholders

Use the repo logo instead of `.kit-avatar`, the product's own SVG icons instead of generic `#kit-*` icons, and a real screenshot instead of `.kit-media-*` boxes. In HTML reference a project image with `<img src="http://lycaon.asset/…">`; in SVG use `<image href="http://lycaon.asset/…">`. Do not point `<use href>` at a project file.

```html
<header style="display:flex;align-items:center;gap:var(--kit-space-3);padding:var(--kit-space-4)">
  <img src="http://lycaon.asset/assets/logo.svg" alt="logo" style="height:40px">
  <span class="kit-font-display" style="font-size:var(--kit-text-2xl)">Product name</span>
</header>
```

Swap the path for the discovered logo. Height-constrain the image and let width follow; never stretch a raster logo past its native size.

## Restyle on the real stylesheet

To propose a change to an app whose CSS lives in the repo, link that stylesheet first, then add one `<style>` block carrying only your proposed delta. The product's rules apply only where your markup uses its real class names, so copy the structure from its templates and keep kit tokens for anything its CSS does not define.

```html
<link rel="stylesheet" href="http://lycaon.asset/src/styles/app.css">
<style>
  /* proposed delta only — everything else inherits the real stylesheet */
  .sidebar { width: 240px; }
  .card { border-radius: var(--kit-radius); }
</style>
<div class="app-shell">
  <!-- markup copied from the product's real templates, real class names -->
</div>
```

Relative `url(...)` inside the linked file resolves against its own repo directory and is served through the same fence. Repo CSS with any `@import` other than an absolute `http://lycaon.asset/` URL (relative imports included) or an `https` `url()` is refused (`RENDER_ASSET_DENIED`), as is repo SVG with scripts, `on*=` handlers, `foreignObject`, or external `href`/`src`. When a project stylesheet pulls a CDN font, copy the rules you need into your delta block instead of linking that file.

## Before and after with a committed screenshot

When the repo carries a screenshot (for example under `docs/`), you may pair it with the proposal in one canvas, labeled as such:

```html
<div style="display:grid;grid-template-columns:1fr 1fr;gap:var(--kit-space-4)">
  <figure><img src="http://lycaon.asset/docs/screenshots/settings.png" style="max-width:100%"><figcaption>Repo screenshot (committed; may predate HEAD)</figcaption></figure>
  <figure><!-- proposed layout --><figcaption>Proposed (authored)</figcaption></figure>
</div>
```

Use `capture_page` when the current state matters. For a decision between full variants, keep one artifact per option instead ([comparing options](references/compare-options.md)).

## Confirm what was served

A successful result reports `kit.assets.count` and a sample of served paths. Check that the count matches the distinct files you referenced, and caption which real assets appear.
