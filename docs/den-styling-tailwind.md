# Den styling

Tailwind CSS v4 utility-first styling for the Den UI.

Tailwind v4 is configured in CSS only. `src/tailwind.css` opens with the layer order, imports Tailwind's `theme.css` and `utilities.css` around Den's own `element-reset.css` (Tailwind's preflight is not used), then imports the domain, utility, and chrome sheets in cascade order and, last, the `@theme` token bridge and shared `@utility` recipes under `src/styling/recipes/`. There is no `tailwind.config.*` and no `postcss.config.*`; Tailwind reaches the build as the `tailwindcss()` Vite plugin, ordered before `solid()` so class extraction sees authored JSX rather than compiled output. `styling-invariants.test.ts` fails the build on a config file or an inverted plugin order, because either one reintroduces a second place where styling truth can live.

Vite runs every stylesheet through Lightning CSS in development and in builds, targeting the WebKit of the oldest macOS the bundle supports (`vite.webview-targets.ts` reads `minimumSystemVersion` from `tauri.conf.json`; a minimum without a known WebKit version fails the config). Authored CSS writes the standard property for everything the pipeline prefixes: the dev server and the packaged app both add `-webkit-user-select`, `-webkit-box-decoration-break`, `-webkit-text-decoration`, `-webkit-mask-*`, and `-webkit-backdrop-filter` where WKWebView still needs them, and `styling-invariants.test.ts` rejects those prefixes hand-written. `-webkit-appearance` is not in that set and is authored directly.

**See also:** [Den](den.md) · [Docs map](README.md)

**Machine truth:** `src/test/style-contracts/` · `src/styling/styling-invariants.test.ts`

---

## Measured invariants

| Metric | Value |
|--------|-------|
| `global.css` Den BEM selectors (`__` / `--`) | **0** (`countBemSelectors`) |
| `@apply` in Den CSS | **0** |
| Bare layout rules in domain CSS | **0** (`findBareLayoutDomainRules`) |
| `@keyframes` in `*-utilities.css` | **0** (`findKeyframesInUtilitiesCss`) |
| Undefined `--radius-den-*` references | **0** (`findUndefinedThemeRadiusReferences`) |
| Stylesheet imports outside `index.tsx` | **0** (`findUnexpectedComponentCssImports`) |
| Pseudo-element rules keyed on every element (`*::before`, `::after`) | **0** (`findUniversalPseudoElementRules`) |

The contract functions live in `src/test/style-contracts/`; the test file calls them.

Domain CSS has no per-file line ceiling: it is bounded by what a rule does. Group retained rules by feature or responsibility, preserving cascade order. Entry stylesheets declare ordered imports, semantic checks follow those imports, and every domain fragment must remain reachable from `tailwind.css`. Moving a block to shorten a file does not improve its organization.

Layout, spacing, and type for each surface live in companion `*-utilities.css` files imported from `tailwind.css`.

**Two suffixes are domain CSS.** `readDenStylesheetInventory` classifies both `*-domain.css` and `*-art.css` as domain sheets, so `chat-art.css` carries every domain invariant. The `-art` name says the sheet holds pseudo-element artwork, not that it is exempt.

Some plain `.den-*` selectors still sit in `*-utilities.css`. They are cascade-sensitive: `tailwind.css` imports `shell-utilities.css` near the end of the domain block, after `global-components.css` and its own `shell-domain.css`, so those rules deliberately outrank both. Reordering that block is a behavior change, and those rules need per-rule review rather than a blanket sweep.

---

## Stylesheet inventory

| File | Role |
|------|------|
| `src/tokens.generated.css` | Brand colors, light/dark `@media (prefers-color-scheme: dark)` — **generated**, see [theme-tokens.md](theme-tokens.md) |
| `src/tokens-derived.css` | Tokens computed from the generated set |
| `src/fonts/font-faces.generated.css` | `@font-face` for the bundled families — **generated** |
| `src/tailwind.css` | Ordered Tailwind entry and feature imports |
| `src/styling/recipes/` | Theme bridge (`theme.css`) and shared `@utility` recipes grouped by surface |
| `src/element-reset.css` | Element reset in `@layer base`, imported from `tailwind.css` in place of Tailwind preflight; never selects a pseudo-element on every element |
| `src/global.css` | Residual plain CSS: `:root` derived tokens, `@property` registrations, keyframes, and the `@layer base` element resets; zero Den BEM |
| `src/global-components.css` | Ordered entry for unlayered component chrome in `src/styling/chrome/` |
| `src/inline-vocabulary.css` | The two inline leaf shapes: `den-inline-control`, `den-status-mark` |
| `src/styling/ui-chrome.css` | Selection behavior for the `data-den-chrome` / `data-den-prose` marks and shared chrome states, paired with `ui-chrome.ts` ([accessibility.md](accessibility.md#macos-spoken-content-speak-selection--speak-under-pointer)) |
| `src/*-domain.css` | Retention-boundary rules, or ordered entries for feature fragments under `src/styling/` |
| `src/*-art.css` | Domain sheet for pseudo-element artwork (`chat-art.css`), same invariants as `*-domain.css` |
| `src/*-utilities.css` | Per-domain `@utility` recipes, or ordered entries for feature fragments under `src/styling/` |
| `src/diffs-page.css` | The Diffs page frame |
| `src/nav-brand-titlebar.css` | Brand titlebar over the nav rail, after `shell-utilities.css` in the cascade |
| `src/file-edit-diff.css` | `.den-file-edit-diff*` rules for `FileEditDiff.tsx`, last in the domain block |
| `src/markdown.css` | `.markdown-body` prose for assistant and agent output |
| `src/components/source/reader/source-reader.css` | Source reader frame, actions row, and editor panes |
| `src/platform/themed-scrollbars.css` | OverlayScrollbars integration; its `[data-den-scrollport]` host rule sits in `@layer components` so unlayered frame classes size and place the box |
| `src/components/home/home-alignment.css` | Home's top inset, aligning its composer stack with the sidebar chrome row |
| `src/accessibility-appearance.css` | Display preferences the host stamps on the root, such as reduced transparency |

### Import order (`index.tsx`)

```text
overlayscrollbars.css → tokens.generated.css → tokens-derived.css
  → fonts/font-faces.generated.css → tailwind.css → global.css
  → components/source/reader/source-reader.css → markdown.css
  → platform/themed-scrollbars.css → components/home/home-alignment.css
  → accessibility-appearance.css
```

`tailwind.css` pulls in every domain, utilities, and chrome sheet in cascade order. Large families use an entry sheet that only lists ordered `@import`s; the rules live in fragments under `src/styling/`:

| Entry | Fragments |
|-------|-----------|
| `tailwind.css` (recipe block) | `styling/recipes/`: the `@theme` bridge and shared `@utility` recipes, including the `btn-*` recipes in `buttons-utilities.css` |
| `global-components.css` | `styling/chrome/` |
| `shell-domain.css` | `styling/shell/` |
| `chat-utilities.css` | `styling/chat/` |
| `tool-utilities.css` | `styling/tools/` |
| `workflow-chrome-utilities.css` | `styling/workflow/` |
| `search-domain.css` | `styling/search/` |
| `scans-domain.css` | `styling/scans/` |
| `files-domain.css`, `files-utilities.css` | `styling/files/` |

A fragment keeps its entry's role and import position. Source checks follow the imports and inspect each physical definition once. Only `index.tsx` imports stylesheets; ordinary component styling belongs in JSX utilities, not a component-local stylesheet.

### Resets live in `@layer base`

An unlayered rule beats every layered one regardless of source order, so a bare element reset in `global.css` would sit permanently above the `@utility` recipes meant to style that element: a `btn-primary` background loses to a bare `button { background: … }` no matter how the sheets are ordered, and the utility is present in the output and simply never wins.

Every element reset therefore lives in `@layer base`: `element-reset.css` (imported with `layer(base)`) carries the box model, typography, and form-control baseline, and the `@layer base` blocks in `global.css` cover the `button` reset, the `:focus-visible` ring, and the text-entry fills. Layering them beneath the unlayered component sheets lets `@utility` recipes and field primitives paint over them, and lets a field opt out of the global ring. `styling-invariants.test.ts` asserts the `button` and `:focus-visible` rules sit inside `@layer base` and that neither reappears at top level.

The mirror-image failure has its own guard: an `@utility` declaration in the shadow of an unlayered rule is unreachable, and `findLayerShadowedUtilities` fails the build rather than letting a dead recipe look live.

### No pseudo-element rule on every element

`element-reset.css` replaces Tailwind preflight because preflight opens with `*, ::after, ::before, ::backdrop, ::file-selector-button { … }`. WebKit resolves a pseudo-element's style for every element once any rule keys that pseudo-element on `*`, so one universal `::before` rule makes every style recalculation resolve three styles per element instead of one. Measured in WebKit on the Files stage with an editor open (2,250 elements), a full recalculation took 52ms with the universal rules and 26ms without, and the same halving applies to every scroll frame, file open, and tree expansion. Chromium does not pay this cost, which is why it only shows in the shipped macOS app.

`findUniversalPseudoElementRules` fails the build on any selector whose subject compound is only `::before` or `::after` (`*::after`, `::before`, `.x > ::after`); other pseudo-elements such as `::placeholder` measured no eager cost. Key pseudo-element rules on the element that owns them: `.den-x::before`, `input::placeholder`, `&::after` inside a nested block. A pseudo-element that sizes itself with a border or padding sets `box-sizing: border-box` in its own rule, since the reset's `*` does not reach pseudo-elements.

---

## Retention boundary

CSS files keep only what Tailwind utilities cannot express:

| Keep in CSS | Examples |
|-------------|---------|
| Keyframes / reduced-motion | `notice-in`, spin, `den-drawer-in` |
| Pseudo-elements that need art | carets, send arrow, thinking dots |
| Vendor / third-party overrides | OverlayScrollbars |
| Complex shell chrome | drag regions, tab bleeds, nav resize |
| Markdown prose | `.markdown-body` |

Everything else is Tailwind utilities or shared `@utility` / `DenButton` primitives.

Authoring rules: `@utility` only for genuinely shared clusters of three or more properties (tints, button variants); no `den-*` class that only carries `display` / `gap` / `padding` / `color`; no Den `__` / `--` BEM blocks in `global.css`; update Vitest and Playwright class assertions in the same change that renames JSX classes.

---

## Tokens and primitives

Brand tokens are generated into `tokens.generated.css` from the host theme units and bridge into Tailwind via `@theme` in `styling/recipes/theme.css`: edit the unit and rerun the codegen, never the sheet ([theme-tokens.md](theme-tokens.md)). Prefer token-backed utilities over raw hex in components.

### The `@theme` bridge

`@theme` is the only door between the generated `--den-*` tokens and Tailwind's utility namespace: a component writes `bg-den-surface` or `text-den-body`, and that class exists only because a key in this block declares it. `REQUIRED_THEME_COLOR_KEYS` / `REQUIRED_THEME_SPACING_KEYS` / `REQUIRED_THEME_TEXT_KEYS` in `test/style-contracts/theme.ts` are the enforced lists.

**Colors bridge by reference.** Every `--color-den-*` entry is `var(--den-…)`, never a literal; a hex here would fork the palette away from the theme unit and stop following light/dark. A theme may add roles, but this semantic floor must always resolve, because component code and the shared recipes assume it:

`--color-den-bg` · `--color-den-surface` · `--color-den-border` · `--color-den-text` · `--color-den-text-muted` · `--color-den-accent` · `--color-den-accent-text` · `--color-den-danger`

**Spacing and type are literal `rem`, and the rung set is closed.** `--spacing-den-1…9` and the `--text-den-*` ladder (`micro` through `banner`) are declared as bare `rem` numbers, not `var()` and never `px`. The root font size carries the composed accessibility text scale, so a `px` rung would freeze that surface at scale 1 while everything around it grew ([accessibility.md](accessibility.md)), and a `var()` rung would resolve against a token that no longer participates in the ladder's `1rem` baseline. Unlike colors, these two sets are checked for exact equality: adding a rung means adding it to the constant in the same change, which is the point at which someone asks whether the ladder needs another rung at all.

**Radius bridges only the three concentric rungs**, `--radius-den-sm`, `--radius-den`, `--radius-den-lg`, and `findUndefinedThemeRadiusReferences` fails any `--radius-den-*` reference outside them, so the corner ladder below cannot be extended by accident from a component.

**Type rule:** every font size resolves to a `--text-den-*` rung. A px font size does not follow the OS text scale.

**Fallback rule:** `var(--t, x)` earns its second arm only where `--t` can be unset at the reader: a chat header reading a macOS-only clearance, a base rule reading a tone its variants set, a rule reading a JS-published extent before the first measure. A token every reader inherits unconditionally resolves everywhere, so a fallback on one is dead.

**Hairline rule:** `--den-line` draws the edge of a region; `--den-line-quiet` draws the rules between repeating siblings inside one, so a group's outline reads louder than its own rows. A control whose ring is its only affordance takes `--den-control-line` instead ([accessibility.md](accessibility.md)).

**Corner rule** (`global.css` radii): one concentric ladder picked by nesting depth, so nested corners stay concentric and a corner says how deep the reader is.

| Rung | Token | Examples |
|------|-------|----------|
| Window | `--den-radius-shell` (`0`) | shell frame, list panels, prefs bands, tables, snippet/diff shells |
| Panel · dialog · bubble | `--den-radius-lg` | drawers, dialogs, the user prompt bubble |
| Card · popover · notice | `--den-radius` | run cards, posture cards, popovers, anything holding its own controls |
| Row · input · block button · status mark | `--den-radius-sm` | list rows, inputs, `btn-*`, badges |
| Inline control | `--den-radius-pill` | header chips, attachment chips, inline toggles |

The pill is the only rung that rounds all the way, and that break is the affordance: an element that holds no other content and is drawn as a pill is something you can act on. Anything holding content is a surface and takes its own rung, which is why the user bubble is rounded and inert, and why a notice carrying a button is a card rather than a large chip.

Radius alone cannot separate a control from a label, since `btn-*` shares `--den-radius-sm` with badges. Two absences do: a status mark has no border and no hover response. Both leaf shapes live in `src/inline-vocabulary.css`, `den-inline-control` and `den-status-mark`, with tone set by `data-tone` so a caller states a fact and never picks a colour. Three rules follow:

- **One class, one tag.** A class that renders a `button` never also renders a `span`. A disclosure capable of holding content is a surface even while collapsed; opening it does not turn a control shape into a container shape.
- **Legible at rest.** A difference that only appears on `:hover` is not a difference: touch never hovers. `cursor: pointer` alone is not an affordance.
- **A control holds nothing round.** Counts and labels inside a button are plain text; a rounded fill inside a target reads as a second, smaller target.

Two shapes sit off the ladder because they are not boxes: a circle (dot, glyph badge) uses `50%`, and a bar or track (progress, meter) rounds its ends with a bare `999px`. A bare `999px` on something holding text is the drift signal: that is a pill, and a pill is a control.

During measured open/close motion, CSS still defines the corner. `ui/height-toggle-motion.ts` samples the computed radius before disclosure state changes and applies `overflow: hidden` plus a rounded `clip-path` until the height and body state settle. Height is the only geometry that primitive changes: a collapsible shell resolves to the same radius open and closed, and collapsible surfaces never pass radius tokens or animate height directly. The primitive also owns the reduced-motion and no-animation path and declares every change to the containing scrollport, so callers have no motion branch of their own. A surface whose height follows changing content rather than toggling uses `followBodyHeight` from the same module: the shell has no padding and bottom-aligns a single body, `change` pins the rendered height before a synchronous edit reaches layout, and the only observation is the shared resize observer, which retargets a running motion when the body changes underneath it. That shell clips with `overflow: clip` rather than a clip path, so its focus ring and shadow stay visible while it moves. A surface keeps its background, edge, and corner on the same shell across states; moving that chrome between a wrapper and a child creates an unstyled transition frame.

Shared interactive chrome uses Solid primitives rather than one-off CSS button blocks:

- `DenSelect` implements single-choice popup paint, placement, focus, typeahead, and listbox keyboard behavior. It is intrinsic-width by default; a caller opts into a stretched trigger with a layout class such as `w-full`, while the popup stays content-sized and never narrower than its trigger.
- `DenTriggerGroup` merges a run of popup triggers into one recessed track. A trigger inside it drops its own box and renders as a bare segment, because the seam dividing neighbours resolves on DOM adjacency; the raise marks the open popup, never a chosen member, and the track holds one tab stop ([Merged trigger groups](den.md#merged-trigger-groups)).
- `DenCheckbox` / `DenRadio` keep native form state and grouping underneath themed marks.
- `DenNumberInput` is the only component that renders `type="number"`; `DenInput` does not accept that type. It preserves numeric validation and arrow-key semantics while painting the steppers. Search decorations, autofill paint, and textarea resize affordances are normalized globally. File inputs remain hidden behind an explicit app control.
- A content-rich picker may define its popup layout, but must provide the same dismissal, focus return, arrow, Home/End, and activation behavior as the shared primitives.

`styling/native-chrome-invariants.test.ts` prevents native select DOM and unowned checkbox, radio, number, or visible file controls from appearing.

### OS light / dark

Scheme is stamped as `data-den-appearance="light|dark"` on `<html>`, never `class="dark"`. Settings → Display picks the mode (`system`/`light`/`dark`) and the theme per scheme; `@media (prefers-color-scheme: dark)` covers only the instant before the stamp lands.

| Runtime | Authority |
|---------|-----------|
| Tauri desktop | `getCurrentWindow().setTheme(null)` (follow system), seed `theme()`, live `onThemeChanged` → `theme.ts` `reconcileOsColorScheme` (`platform/windows/window-chrome.ts`) |
| Web / harness | `matchMedia('(prefers-color-scheme: dark)')` via `syncOsColorScheme` |

Do not drive `setTheme("light"\|"dark")` from matchMedia; that locks the window off system follow.

### Live layout geometry

A custom property inherits, so one that changes every frame of a drag invalidates style for the whole subtree under the element carrying it.

Rules for any property holding a width, height, or offset that moves during a resize:

| Rule | Detail |
|------|--------|
| One authority per question | CSS resolves against the live box. JS publishes intent (a mode, a ratio), not a measured width that decides what renders |
| Register it | `@property { syntax: "*"; inherits: false }` in `global.css`. The universal syntax is required: a typed syntax needs an `initial-value`, and a registered property with one is never guaranteed-invalid, which silently disables every `var(--x, fallback)` reading it |
| Declare it on the reader | Every reader sits on the declaring element. A descendant or pseudo-element falls back instead of inheriting |
| Never read layout on demand | Measure containers with `layout/element-extent.ts` (ResizeObserver-backed signals). `clientWidth` from a clamp callback or an `aria-valuemax` render effect forces layout mid-drag, once per `pointermove` |
| Never track what you write | An effect that reads the signal its own `ResizeObserver` writes re-runs on every write and rebuilds the observer |
| Stand down while busy | Opportunistic measurement gates on `isShellLayoutBusy()` and pairs with `onShellLayoutSettled()` |
| Keep chrome live | Window resize publishes viewport width once per frame, and split-host width publishes on the next frame while a split drags; only opportunistic extents wait for settle |
| Isolate columns | `.den-split-col` uses `contain: layout style`. `paint` is omitted so portaled chrome can hang out |
| One observer | Opportunistic content-box reads go through `layout/shared-resize-observer.ts` and deliver once at rest |

Proof: `styling/resize-invalidation-invariants.test.ts` (registration, reader placement, epoch freeze, and a drift guard that fails any new px-valued custom property written from a component), `layout/element-extent.test.ts`, `layout/shared-resize-observer.test.ts`, `shell/shell-layout-busy.test.ts`, `shell/window-resize.test.ts`.

---

## Invariants

**`src/styling/styling-invariants.test.ts` is the list.** Its `it(…)` titles are the roster, about fifty of them across `Tailwind adoption invariants`, `utility-first adoption ratchets`, and the helper suite, plus the sibling suites named through this page (`native-chrome-invariants`, `resize-invalidation-invariants`, `spoken-content-a11y`, `focus-motion-a11y`). Read that file for the complete set; this page explains why the rules exist.

| Category | What it protects | Examples |
|----------|------------------|----------|
| One place per fact | A style truth cannot live in two files that can disagree | No `tailwind.config.*` / `postcss.config.*`; no `@apply`; zero BEM in `global.css`; every `@utility` hook defined exactly once; every `btn-*` hook a component uses resolves through `tailwind.css` |
| Retention boundary | A rule sits in the sheet class that matches what it is | No bare layout rule in domain CSS; no `@keyframes` in `*-utilities.css`; no component-local CSS import; no forbidden bare selector root in domain CSS |
| Cascade reachability | A declaration that cannot win is a bug, not a preference | Resets inside `@layer base`; no `@utility` in the shadow of an unlayered rule; no box contest between a `DenButton` class and its recipe |
| Name parity | A rename lands on both sides or the styling silently detaches | No orphaned `den-`/`btn-` domain selectors; every `den-`/`btn-` hook in JSX resolves to CSS or an `@utility`; a hook that exists as `den-{hook}` in CSS carries that name in JSX |
| Tokens and copy | A value routes through the theme, and text reads as product copy | No hardcoded hex in domain/art CSS, including inside a `var()` fallback; literal `rem` in `@theme`; every animation name resolves to a `@keyframes`; reserved BEM prefixes stay out of hooks; user-facing strings stay sentence case |

A failure names the file and line. Fix the rule at that site; do not widen the matcher.
