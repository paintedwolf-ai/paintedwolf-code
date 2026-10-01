# Coding-workflow prototype layout

## Workspace frame

Build the first viewport around the active task:

```text
┌ product bar: product · search · activity · user ┐
├ 176px rail ┬ task identity + current phase      ┤
│ navigation │ state stepper                      │
│            │ main evidence/action ┬ context     │
│            │ pane                 │ 280px       │
└────────────┴──────────────────────┴─────────────┘
```

- Desktop frame: 1280×720 or 1440×900.
- Product bar: 56px high.
- Navigation rail: 176–224px; omit it below 900px.
- Context panel: 260–320px; stack it below the main pane below 820px.
- Main body text: 15–16px. Metadata: no smaller than 12px.
- State controls stay visible above the changing region.

## DOM skeleton

```html
<main class="coding-prototype" data-state="investigating">
  <header class="product-bar">…</header>
  <div class="workspace">
    <nav class="product-rail">…</nav>
    <section class="task">
      <header class="task-heading">…</header>
      <nav class="state-stepper" aria-label="Prototype state">…</nav>
      <div class="task-grid">
        <section class="state-pane" aria-live="polite">…</section>
        <aside class="evidence-pane">…</aside>
      </div>
    </section>
  </div>
</main>
```

Use real buttons for state transitions. The selected step carries `aria-current="step"`. Keep state in the URL query or a small in-memory fixture; a reviewer must be able to reload the entry state without a database.

## CSS skeleton

```css
* { box-sizing: border-box; }
.coding-prototype { min-height: 100vh; background: #f5f7fb; color: #18243a; }
.product-bar { height: 56px; display: grid; grid-template-columns: 220px minmax(220px, 520px) auto; align-items: center; gap: 24px; padding: 0 20px; background: white; border-bottom: 1px solid #dfe4ec; }
.workspace { display: grid; grid-template-columns: 188px minmax(0, 1fr); min-height: calc(100vh - 56px); }
.product-rail { padding: 16px 10px; background: white; border-right: 1px solid #dfe4ec; }
.task { min-width: 0; padding: 24px; }
.state-stepper { display: grid; grid-template-columns: repeat(var(--state-count, 4), minmax(0, 1fr)); gap: 2px; padding: 3px; background: #e7ebf2; border-radius: 8px; }
.state-stepper button { min-height: 38px; border: 0; border-radius: 6px; background: transparent; color: #657187; }
.state-stepper [aria-current='step'] { background: white; color: #244dac; box-shadow: 0 1px 5px rgb(22 35 62 / 10%); }
.task-grid { display: grid; grid-template-columns: minmax(0, 1fr) 280px; gap: 16px; margin-top: 16px; }
.state-pane, .evidence-pane { min-width: 0; padding: 22px; background: white; border: 1px solid #dfe4ec; border-radius: 9px; }
.changed-files { display: grid; grid-template-columns: minmax(180px, 1fr) auto auto; font-variant-numeric: tabular-nums; }
@media (max-width: 900px) {
  .workspace { grid-template-columns: minmax(0, 1fr); }
  .product-rail { display: none; }
}
@media (max-width: 820px) {
  .task-grid { grid-template-columns: minmax(0, 1fr); }
  .product-bar { grid-template-columns: 1fr auto; }
  .product-search { display: none; }
}
@media (max-width: 560px) {
  .task { padding: 14px; }
  .state-stepper { grid-template-columns: repeat(2, 1fr); }
}
```

## State content

Each state pane contains, in order:

1. A state label and one-sentence claim.
2. The evidence or changed facts that justify the claim.
3. The primary action and its consequence.
4. A visible recovery or escape path.

The evidence panel distinguishes source reads, commands, test receipts, authored analysis, and human decisions. Never style model-authored statements as observed tool output.

## Required prototype checks

- No horizontal document overflow at 390, 768, or 1280 CSS pixels.
- Keyboard focus follows the critical path and returns after dialogs or drawers.
- Loading preserves task identity and the last settled state.
- Failure preserves user input, diff context, and recovery action.
- Approval states name the exact action and scope being approved.
- Completion shows verification freshness and any unverified limits.
