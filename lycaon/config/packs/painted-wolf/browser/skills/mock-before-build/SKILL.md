---
name: mock-before-build
description: Mock up an unresolved visual direction for human choice; skip specified designs and routine or invisible edits.
license: Apache-2.0
metadata:
  author: painted-wolf
---

# Mock before build

1. **Entry check.** Confirm the direction is genuinely open. If the user already fixed the design, or the change is invisible (logic, tests, tooling), exit this skill and build directly. For a focused single screen or refinement, author mockups inline. When exploring divergent directions or prototyping multiple distinct screens concurrently, dispatch 2 or more parallel worker legs to author alternatives in parallel rather than authoring them serially.
2. **Select the canvas & theme.** Call `render_view` with `viewport.preset` matching target display. Presets enforce a minimum canvas height (desktop is 1280×720); for full-screen mockups (games, pages), make root markup fill the canvas (`width: 100%; min-height: 100vh` or fixed 16:9) without arbitrary `max-width`. Always set `theme: "dark"` for dark UIs so background margins do not render light beige. For standalone cards or components, set explicit `viewport: { width, height }` or `theme: "transparent"`. Keep aspect ratios consistent across related screens. Pass `handle: "<name>"` to retain for patching.
3. **Compose with design kit tokens.** Author offline HTML or SVG markup using host `--kit-*` CSS variables, `.kit-surface` cards, `.kit-media-16x9`, catalog fonts, and Lucide icons `<svg class="kit-icon"><use href="#kit-NAME"/></svg>`. No `<script>`, external links, or https URLs. Use `old_string`/`new_string` to iterate on a handle. For custom vector icons, badges, or chrome, follow `craft-icons-and-chrome`.
4. **Discover project assets.** Check the repository for existing brand images (SVG, PNG, WebP) or stylesheets and reference them offline using `http://lycaon.asset/<root-relative-path>` (e.g. `<img src="http://lycaon.asset/assets/logo.svg">`). Follow [project assets](references/project-assets.md) for discovery, restyling on the product's real stylesheet, and confirming `kit.assets`.
5. **Use realistic domain data.** Populate the layout with believable, context-specific entity names, numbers, timestamps, and active status badges. Never use "Lorem Ipsum" or generic placeholder text. Include distinct UI states (empty state, populated list, error badge) when comparing options.
6. **Handle render failures.**
   - On `RENDER_MARKUP_FORBIDDEN`: remove flagged external URLs, CDN links, or `<script>` tags; inline styles and use `#kit-*` icons.
   - On `RENDER_CANVAS_OVERFLOW`: switch tall single-column stacks into a multi-column grid or reduce container padding.
   - On `RENDER_KIT_UNKNOWN`: check the returned suggestions and substitute an allowed font family or icon name.
   - On `RENDER_HANDLE_NOT_FOUND`: supply initial `markup` along with `handle` to establish the canvas in session memory before patching.
   - On `RENDER_PATCH_NOT_FOUND` or `RENDER_PATCH_AMBIGUOUS`: include unique surrounding context in `old_string`, set `replace_all: true`, or supply full `markup` to overwrite.
7. **Return the mockup handoff.** Deliver:
   - `variants`: names and themes of rendered directions.
   - `decision_needed`: two to four explicit structural, layout, or hierarchy choices for the user to select. To put rendered variants in front of the human, follow [comparing options](references/compare-options.md).
   - `intent_notice`: state plainly that renders represent authored design intent for alignment, never observed runtime evidence.

Worked example:

```html
<div class="kit-surface" style="padding:var(--kit-space-4); max-width:480px; font-family:'Inter'">
  <header style="display:flex; justify-content:space-between; align-items:center; margin-bottom:var(--kit-space-3)">
    <h3 style="margin:0; font-size:var(--kit-text-lg); color:var(--kit-fg)">Active deployment</h3>
    <span style="background:var(--kit-accent); color:var(--kit-accent-fg); padding:2px 8px; border-radius:var(--kit-radius); font-size:var(--kit-text-xs)">Live</span>
  </header>
  <p style="color:var(--kit-muted); font-size:var(--kit-text-sm); margin:0 0 var(--kit-space-3) 0">Cluster us-east-2 · rev 4a9f12c</p>
  <div style="display:flex; gap:var(--kit-space-2)">
    <button style="display:inline-flex; align-items:center; gap:6px; background:var(--kit-bg); color:var(--kit-fg); border:1px solid var(--kit-border); padding:6px 12px; border-radius:var(--kit-radius); cursor:pointer">
      <svg class="kit-icon" aria-hidden="true"><use href="#kit-settings"/></svg> Configure
    </button>
  </div>
</div>
```

See [the kit cheatsheet](references/kit-cheatsheet.md) for tokens, fonts, icons, viewport presets, sandbox limits, and reject codes.
