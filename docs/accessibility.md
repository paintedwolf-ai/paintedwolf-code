# Accessibility (macOS)

The Den chrome accessibility contract for human users on macOS: OS text-size scaling, layout resilience, VoiceOver semantics, Spoken Content, hover tips, focus, and reduced motion. There is no Linux or Windows system text-size bridge.

**See also:** [Den](den.md) · [Keyboard shortcuts](keyboard-shortcuts.md) · [In-view find](den-in-view-find.md) · [Dev tasks](dev-tasks.md#den-harness-llm-drivable-stack)

**Machine truth:** [`platform/accessibility-macos-invariants.test.ts`](../lycaon-den/src/platform/accessibility-macos-invariants.test.ts) · [`platform/accessibility-axe-smoke.test.tsx`](../lycaon-den/src/platform/accessibility-axe-smoke.test.tsx) · [`styling/spoken-content-a11y.test.ts`](../lycaon-den/src/styling/spoken-content-a11y.test.ts) · [`styling/focus-motion-a11y.test.ts`](../lycaon-den/src/styling/focus-motion-a11y.test.ts) · [`platform/desktop/accessibility-text-size.ts`](../lycaon-den/src/platform/desktop/accessibility-text-size.ts) · [`ui-chrome.ts`](../lycaon-den/src/styling/ui-chrome.ts) · [`global.css`](../lycaon-den/src/global.css)

## Scope

Accessibility work enables assistive features on the shipped product. It is not a redesign of look, density, or functionality: add semantics, names, live regions, focus traps, rem scaling, and min-heights that prevent clipping; do not restyle the shell, restack IA, invent an "a11y theme," or simplify density. Grow hit targets only as scale or the 24 px floor requires. Honor `prefers-reduced-motion` by disabling non-essential motion without removing product motion for everyone else. Reuse existing commands and flows rather than adding parallel a11y-only paths.

On macOS, Den follows System Settings → Accessibility → Display → **Text size**. Settings → Display also offers a device **Text size** preference; it multiplies the OS scale rather than replacing it, so a reader who set the system slider keeps that setting and can still ask this one app for more. VoiceOver and Spoken Content work on the real chat transcript without a special mode.

## macOS text-size signal

Accessibility → Display → Text size is read from the Sonoma+ content-size category. `NSFont.preferredFont(forTextStyle:)` and `NSAccessibilityPreferredTextAttributesChangedNotification` do not track it and are not the scale source. Implementation: [`accessibility_text_size.rs`](../lycaon-den/src-tauri/src/accessibility_text_size.rs).

| Item | Contract |
|------|----------|
| Primary source | `UserDefaults.standard` string key `UIPreferredContentSizeCategoryName` (`UICTContentSizeCategory*` values) |
| Fallback source | `UserDefaults(suiteName: "com.apple.universalaccess")` → `FontSizeCategory` → `global` (`DEFAULT`, `L`, `AX1`, …) when the primary key is absent |
| Change notification | Distributed notification `com.apple.PreferredContentSizeCategoryChanged`; KVO on the fallback suite when using `FontSizeCategory` |
| Scale formula | `--den-text-scale = TABLE[category] × productTextScale`: the OS category scale (default / Large → `1.0`, the Den baseline) times the device Text size preference (`PRODUCT_TEXT_SCALES`, default `1`), composed in [`accessibility-text-size.ts`](../lycaon-den/src/platform/desktop/accessibility-text-size.ts); either input may move without the other |
| Root CSS | `html { font-size: calc(14px * var(--den-text-scale)) }` with `:root { --den-text-scale: 1; }` |
| Type tokens | `@theme --text-den-*` in rem, where `1rem` is the baseline root at scale 1 (14px). Every font size resolves to a rung (`accessibility-macos-invariants.test.ts`) |
| Transcript seams | The four rungs in [`transcript-spacing.ts`](../lycaon-den/src/chat/transcript/layout/transcript-spacing.ts) are rem for the same reason: a px seam holds 14px while the text around it triples, so a turn boundary vanishes where it is needed most. Virtual geometry resolves them against one published root size (`styling/scalable-layout-invariants.test.ts`) |
| Delivery | Tauri event and the `accessibility_preferred_text_size()` command return `{ category, textScale, revision }`; the monotonic revision prevents an older launch snapshot from overwriting a live change |
| Non-macOS | The bridge returns `{ category: "default", textScale: 1, revision: 0 }`; the app runs unscaled |

**Category → scale table** (same numbers in Rust and the Den fixture). Scales are the published Dynamic Type body point sizes ÷ 17 (Large body = 17pt → `1.0`). No cap is applied to `--den-text-scale`; layout resilience sets max-category chrome.

| `UIPreferredContentSizeCategoryName` | `FontSizeCategory.global` alias | Body pt | `textScale` |
|---|---|---|---|
| `…XS` | `XXXS`, `XXS` | 14 | `14/17` ≈ 0.8235 |
| `…S` | `XS` | 15 | `15/17` ≈ 0.8824 |
| `…M` | `S` | 16 | `16/17` ≈ 0.9412 |
| `…L` | `DEFAULT`, `M` | 17 | **`1.0`** |
| `…XL` | `L` | 19 | `19/17` ≈ 1.1176 |
| `…XXL` | `XL` | 21 | `21/17` ≈ 1.2353 |
| `…XXXL` | `XXL` | 23 | `23/17` ≈ 1.3529 |
| `…AccessibilityM` | `XXXL`, `AX1` | 28 | `28/17` ≈ 1.6471 |
| `…AccessibilityL` | `AX2` | 33 | `33/17` ≈ 1.9412 |
| `…AccessibilityXL` | `AX3` | 40 | `40/17` ≈ 2.3529 |
| `…AccessibilityXXL` | `AX4` | 47 | `47/17` ≈ 2.7647 |
| `…AccessibilityXXXL` | `AX5` | 53 | `53/17` ≈ 3.1176 |
| missing / unknown / `"default"` | unknown | — | **`1.0`** |

The maximum harness inject is the `AccessibilityXXXL` scale (`53/17`), used by the layout proof when the OS slider cannot be driven.

```text
app launch / OS text-size change
  → read UIPreferredContentSizeCategoryName (fallback FontSizeCategory.global)
  → map category → textScale via the table
  → publish { category, textScale, revision }
  → Den subscribes, then hydrates; it applies only the newest revision
  → --den-text-scale = that scale × the device Text size preference
  → rem type tokens rescale; layout pass prevents clip
```

## macOS display preferences

Accessibility → Display also carries **Increase contrast** and **Reduce transparency**, and Appearance carries the accent colour. The shipped web view does not expose these to CSS dependably, so the host reads all three and Den follows host facts rather than media queries ([`system_appearance.rs`](../lycaon-den/src-tauri/src/system_appearance.rs), [`system-appearance.ts`](../lycaon-den/src/platform/desktop/system-appearance.ts)).

| Item | Contract |
|------|----------|
| Source | `NSWorkspace.accessibilityDisplayShouldIncreaseContrast` / `…ShouldReduceTransparency`; `NSColor.controlAccentColor` converted to sRGB |
| Accent opt-out | Multicolor writes no `AppleAccentColor` in the global domain. With the key absent the theme keeps its own accent |
| Change notification | `NSWorkspaceAccessibilityDisplayOptionsDidChangeNotification` on the workspace center; `NSSystemColorsDidChangeNotification` on the default center |
| Delivery | Event `appearance://system-changed` and command `system_appearance()` return `{ increaseContrast, reduceTransparency, accent, revision }`; the monotonic revision rejects stale snapshots |
| Root CSS | `data-den-contrast="more"` and `data-den-transparency="reduced"` on `:root` |
| Increase contrast | Theme tokens re-derived: `text-muted` and `accent-text` to 7:1, `border` to 3:1 against the theme background ([`theme-adjustments.ts`](../lycaon-den/src/contributions/theme-adjustments.ts)) |
| Reduce transparency | Blurred chrome drops `backdrop-filter` and paints its opaque surface token ([`accessibility-appearance.css`](../lycaon-den/src/accessibility-appearance.css)) |
| Accent | The theme compiler's accent family is rebuilt from the chosen colour, so hover, label ink, signal, and selection stay legible in the active theme |
| Non-macOS | The web build falls back to `prefers-contrast: more` and `prefers-reduced-transparency: reduce`; no accent is read |

Adjustments apply where the theme applies: theme tokens land as inline custom properties on the root, which a later stylesheet could only beat with `!important`.

**Proof:** `contributions/theme-color.test.ts` (colour math against the compiler's values), `contributions/theme-adjustments.test.ts` (the contrast floors), `platform/desktop/system-appearance.test.ts` (root stamping, stale snapshots, one notification per change).

## Layout resilience rules

At the maximum text-size category (or the harness inject) and the Tauri minimum window width of 720px:

- Text-bearing rows use `min-height` in `em`/`rem`, never a fixed `px` height.
- A glyph sized from the type ladder sits in a `rem` box; a `px` box clips it.
- Interactive controls have a hit target of at least 24×24 CSS px (WCAG 2.2 SC 2.5.8), growing with text via `em` when larger.
- Tab chips, nav sessions, and tool card headers may grow vertically; prefer wrap or ellipsis over clip.
- The composer aligns `flex-end` with a textarea `min-height` in `em`.
- Sticky shell and composer chrome must not obscure focused controls (WCAG 2.2 SC 2.4.11).
- Forbidden fix: `overflow: hidden` on a row to hide clipped text instead of raising its min-height.

## VoiceOver contract

| Surface | Requirement |
|---------|-------------|
| Shell | `<nav aria-label="Main">`; `<main>` around chat and stage; drawers carry `role="dialog"` with an `aria-labelledby` title, whether hosted in the stage ([`ContextDrawer.tsx`](../lycaon-den/src/components/shell/ContextDrawer.tsx)) or overlaid ([`DiffViewerDrawer.tsx`](../lycaon-den/src/components/source/diff/DiffViewerDrawer.tsx)) |
| Transcript | One stable conversation container ([`ChatSpanBlocks.tsx`](../lycaon-den/src/components/transcript/ChatSpanBlocks.tsx)) carries `role="log"`, `aria-live="polite"`, `aria-relevant="additions"`, `aria-atomic="false"`, and an accessible name. Virtual windows nest under `aria-live="off"` |
| Transcript turn | An `<article>` element (the implicit role, not a `role` attribute) with an `aria-label` from stable fields ("You", "Assistant", worker role from the wire) |
| Dual live regions | A `role="status"` region carries thinking / tool / turn-idle / error lifecycle; a stable log child announces each completed visible assistant turn once. While any session is streaming (`isAnySessionActivityLive`), NoticeRail polite announcements are muted so the composer activity lane fills the status region. That predicate is narrower than session liveness: an armed `awaiting_wake` lights the lane but streams nothing, so it never mutes announcements. The lane is a named line above `.den-composer-row` (`role="status"`, spinner `aria-hidden`); its visible copy is the announced value, so it carries no `aria-label`, and the 5 s `THINKING_LABEL_HOLD_MS` cadence keeps it from announcing per frame. The elapsed clock beside it is `aria-hidden`. See [Den § Composer activity lane](den.md#composer-activity-lane) |
| Streaming | `aria-busy="true"` on the active assistant article while streaming; cleared on idle |
| Transcript time | Time rows sit in the non-live virtual window, so ticking relative copy never re-announces. Day labels read their full date; times hidden until hover stay in the accessibility tree |
| ask_user / workflow feedback | An open ask pulls focus to the composer; options live in `AskUserDock` above it, the transcript carries a muted `workflow-feedback-open-marker`, and an answered ask becomes a chicklet |
| Icon-only controls | `aria-label` required (Send, Stop, nav collapse, unlabeled disclosure carets) |
| Hidden decoration | `aria-hidden="true"` on glyphs and SVG only, never on text-bearing nodes |
| Status | Tool, worker, and session status pair colour with text or `aria-label`, never colour alone |

Naming uses wire and catalog fields (session title, tool id, worker agent type), never markdown substring classification ([AGENTS.md](../AGENTS.md) § No heuristics).

## macOS Spoken Content (Speak selection / Speak under pointer)

Speak selection and VoiceOver read the selectable transcript; there is no in-app `SpeechSynthesis`. Selectable text is required in user message bubbles, rendered assistant markdown (`AssistantProseBody` / `.markdown-body`), and code blocks.

Chrome roots stamp `data-den-chrome` via [`chromeProps()`](../lycaon-den/src/styling/ui-chrome.ts) (nav rail, tab rails, title rows, dismiss ×, drag bands); inheritance covers children, and form fields under a chrome root stay selectable. Transcript prose never takes the mark. Copy docked inside chrome (notice rail, header cards) spreads `{...proseProps()}`; a control inside a prose island takes `chromeProps()` back. Context menu handling: [`context-menu.ts`](../lycaon-den/src/platform/interaction/context-menu.ts).

The chrome mark is `user-select: none`, so a stylesheet is one edit away from silencing the transcript. [`spoken-content-a11y.test.ts`](../lycaon-den/src/styling/spoken-content-a11y.test.ts) reads the shipped CSS and requires `user-select: text` on the user bubble, the assistant bubble, `.markdown-body`, and `.markdown-body pre`, and refuses a `none` on the prose body.

## Hover tips

Use hover tips for information the control does not already communicate: an unfamiliar action, a keyboard shortcut, a consequence, a disabled-state reason, or hidden detail. Ordinary show/hide, close, minimize, overflow-menu, and timeline navigation controls do not need them. Apply the choice to the whole control family, including opposite states and shared wrappers. Repeated visible text appears only when it is actually clipped.

A hover tip is Den's own layer, never the OS popup. A control carries `data-tip` (with `data-tip-pos` for placement and `data-tip-when-clipped` to show it only when the label is truncated), and one portaled [`TooltipHost`](../lycaon-den/src/components/primitives/TooltipHost.tsx) mounted at the app root renders it on hover or keyboard focus after a short delay. Because the host is delegated and portaled it also serves disabled controls, which dispatch no pointer events of their own.

The native `title` attribute is not used anywhere in Den: it ignores the theme, delay, and placement, cannot be dismissed, varies under VoiceOver, and never appears on a touch or keyboard path. `findNativeTooltipAttributes` ([`test/style-contracts/browser.ts`](../lycaon-den/src/test/style-contracts/browser.ts), run from `styling-invariants.test.ts`) fails the build on an intrinsic `title=` attribute, `setAttribute("title", …)`, a `.title =` assignment, or `title="` inside a markup template.

**A tip is not an accessible name.** It is supplemental and can be suppressed by `data-tip-when-clipped`; an icon-only control still carries its own `aria-label`, which a tip may repeat or extend but never replace.

## Focus + reduced motion baseline

| Concern | Contract |
|---------|----------|
| Modal dialogs | One shared trap, never a hand-rolled keydown loop: `createModalFocusTrap` for a standalone modal and `createOverlayScopeFocusTrap` where the surface also claims overlay shortcut scope ([`modal-focus-trap.ts`](../lycaon-den/src/platform/interaction/modal-focus-trap.ts)), both over `activateFocusTrap` ([`focus-trap.ts`](../lycaon-den/src/platform/interaction/focus-trap.ts)). Each supplies `aria-modal="true"`, initial focus on open, Escape closes (via `overlay.dismiss` when landed), focus restore to the trigger on dismissal, and refcounted `inert` on the backdrop so nested traps do not clear an outer claim. A modal action that opens another surface relinquishes restoration so the destination owns focus. Stage panes (Search, Settings, Files) are ordinary Shell nav, not modals. `accessibility-macos-invariants.test.ts` fails any `den-dialog-backdrop` or `aria-modal="true"` surface that reaches none of those entry points |
| Anchored popovers | `createAnchoredPopoverFocus` (same module) moves focus to the popover content, restores it to the trigger on Escape or dismissal, and leaves the page interactive: no `aria-modal`, no `inert`, no Tab trap. First-time tips use this contract and are requested at the interaction they explain, not queued when a stage mounts |
| Resident stages | A retained stage runs its task-focus contract whenever it becomes active, including after an idle or pending handoff. Search focuses its query; Files focuses the active editor once attached when focus arrived from outside Files. Opening from the Files tree keeps tree focus; the Files focus command transfers focus to the editor. Returning to a retained stage never leaves focus on `body` or in an inert surface |
| Editor gutter | One sequential Tab stop. Arrow keys move among its visible controls. On a line-facts handle, Up and Down move between fact lines and Right or Enter enters the card; inside, Up and Down move among rows and Left or Escape returns to the handle. Fold, finding, provenance, and restore controls activate with Enter and Space |
| Files tree | One sequential Tab stop. Arrow keys move among visible entries; Left and Right collapse, expand, and move through hierarchy. Shift+F10 or the Context Menu key opens every action for the focused entry, including icon actions removed from sequential focus |
| Trigger group | A run of popup triggers merged into one track ([`DenTriggerGroup`](../lycaon-den/src/components/primitives/DenTriggerGroup.tsx)) is one Tab stop as a `toolbar`. Left and Right move between segments and pass over disabled ones, Home and End jump to the ends, Enter, Space, or Down opens the focused popup, and the popup's own contract owns focus from there. Membership changing under the user leaves the tab stop where they left it. Roving focus comes from [`roving-focus.ts`](../lycaon-den/src/platform/interaction/roving-focus.ts), the same helper tablists use; do not fork a keydown loop per widget |
| Transcript disclosure | Disclosure controls retain focus. Expansion preserves the reading anchor. Programmatic focus uses `preventScroll`; only explicit typed reveal may align and focus a transcript target |
| Checkpoint and HITL cards | Allow / No / grant menu are keyboard-activatable, labelled, and restore focus on close |
| Focus visible | Custom buttons keep a `:focus-visible` ring ([`global.css`](../lycaon-den/src/global.css)); no `outline: none` without a replacement |
| Control outline | A control whose ring is its only affordance (unchecked `den-checkbox`) draws from `--den-control-line`, never the `--den-line` divider hairline: dark softens `--den-line` to about 1.2:1 so dividers dissolve into the stage, which would erase the box. Guard: `styling/focus-motion-a11y.test.ts` |
| Reduced motion | `@media (prefers-reduced-motion: reduce)` sets `scroll-behavior: auto`; disclosure layout keeps its semantic anchor when height animation is skipped; the composer lands at each new height and its activity line appears and leaves without a fade; boot splash fade and drawer and tab chrome animations are disabled. Shared surface load-in keeps its opacity-only fade and preparation boundary. CSS media query only, no native bridge |

Keyboard bindings live in [`keyboard-shortcuts.md`](keyboard-shortcuts.md); this page defines tab order and focus traps. Reuse an existing command where a chord already exists rather than forking key handling.

## Manual verification checklist

Run on macOS with System Settings → Accessibility → Display → Text size at default and maximum. Record the macOS version, slider position, and session ids in the handoff.

Automated companions (not a substitute for VoiceOver or Speak selection on device): `accessibility-macos-invariants.test.ts`, `accessibility-axe-smoke.test.tsx` (wcag2a/aa fixtures; smoke only, not a full WCAG gate), and `./task den:harness:test -- --grep "macOS accessibility"` ([`e2e/a11y-macos.spec.ts`](../lycaon-den/e2e/a11y-macos.spec.ts)).

1. Launch Den, change the text size slider: app text rescales without restart (`--den-text-scale` updates live).
2. Narrow the window to 720px at maximum text size: no horizontal scroll on the shell; composer and send button usable (≥24px hit target); no clipped nav or tool labels.
3. VoiceOver on: on Home and project surfaces, VO+U navigates landmarks (`Main` nav, `main`) and reaches an open drawer as a named dialog; the transcript is a log; a new assistant reply is announced once at turn idle, not per token; Stop, Send, and Mirror workspace buttons are named.
4. Spoken Content: select a user bubble and an assistant code block; Speak selection reads both.
5. Open Crossbar (`Mod+K`) and escalate to full search: Search nav stays selected on depth; Escape dismisses Crossbar and stage per [search.md](search.md); the sidebar stays interactive.
6. Enable Reduce motion: boot splash fade and drawer slide disabled; shared surface load-in still fades after its content is prepared.
7. Focus pass: dismiss a composer status popover with Escape and with an outside click; focus returns to its status chip. Confirm one checkpoint / HITL card is keyboard-activatable with focus restore on close.
