# Design-kit cheatsheet

This file is a pre-first-call cheatsheet, not a second contract — every successful `render_view` result returns the full kit catalog with usage notes.

## Color and theme tokens

`--kit-bg`, `--kit-fg`, `--kit-muted`, `--kit-accent`, `--kit-accent-fg`, `--kit-border`, `--kit-danger`, `--kit-surface`, `--kit-shadow`, `--kit-radius`. Themes are `light` (default), `dark`, and `transparent` (a clear canvas for standalone icons and components); the host sets `data-kit-theme` and swaps token values, so markup that stays on tokens themes for free. Helper classes: `.kit-muted`, `.kit-accent`.

## Type and spacing

Text sizes `--kit-text-xs` through `--kit-text-4xl`; spacing `--kit-space-1` (0.25rem) through `--kit-space-8` (4rem). Font stacks via `.kit-font-sans`, `.kit-font-serif`, `.kit-font-display`, `.kit-font-mono` or the matching `--kit-font-*` variables.

## Fonts

Inter, Source Sans 3, IBM Plex Sans, DM Sans, Space Grotesk, Source Serif 4, Literata, Playfair Display, Fraunces, JetBrains Mono. Use single-quoted names in style attributes, e.g. `style="font-family:'Fraunces'"`. The host injects OFL @font-face data URLs; never link a CDN.

## Icons

Lucide-style kebab-case names referenced as `<svg class="kit-icon"><use href="#kit-NAME"/></svg>`. Guess from English — base nouns (`search`, `settings`, `user`, `house`), states (`bell-off`, `user-plus`, `circle-check`), directions (`arrow-right`, `chevron-down`). Common synonyms resolve: close→x, delete→trash-2, gear→settings, email→mail, chat→message-circle. Unknown names reject with suggestions.

## Placeholders and surfaces

Media placeholders `.kit-media-16x9`, `.kit-media-4x3`, `.kit-media-1x1`; `.kit-avatar` for a round avatar; `.kit-surface` for a bordered, shadowed card.

## Viewports

| Preset | CSS px |
|--------|--------|
| `phone` | 390×844 |
| `phone-landscape` | 844×390 |
| `tablet` | 768×1024 |
| `tablet-landscape` | 1024×768 |
| `desktop` (default) | 1280×720 |
| `desktop-wide` | 1440×900 |

Explicit `viewport.width`/`height` override a preset.

`viewport.fit` decides what the preset height means:

- **`content` (default)** — the preset fixes the width and sets a minimum height floor (e.g. 720px for desktop). Documents taller than the floor grow to fit. To avoid unused background margins on shorter mockups, make root containers fill 100% width/height, specify explicit `viewport.width`/`height`, or use `theme: transparent`.
- **`viewport`** — the preset is an exact frame and anything below it is cropped. Use it only when the fold is the subject: above-the-fold, or one phone screen.

Growth extends the capture, not the layout viewport. `vh` units and viewport media queries stay bound to the requested height, so `min-height:100vh` is one screen inside a much taller image — that is intent, not a bug. The result reports that height as `frame_height`.

Every result reports `canvas`: `width`, `height`, `frame_height`, `content_height`, `complete`, `below_fold`, and `scale`. `scale` is CSS→image pixels — 2 at small canvases, dropping as the canvas grows so the raster always lands on the perceive ceiling without a second downscale. Below 1 means fine detail is softened; the layout still reads.

Canvas bounds are 2048 CSS px wide and 4096 CSS px tall. A content fit past the height cap rejects `RENDER_CANVAS_OVERFLOW` rather than returning a partial image.

## Project assets

Files under the project root render offline through the reserved origin `http://lycaon.asset/<root-relative-path>` (forward slashes, no `..`). Reference them with `<img src>`, CSS `url()`, `<link rel="stylesheet" href="…">`, or `@import url(…)`. Allowed types: png, jpeg, webp, gif, svg, css. Byte-capped per file and per render; paths under `.git/` are denied; SVG/CSS with scripts or non-asset external references are refused. The render fails fast on the first bad reference instead of drawing a broken image. The result reports `kit.assets.count` and a sample of served paths. Discovery and restyling recipes: [project assets](references/project-assets.md).

## Sandbox and limits

The page renders offline with JavaScript disabled. No `<script>`, `<iframe>`, `<object>`, `<embed>`, inline event handlers, `javascript:` URLs, or any https `url()`/`src`/`href`. `<link>`/`@import` are allowed only as asset-origin stylesheet references (above). Markup is capped at 512 KiB.

## Reject codes

| Code | Meaning | Fix |
|------|---------|-----|
| `RENDER_MARKUP_INVALID` | Empty markup, unsupported mime, or invalid viewport | Pass non-empty markup with mime `svg` or `html` and a valid viewport |
| `RENDER_MARKUP_FORBIDDEN` | External or executable construct in markup | Remove the flagged construct; inline everything on kit assets |
| `RENDER_MARKUP_OVERSIZED` | Markup over 512 KiB or a requested canvas over the max edge | Trim markup or shrink the canvas |
| `RENDER_CANVAS_OVERFLOW` | Document taller than the max content canvas | Split it into sections, tighten vertical rhythm, or go multi-column |
| `RENDER_OUTPUT_OVERSIZED` | Raster PNG over the artifact byte cap | Reduce canvas size or visual density |
| `RENDER_KIT_UNKNOWN` | Unknown font, icon, theme, or preset | Use the `suggestions` and catalog in the reject payload |
| `RENDER_HANDLE_NOT_FOUND` | Referenced session handle does not exist | Provide initial `markup` with `handle` to create it |
| `RENDER_HANDLE_CONFLICT` | The handle changed after this call read it (a concurrent patch) | Re-read the handle's current markup and reapply the patch once |
| `RENDER_PATCH_NOT_FOUND` | `old_string` was not found in the stored markup | Inspect markup or provide exact matching context |
| `RENDER_PATCH_AMBIGUOUS` | `old_string` matched multiple locations | Add surrounding context to make it unique or set `replace_all: true` |
| `RENDER_ASSET_NOT_FOUND` | Referenced asset path missing under the project root | Fix the root-relative path or drop the reference |
| `RENDER_ASSET_DENIED` | Path escape, `.git/` path, or active/external content in SVG/CSS | Use an allowed path; keep asset SVG/CSS passive and hermetic |
| `RENDER_ASSET_TYPE` | File is not png/jpeg/webp/gif/svg/css | Reference an allowed image or stylesheet |
| `RENDER_ASSET_TOO_LARGE` | File or render total over the asset byte caps | Use a smaller export or fewer large assets |

## Iterative handles and file export

- **`handle`**: Session-scoped name (e.g. `handle: "sidebar-nav"`). Passing `handle` and `markup` saves or updates the canvas in session memory.
- **`old_string` and `new_string`**: Surgically replaces substrings in the stored handle markup without resending the entire document. Set `replace_all: true` to update multiple identical occurrences.
- **`dest`**: Optional root-relative path (e.g. `dest: "assets/preview.png"`). When omitted, the view renders in-memory as an artifact visible to the agent and the user, leaving the working tree untouched.

## Project brand overlay

An optional `${overlay_dir}/design-kit.css` at the project root is appended after kit CSS on every render. Override `--kit-*` variables there to restyle all token-based markup at once.
