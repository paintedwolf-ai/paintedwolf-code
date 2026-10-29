---
name: craft-icons-and-chrome
description: Author or restyle SVG icons, status badges, and UI chrome such as window headers, command palettes, toolbars, and tab strips, checked at pixel level.
license: Apache-2.0
metadata:
  author: painted-wolf
---

# Craft icons and chrome

## Procedure

1. **Entry check.** The task authors, evaluates, or restyles vector icons, badges, glyph systems, or chrome containers (window headers, command palettes, floating toolbars, tab strips). Page-level layout with no icon or chrome work belongs to mock-before-build; leave for non-visual work.
2. **Choose grid and style.** Record a spec row (`name`, `viewbox`, `style_variant`, `stroke_weight`, `grid_padding`):
   - `0 0 24 24`, 2px padding: action icons, navigation, toolbars; `stroke-width` 2 or 1.5, round caps and joins.
   - `0 0 20 20` or `0 0 16 16`, 1px padding: compact table controls, inline badges, micro-actions; 1.5 stroke or solid.
   - Variants: `linear-outline`, `solid-silhouette`, `duotone-accent` (20% opacity secondary fill), `badged-container` (offset status ring), `elevated-chrome` (surfaces on `--kit-*` tokens).
3. **Author the markup** at its project path (`assets/icons/<name>.svg`, `components/chrome/<name>.html`).
   - SVG: `xmlns`, matching `viewBox`, `fill="none"`, `stroke="currentColor"`, `aria-hidden="true"`, integer anchor coordinates.
   - Chrome: `--kit-surface`, `--kit-border`, `--kit-radius`, layered `--kit-shadow`, and `--kit-bg` / `--kit-fg` so theme swaps work.
4. **Preview with `render_view`:** pass the markup, a `viewport` sized to the component (`{width: 128, height: 128}` for an icon, `{width: 320, height: 240}` for chrome), `theme: "transparent"` or `"dark"`, and `handle: "<component-name>"`.
5. **Inspect pixels with `view_image`** (`handle`, `scale: 2.0`). Check that straight strokes land on whole pixels, asymmetric glyphs (play, triangles, diagonal arrows) are optically centered, and cutouts and badges stay distinct from their container.
6. **Refine with `render_view` patches** (`handle`, `old_string`, `new_string`), at most 3 iterations; stop sooner once edges are crisp and weight matches neighboring controls.
   - `RENDER_MARKUP_FORBIDDEN`: remove external links and `<script>`; keep markup hermetic.
   - `RENDER_PATCH_NOT_FOUND` / `RENDER_PATCH_AMBIGUOUS`: widen `old_string` or pass full `markup`.
   - `IMAGE_DIMENSIONS_EXCEEDED`: shrink the `viewport` or use `scale: 1.0`.
7. **Export and report.** Remove preview scaffolding, save the production SVG or component, and report `asset_path`, `style_variant`, `viewbox`, `stroke_weight`, and `visual_handle`.

## References

- [Icon styles and mechanics](references/icon-styles-and-mechanics.md): grid alignment, stroke weights, optical balance, path economy.
- [Permissive pack patterns](references/permissive-pack-patterns.md): geometry signatures and license attribution for Lucide, Heroicons, Phosphor, Tabler, and Radix.
- [UI chrome and containers](references/ui-chrome-and-containers.md): window headers, palettes, segmented controls, docks, cards, toasts; specimens in `assets/`.
- [Chrome elevation and tokens](references/chrome-elevation-and-tokens.md): shadows, `<kbd>` badges, and `--kit-*` token mapping.
- [Visual inspection workflow](references/visual-inspection-workflow.md): the `render_view` and `view_image` loop in detail.
- [Worked example](references/worked-example.md): a duotone search icon inside elevated palette chrome.
