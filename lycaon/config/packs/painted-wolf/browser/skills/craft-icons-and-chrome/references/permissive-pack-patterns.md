# Permissive pack patterns and comparative analysis

A study of industry-leading, permissively licensed open-source icon design systems. This guide extracts the mathematical rules, stroke conventions, and structural patterns that make cohesive icon libraries work across complex web applications.

## 1. Upstream license and attribution matrix

When referencing or incorporating design concepts from open-source icon systems, observe their respective permissive license terms:

| Icon system | Upstream license | Copyright holder | Key permissions & obligations |
|---|---|---|---|
| **Lucide** | **ISC** | Copyright (c) Lucide Contributors | Attribution required; permits free commercial use, modification, and redistribution. |
| **Heroicons** | **MIT** | Copyright (c) 2020-2023 Tailwind Labs Inc. | Attribution and copyright notice retained; free for commercial and private use. |
| **Phosphor Icons** | **MIT** | Copyright (c) 2020-2023 Tobias Fried & Helena Zhang | Attribution and copyright notice retained; full commercial and derivative rights. |
| **Tabler Icons** | **MIT** | Copyright (c) 2020-2023 Paweł Kuna | Attribution and copyright notice retained; permits modification and bundling. |
| **Radix Icons** | **MIT** | Copyright (c) 2020-2023 WorkOS | Attribution and copyright notice retained; designed for web component libraries. |

All specimen assets and examples bundled inside this skill are first-party implementations licensed under **Apache-2.0**.

---

## 2. Architectural profiles of leading packs

### Lucide (ISC)
- **Base grid**: `24×24` with `2px` standard inner padding.
- **Stroke rules**: Uniform `2px` stroke, `stroke-linecap="round"`, `stroke-linejoin="round"`.
- **Design signature**: Stroke-first construction. Even enclosed shapes (circles, shields, folders) are drawn as open strokes rather than filled paths.
- **Naming convention**: Semantic kebab-case matching base English nouns (`search`, `shield`, `folder-lock`, `alert-circle`).

### Heroicons (MIT)
- **Multi-size strategy**: Distinct geometry crafted per display scale:
  - `24×24 Outline`: `1.5px` stroke with round caps/joins, optimized for spacious navigation and hero banners.
  - `20×20 Solid`: Filled silhouettes with strict negative-space carving, optimized for dense tables, dropdowns, and button groups.
  - `16×16 Micro`: Heavy silhouettes with simplified contours for badges and notification chips.
- **Design signature**: Refined line weight (`1.5px`) gives outline icons a lighter, more modern visual presence on high-DPI displays.

### Phosphor Icons (MIT)
- **Weight matrix**: Six cohesive weight variants sharing identical anchor points:
  - `Thin` (`1px` stroke)
  - `Light` (`1.25px` stroke)
  - `Regular` (`1.5px` stroke)
  - `Bold` (`2px` stroke)
  - `Fill` (solid silhouette)
  - `Duotone` (layered `opacity="0.2"` fill + `1.5px` stroke)
- **Design signature**: Unprecedented style consistency across diverse visual treatments, allowing dynamic switching between states (e.g. Regular default -> Fill on active/hover).

### Tabler Icons (MIT)
- **Base grid**: `24×24` with `2px` stroke.
- **Domain depth**: Uniquely rich coverage of technical, enterprise, developer tooling, and database concepts.
- **Design signature**: Geometric pragmatism. Heavy use of pure primitive combinations (circles, rounded rectangles, clean 45-degree diagonals).

### Radix Icons (MIT)
- **Base grid**: `15×15` pixel-snapped grid.
- **Target environment**: Component-level micro-UIs (menus, sliders, popovers, tabs).
- **Design signature**: Strict sub-pixel snapping. Because the grid is odd-numbered (`15px`), central elements sit directly on integer coordinates without half-pixel offsets.

---

## 3. What makes an icon set work: core consistency rules

When building custom icons or extending an existing pack, visual coherence is determined by five structural invariants:

### Rule 1: Fixed angle constraints
Arbitrary diagonal angles break set cohesion. Confine all diagonal paths to a restricted angle set:
- **Primary angles**: `45°` and `90°` (standard diagonals and orthogonal lines).
- **Secondary angles**: `30°` and `60°` (for isometric projections, isometric cubes, or stylized stars).
- Never draw arbitrary lines (e.g. `17°` or `53°`) unless physically required by a specific glyph's recognizable shape (such as a pencil or lightning bolt).

### Rule 2: Standardized corner radii
Every rounded vertex across the entire icon set must draw from a small token scale:
- **Sharp / Micro**: `0px` or `1px` (for tiny notch details).
- **Standard vertex**: `2px` (for corners of boxes, cards, folders).
- **Large enclosure**: `4px` or `6px` (for exterior pill cards and badges).
- Mixing arbitrary radii (e.g. `2px` on a folder, `5px` on a clipboard) makes icons look assembled from disparate sources.

### Rule 3: Uniform terminal caps
Path ends communicate personality:
- Use `stroke-linecap="round"` uniformly across all line icons.
- If opting for a technical / engineering aesthetic with `stroke-linecap="square"` or `butt`, apply it consistently to *all* glyphs in the project without exception.

### Rule 4: Consistent arrow anatomy
Arrows appear in dozens of glyphs (chevrons, external links, undo/redo, downloads, expanders). Standardize their physical dimensions:
- **Barb length**: exactly `5px` along the diagonal.
- **Barb angle**: strictly `45°` from the shaft.
- **Shaft thickness**: identical to the global stroke width.

### Rule 5: Negative space clearance
In filled and duotone variants, the white space between adjacent elements is as critical as the colored paths:
- Minimum negative gap: `2px` on a `24×24` grid; `1.5px` on a `20×20` grid.
- Never let an outline stroke merge into a solid mass without an intentional offset barrier.

---

## 4. Permissively licensed UI chrome and component systems

In addition to standalone icon packs, modern web applications compose icons within component containers whose architectural patterns derive from permissively licensed design systems:

| System | License | Upstream repository | Component patterns adapted |
|--------|---------|---------------------|----------------------------|
| **shadcn/ui** | **MIT** | `shadcn-ui/ui` | Segmented tabs, floating command palettes, badge variants, and semantic token bindings. |
| **Radix UI Primitives** | **MIT** | `radix-ui/primitives` | Accessible tablist/tab semantics, dialog overlays, status regions, and keyboard focus states. |
| **Shoelace** | **MIT** | `shoelace-style/shoelace` | Pill badge geometries, indicator status dots, and container elevation tokens. |

