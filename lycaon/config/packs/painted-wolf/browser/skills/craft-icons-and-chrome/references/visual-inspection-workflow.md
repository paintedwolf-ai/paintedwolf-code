# Visual inspection workflow

This guide details the precision inspection feedback loop that couples `render_view` and `view_image` to evaluate, debug, and refine vector icons and UI chrome without needing a running frontend dev server.

---

## 1. The visual iteration loop

```
+------------------+       +-------------------+       +--------------------+
|  1. Draft Vector | ----> |   2. render_view  | ----> |   3. view_image    |
|   or Chrome HTML |       |  (headless raster)|       |  (inspect pixels)  |
+------------------+       +-------------------+       +--------------------+
         ^                                                        |
         |               4. Surgical patch editing                |
         +--------------------------------------------------------+
```

### Step 1: Render with tight component bounds

When rendering isolated SVG icons or compact chrome components, avoid full desktop viewports (which introduce massive empty canvas margins). Instead, provide an explicit `viewport` sized closely to the component:

- **Isolated 24×24 icon**: use `viewport: { width: 128, height: 128 }` with `theme: "transparent"` or a padded specimen card.
- **Search bar / Command palette**: use `viewport: { width: 640, height: 320 }`.
- **Window header / Dock**: use `viewport: { width: 500, height: 180 }`.

Always supply `handle: "<name>"` so the rendered canvas is preserved in session memory for rapid patching.

```json
{
  "markup": "<div class=\"kit-surface\" style=\"padding:16px; display:inline-flex\">...</div>",
  "viewport": { "width": 320, "height": 200 },
  "theme": "dark",
  "handle": "cmd-palette-chrome"
}
```

---

### Step 2: Inspect raster output with `view_image`

Call `view_image` directly on the session handle returned by `render_view`:

```json
{
  "handle": "cmd-palette-chrome",
  "scale": 2.0
}
```

Inspect the rendered image for the following critical criteria:

1. **Pixel snapping and anti-aliasing sharpness**:
   - Check vertical and horizontal edges. Do they render as crisp 1px/2px lines, or are they fuzzy, two-pixel gray blends?
   - *Fix*: If a 1px stroke sits on an integer coordinate (e.g. `x1="10" x2="10"`), the stroke expands `0.5px` to each side, triggering anti-aliasing blur. Shift the coordinate to `x1="10.5" x2="10.5"`, or use an even stroke width (`2px`) on integer coordinates.
2. **Optical weight and balance**:
   - Compare the icon against adjacent text or sibling icons. Does an asymmetrical glyph (like an arrow or triangle) look off-center?
   - *Fix*: Nudge coordinates by `1px` to restore optical alignment.
3. **Contrast across themes**:
   - Verify that `--kit-border`, `--kit-muted`, and `--kit-surface` provide sufficient contrast in both light and dark themes.

---

### Step 3: Surgically patch with `old_string` and `new_string`

Instead of rewriting and resending entire multi-kilobyte markup payloads, modify the stored handle directly using `render_view` surgical patching:

```json
{
  "handle": "cmd-palette-chrome",
  "old_string": "stroke-width=\"1.5\"",
  "new_string": "stroke-width=\"2\"",
  "replace_all": true
}
```

Re-inspect the updated handle with `view_image`. Repeat this cycle until the visual result meets production standards (bounded to at most 3 iteration loops).

---

### Step 4: Clean export

Once visually verified, extract the clean SVG or HTML code from the preview wrapper:
- Ensure the root SVG includes: `xmlns="http://www.w3.org/2000/svg"`, `viewBox="0 0 24 24"`, `fill="none"`, `stroke="currentColor"`, and `aria-hidden="true"`.
- Strip temporary background staging, debug borders, and test containers.
- Save the asset to its destination project path.
