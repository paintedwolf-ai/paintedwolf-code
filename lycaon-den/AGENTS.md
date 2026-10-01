# Lycaon Den — frontend agent policy

Package-specific rules for the Tauri + Solid.js desktop app (`lycaon-den/`). Universal policy: [`../AGENTS.md`](../AGENTS.md). Standard semantics: [`../docs/agents-md-standard.md`](../docs/agents-md-standard.md).

**Run all commands from the repo root** — use `./task den:*`, not bare `bun test` or `npx tsc`.

## Commands

```bash
./task den:typecheck
./task den:test:fast
./task den:test
./task den:test:digest -- src/**/*.test.ts
./task den:harness          # LLM-drivable UI stack — author e2e specs
./task den:harness:test
./task den:sidecar          # terminal 1 — with den:dev in terminal 2
./task den:dev
```

Full catalog: [`../docs/dev-tasks.md`](../docs/dev-tasks.md).

## Thin client authority

Den is a **thin cache with a UI** — session state, gates, plan approval timing, and tool card status come from the sidecar, not client inference.

Wire contract: [`../docs/host-contract.md`](../docs/host-contract.md). `src/api/types.ts` is **generated** (`./task codegen:den-types` after `./task openapi:bundle`) — never hand-edit. Non-DTO helpers live beside it; the EventTopic payload relation is generated as `event-payloads.generated.ts`. See [`../lycaon/AGENTS.md`](../lycaon/AGENTS.md).

## Naming

**Zero "lycaon"** in user- or OS-visible product copy — [`../docs/naming.md`](../docs/naming.md). Repo paths and `LycaonClient` stay engine-internal.

## Testing

Frontend tests via `./task den:test` (full) or `./task den:test:fast` (handoff; representative model and DOM seam canaries). Styling invariants: `src/styling/styling-invariants.test.ts`. E2E: `lycaon-den/e2e/` with `e2e/helpers.ts`; prefer `./task den:harness:test` for local iteration.

Den verification shares the host queue and worker budget with Go checks. Submit
one request at a time and wait for its result using the
[completion handler](../AGENTS.md#verification-batches). Queue controls and
cache policy are in [`../AGENTS.md`](../AGENTS.md#testing).

Vitest runs `.test.ts` in Node and `.test.tsx` in jsdom. A TypeScript test that genuinely exercises browser globals declares `// @vitest-environment jsdom` on its first line; pure model tests stay on the Node project.

## Driving the Den UI (harness)

Use `./task den:harness` to verify UI changes, reproduce bugs, or author specs — mock-LLM sidecar + Vite, same-origin proxy. Page exposes `window.__harness` (`await __harness.help()`).

Typical loop: `openProject('Harness')` → `newSession()` → `prompt(text)` → `transcript()`.

LLM modes: default mock; `LYCAON_HARNESS_REAL=1` for saved provider keys; `LYCAON_LLM_MANUAL=1` to author assistant turns via `__harness.llm`.

Full reference: [`../docs/dev-tasks.md`](../docs/dev-tasks.md#den-harness-llm-drivable-stack).

Do **not** use in-app "restart backend" while `./task den:dev` runs — restart `den:sidecar` in terminal 1 instead.

## Layout

| Path | Role |
|------|------|
| `src/` | Solid.js app |
| `src/api/types.ts` | Generated wire types (`codegen:den-types` — DO NOT EDIT) |
| `src-tauri/` | Tauri shell + sidecar lifecycle |
| `e2e/` | Playwright specs |

## Responsive layout

A region that resizes with the window — split host, split column, browse stage, file editor, tab panel — takes a **band** from `src/layout/layout-bands.ts`, not `container-type`. Add the stop to `LAYOUT_BAND_SCALES`, bind with `bindLayoutBand` on the element that owns the box, and select with `:where([data-*-band~="<token>"])` so the cascade is unchanged. A container query over a whole region re-resolves that subtree on every frame of a live resize.

Components smaller than a region — cards, chip rows, chrome strips, forms — keep `@container`. Proof: `src/layout/layout-bands.test.ts` (tokens and stylesheet agree in both directions) and `src/styling/resize-invalidation-invariants.test.ts`.

## Scrolling

A themed scroll surface is `Scrollport` (`src/components/primitives/Scrollport.tsx`): a frame that carries the scrollbar, a viewport that scrolls, and a content box that is the extent. The frame class sizes and paints the box; the content class carries padding and the inner layout. HTML built outside Solid uses `wrapInScrollportFrame`. Scrollbar chrome never sits inside the element that scrolls — WebKit moves that element on its scrolling thread, so chrome inside it jumps whenever the main thread is busy. Surfaces with their own measure pass (chat, editor, files tree, rail) attach with `attachThemedViewportScrollbar` on the same frame shape. Never request a native smooth scroll (`behavior: "smooth"`, `scroll-behavior: smooth`): a write during WebKit's threaded animation leaves painting and hit testing a row apart. Glide with `ScrollportMotion.revealOffset` or `glideScrollLeft` ([`native-smooth-scroll.test.ts`](src/platform/scrolling/native-smooth-scroll.test.ts)). Proof: `src/platform/scrolling/themed-scrollbars.test.ts` (with its `.geometry`, `.measurement`, and `.discovery` siblings) and `src/platform/scrolling/scrollbar-visual-contract.test.ts`.

## Accessibility

macOS OS text size, VoiceOver, Spoken Content, hover tips, focus, and reduced-motion contract: [`../docs/accessibility.md`](../docs/accessibility.md). Keyboard chords stay in [`../docs/keyboard-shortcuts.md`](../docs/keyboard-shortcuts.md) — do not fork a11y keydown handlers.

**Proof:** `src/platform/accessibility-macos-invariants.test.ts` (scale source, rem root, px-free type, selectable prose, dual live regions); `src/platform/accessibility-axe-smoke.test.tsx` (wcag2a/aa fixtures — not a full CI gate); the manual verification checklist in [`../docs/accessibility.md`](../docs/accessibility.md#manual-verification-checklist) via `./task den:harness` / `e2e/a11y-macos.spec.ts`.

## Related

- [`../AGENTS.md`](../AGENTS.md) · [`../lycaon/AGENTS.md`](../lycaon/AGENTS.md)
- [`../docs/dev-tasks.md`](../docs/dev-tasks.md) · [`../docs/den.md`](../docs/den.md) · [`../docs/host-contract.md`](../docs/host-contract.md) · [`../docs/coordination.md`](../docs/coordination.md)
