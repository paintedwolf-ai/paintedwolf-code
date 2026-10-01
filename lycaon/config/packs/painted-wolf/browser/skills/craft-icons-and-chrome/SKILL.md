---
name: craft-icons-and-chrome
description: Vector SVG icons, status badges, and styled chrome components for web interfaces, dashboards, and pages.
license: Apache-2.0
metadata:
  author: painted-wolf
---

# Craft icons and chrome

## Procedure

1. **Entry check.** Confirm the task involves authoring, evaluating, or restyling vector SVG icons, badges, glyph systems, or UI chrome containers (window headers, command palettes, floating toolbars, tab strips). When building or polishing a full web UI, use this skill for its bespoke icon set, badges, and chrome components. If the task is solely page-level layout wireframing with no icon or chrome needs, follow the `mock-before-build` skill instead. For non-visual logic, backend work, or styling unrelated to icons or chrome, exit immediately.
2. **Establish grid and style preset.** Select the base coordinate viewBox and style parameters from the matrix below. Record the decision in a design specification table (`name`, `viewbox`, `style_variant`, `stroke_weight`, `grid_padding`).
   - `0 0 24 24` with 2px padding (20×20 live area): standard action icons, navigation, toolbars (`stroke-width: 2px` or `1.5px`, `stroke-linecap: round`, `stroke-linejoin: round`).
   - `0 0 20 20` or `0 0 16 16` with 1px padding: compact table controls, inline badges, micro-actions (`stroke-width: 1.5px` or solid silhouettes).
   - Style variants: `linear-outline`, `solid-silhouette`, `duotone-accent` (20% opacity secondary fill layer), `badged-container` (offset status ring), or `elevated-chrome` (surfaces using `--kit-*` tokens).
3. **Author vector or chrome markup.** Write markup to a target project path (e.g. `assets/icons/<name>.svg` or `components/chrome/<name>.html`).
   - Standalone SVG: declare `xmlns="http://www.w3.org/2000/svg"`, matching `viewBox`, `fill="none"`, `stroke="currentColor"`, and `aria-hidden="true"`. Use integers for coordinate anchors to align with pixel boundaries.
   - UI chrome: structure container with `--kit-surface`, `--kit-border`, `--kit-radius`, and layered drop shadows (`--kit-shadow`). Map background and text to `--kit-bg` and `--kit-fg` so theme swaps function automatically.
4. **Render headless preview with `render_view`.** Call `render_view` passing the SVG or chrome HTML in `markup`, an explicit `viewport` matching component size (e.g. `{ width: 320, height: 240 }` for chrome or `{ width: 128, height: 128 }` for isolated icons), `theme: "transparent"` (or `"dark"` for dark mode chrome), and session `handle: "<component-name>"`.
5. **Visually inspect rasterized pixels with `view_image`.** Call `view_image` with `handle: "<component-name>"` and `scale: 2.0`. Inspect the rendered pixels for:
   - Pixel snapping: ensure horizontal and vertical strokes align cleanly to integer coordinates without blurry half-pixel anti-aliasing.
   - Optical balance: adjust asymmetrical glyphs (such as triangles, play buttons, or diagonal arrows) so their optical mass aligns with the geometric center.
   - Silhouette legibility: verify negative space cutouts and badges remain distinct against the parent container.
6. **Refine iteratively with `render_view` patch editing.**
   - Flaw resolution: call `render_view` with `handle: "<component-name>"`, `old_string`, and `new_string` to surgically update coordinates, path curves, stroke widths, or token assignments.
   - Stopping rule: stop refinement once pixel edges are crisp, optical weight matches neighboring controls, or after at most 3 patch iterations.
   - On `RENDER_MARKUP_FORBIDDEN`: remove external links or `<script>` tags; keep vector markup hermetic.
   - On `RENDER_PATCH_NOT_FOUND` or `RENDER_PATCH_AMBIGUOUS`: include broader surrounding context in `old_string` or pass the full revised `markup` to overwrite the handle.
   - On `IMAGE_DIMENSIONS_EXCEEDED`: reduce canvas width/height in `viewport` or set `scale: 1.0` in `view_image`.
7. **Produce clean export artifact and handoff report.** Strip test preview frames and write the clean, production-ready SVG or component to disk. Return a handoff report with the following fields:
   - `asset_path`: root-relative path to saved SVG or chrome component.
   - `style_variant`: selected variant from step 2.
   - `viewbox`: coordinate bounds (e.g. `0 0 24 24`).
   - `stroke_weight`: line width used for vector paths.
   - `visual_handle`: session handle used for verification in `render_view`.

## Worked example

```html
<div class="kit-surface" style="display:flex; align-items:center; gap:var(--kit-space-2); padding:6px 12px; border-radius:var(--kit-radius); max-width:320px; font-family:'Inter'">
  <svg width="20" height="20" viewBox="0 0 24 24" fill="none" aria-hidden="true" style="color:var(--kit-muted); flex-shrink:0">
    <circle cx="11" cy="11" r="8" fill="currentColor" fill-opacity="0.2"/>
    <circle cx="11" cy="11" r="8" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
    <path d="m21 21-4.35-4.35" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
  </svg>
  <span style="color:var(--kit-muted); font-size:var(--kit-text-sm); flex-grow:1">Search tools...</span>
  <kbd style="background:var(--kit-bg); color:var(--kit-muted); border:1px solid var(--kit-border); border-radius:4px; padding:2px 6px; font-size:var(--kit-text-xs); font-family:'JetBrains Mono'">⌘K</kbd>
</div>
```

## Reference guides

- [Icon styles and mechanics](references/icon-styles-and-mechanics.md): grid alignment, stroke weights, optical balance, and vector path economy.
- [Permissive pack patterns](references/permissive-pack-patterns.md): architectural rules, geometry signatures, and license attribution for Lucide, Heroicons, Phosphor, Tabler, and Radix.
- [UI chrome and containers](references/ui-chrome-and-containers.md): window headers, command palettes, segmented controls, docks, kanban cards, and `--kit-*` elevation physics.
- [Visual inspection workflow](references/visual-inspection-workflow.md): step-by-step loop coupling `render_view` with `view_image` for pixel-level quality control.
