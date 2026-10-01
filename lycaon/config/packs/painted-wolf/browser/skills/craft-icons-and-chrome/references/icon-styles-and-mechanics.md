# Icon styles and mechanics

This guide details the technical, geometric, and optical standards required to design cohesive vector icon sets for web user interfaces.

## 1. Grid systems and keylines

Consistent icon sets share a common bounding box, live area, and keyline geometry. Designing on a grid ensures uniform visual weight across different glyph shapes.

### Standard grids

| Grid dimension | Live area | Padding | Primary application | Recommended stroke |
|---|---|---|---|---|
| `24×24` | `20×20` | `2px` | Primary navigation, headers, button icons | `2px` (standard) or `1.5px` (refined) |
| `20×20` | `16×16` | `2px` | Compact toolbars, dense table rows, dropdown items | `1.5px` or solid fill |
| `16×16` | `14×14` | `1px` | Micro-indicators, badge adornments, tree views | `1px` or solid fill |

### Keyline shapes (24×24 grid)

Keylines balance the perceived area of different geometric primitives:
- **Circle**: diameter `20px` (centered at `12, 12`). Circles require the largest bounding diameter because curves cut away corner mass.
- **Square**: `16×16px` (centered at `12, 12`, corners at `4, 4` to `20, 20`). Sharp corners carry maximum visual weight.
- **Horizontal rectangle**: `18×14px` (wide assets like files, landscape cards).
- **Vertical rectangle**: `14×18px` (tall assets like mobile devices, documents).

Aligning icons to these keylines ensures that a circle icon (e.g. `settings` or `user`) does not feel smaller or larger than a square icon (e.g. `folder` or `stop`).

## 2. Optical balance versus geometric center

Pure mathematical centering often produces icons that look visually displaced.

- **The Play Button / Triangle**: A right-pointing equilateral triangle centered on its bounding box appears shifted to the left because its visual mass is concentrated in the left half. Shift the horizontal center approximately `1px` to `1.5px` to the right to achieve optical equilibrium.
- **Arrows and Chevrons**: Symmetrical chevrons (e.g. pointing up) carry more weight at the apex. Keep apex points on integer pixel centers to prevent split-pixel blurring.
- **Baseline text alignment**: When placing an icon inline with text, center the icon vertically against the font's x-height or cap-height rather than the entire line-height box.

## 3. Style taxonomy

### Linear outline (Monoline)

The most popular modern web interface icon style (e.g. Lucide, Feather, Heroicons Outline).

- **Stroke properties**:
  - `stroke-width`: `2px` (or `1.5px` for lighter density).
  - `stroke-linecap="round"`: softens path endpoints and prevents harsh square stubs.
  - `stroke-linejoin="round"`: creates uniform curved vertices.
- **Scaling rule**: Add `vector-effect="non-scaling-stroke"` when icons need to scale without line width swelling.
- **Color inheritance**: Use `stroke="currentColor"` and `fill="none"` to allow immediate theme color propagation.

```xml
<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
  <path d="M12 2v20M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6"/>
</svg>
```

### Solid silhouette

Provides high contrast and immediate readability at small physical sizes (e.g. Heroicons Solid, Phosphor Fill).

- **Negative space carving**: When separating adjacent elements (such as an arrow crossing a circle), carve out a gap of at least `2px` to preserve distinction at 1x resolution.
- **Single-path compound paths**: Use SVG winding rules (`fill-rule="evenodd"`) to combine outer contours with inner holes in a single `<path>` element.
- **Avoid thin spurs**: Ensure no feature narrows below `1.5px`, which degrades into a faint gray blur on standard displays.

```xml
<svg width="20" height="20" viewBox="0 0 20 20" fill="currentColor" aria-hidden="true">
  <path fill-rule="evenodd" d="M10 2a8 8 0 1 0 0 16 8 8 0 0 0 0-16Zm1 11H9v-2h2v2Zm0-4H9V5h2v4Z" clip-rule="evenodd"/>
</svg>
```

### Duotone / Two-tone

Adds visual depth and hierarchy while preserving line clarity (e.g. Phosphor Duotone).

- **Secondary backdrop**: A filled silhouette layer set to `fill="currentColor"` with `fill-opacity="0.2"` (or `0.15` in dark mode).
- **Primary foreground**: Crisp outline paths or solid accents at `opacity="1.0"`.
- **Layer ordering**: Always place the low-opacity fill beneath the stroke paths to prevent dark overlapping outlines.

```xml
<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
  <path d="M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z" fill="currentColor" fill-opacity="0.2" stroke="none"/>
  <rect x="2" y="4" width="20" height="16" rx="2"/>
  <path d="m22 7-8.97 5.7a1.94 1.94 0 0 1-2.06 0L2 7"/>
</svg>
```

### Badged and enclosed containers

Used for app tiles, status indicators, and notification-bearing avatars.

- **Container shapes**: Squircles (super-ellipses), circles, or rounded rectangles with consistent corner radius (typically `4px` to `8px`).
- **Offset cutout ring**: When placing a status dot or counter badge on top of a container, create an outer ring with the background color (`stroke="var(--kit-bg)" stroke-width="2px"`) to visually detach the badge.

```xml
<svg width="24" height="24" viewBox="0 0 24 24" fill="none" aria-hidden="true">
  <rect x="2" y="2" width="20" height="20" rx="6" fill="var(--kit-surface)" stroke="var(--kit-border)" stroke-width="1.5"/>
  <path d="M8 12h8M12 8v8" stroke="var(--kit-fg)" stroke-width="2" stroke-linecap="round"/>
  <circle cx="19" cy="5" r="3" fill="var(--kit-accent)" stroke="var(--kit-bg)" stroke-width="1.5"/>
</svg>
```

### Rich, elevated, and skeuomorphic vector styling

For landing pages, hero features, or rich desktop toolbars where flat geometry is insufficient:
- **Subtle linear gradients**: Use 10% to 15% gradient shifts along the vertical axis (lighter at the top, deeper at the bottom) to simulate natural overhead lighting.
- **Inner highlights**: Layer an inset border or highlight path (`rgba(255, 255, 255, 0.15)` top edge) to produce crisp glass-like edges.
- **Ambient + key drop shadows**: Use dual shadows (`drop-shadow(0 1px 1px rgba(0,0,0,0.08)) drop-shadow(0 4px 6px rgba(0,0,0,0.05))`) for natural elevation.

## 4. Vector path economy

High-performance web UIs demand compact, clean SVG geometry:
1. **Snap to integer pixels**: Align horizontal and vertical lines to full pixel values (e.g. `x="4" y="8"`) or exact half-pixels when centering odd-width strokes (e.g. `1px` stroke centered on `x="4.5"`).
2. **Combine subpaths**: Merge disconnected strokes into a single `<path>` element using space-separated commands rather than multiple `<line>` or `<circle>` elements where practical.
3. **Limit precision**: Avoid excessive decimals resulting from vector tool exports (e.g. `12.00004` -> `12`).
4. **Remove hidden artifacts**: Eliminate zero-length line caps, redundant `clipPath` definitions, and empty group `<g>` tags.
