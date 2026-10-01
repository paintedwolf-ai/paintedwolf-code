# System-design review layout

## Review board

Use a dark technical board only when it improves boundary legibility; inherit a supplied project brand when present. The board has four horizontal bands:

1. Design claim and scope facts.
2. Architecture map.
3. Decision log and interface changes.
4. Failure budget, recovery contract, rollout, and open questions.

```html
<article class="design-review">
  <header class="design-claim">…</header>
  <section class="architecture" aria-label="Target architecture">…</section>
  <section class="review-grid">
    <div class="decision-log">…</div>
    <div class="interface-summary">…</div>
  </section>
  <section class="recovery-grid">…</section>
</article>
```

## Architecture grammar

- Use two to four nodes per row and no more than nine primary nodes.
- Each node contains a noun title, one responsibility, and optional state owned.
- Horizontal arrows mean request or transfer; vertical arrows mean projection or lifecycle progression; dashed arrows mean asynchronous delivery.
- Label every fan-out and trust-boundary crossing.
- Place the durable state node centrally; place adapters and projections below it.
- Use explicit row wrappers instead of relying on element order or `nth-child` placement.

```html
<section class="architecture">
  <p class="layer-label">Task control</p>
  <div class="architecture-row">
    <article class="node">Task launcher</article>
    <div class="arrow" aria-label="creates">→</div>
    <article class="node">Workspace broker</article>
  </div>
  <div class="down" aria-label="records">↓</div>
  <p class="layer-label">Durable state</p>
  <article class="node spine">Session and effect ledger</article>
  <div class="fanout" aria-hidden="true"><i></i><i></i><i></i></div>
  <div class="service-row">…</div>
</section>
```

## CSS skeleton

```css
:root {
  --review-bg: #111b20;
  --review-panel: #182a27;
  --review-line: #40574f;
  --review-fg: #edf3ef;
  --review-muted: #84978f;
  --review-accent: #72d5a7;
  --review-open: #e6bf63;
}
* { box-sizing: border-box; }
.design-review { max-width: 1180px; margin: 0 auto; color: var(--review-fg); background: var(--review-bg); }
.design-claim { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 36px; padding: 34px 38px; border-bottom: 1px solid var(--review-line); }
.design-claim h1 { max-width: 34ch; margin: 0; font: 520 34px/1.18 var(--kit-font-serif); }
.scope-facts { display: grid; grid-template-columns: repeat(3, 1fr); align-self: center; }
.scope-facts div { padding: 12px 16px; background: #17252b; border-left: 1px solid var(--review-line); }
.architecture { padding: 30px 42px 36px; }
.architecture-row { display: grid; grid-template-columns: 1fr 54px 1fr; gap: 12px; align-items: center; }
.node { min-width: 0; min-height: 72px; padding: 18px; background: var(--review-panel); border: 1px solid var(--review-line); }
.node.spine { max-width: 720px; margin: 0 auto; }
.service-row { display: grid; grid-template-columns: repeat(3, 1fr); gap: 12px; }
.arrow, .down { display: grid; place-items: center; color: var(--review-accent); }
.down { width: 28px; height: 28px; margin: 12px auto; border: 1px solid var(--review-line); border-radius: 50%; }
.review-grid, .recovery-grid { display: grid; grid-template-columns: 1.4fr 1fr; border-top: 1px solid var(--review-line); }
.review-grid > *, .recovery-grid > * { min-width: 0; padding: 26px; border-right: 1px solid var(--review-line); }
@media (max-width: 760px) {
  .design-claim, .architecture-row, .review-grid, .recovery-grid { grid-template-columns: 1fr; }
  .scope-facts { width: 100%; }
  .arrow { transform: rotate(90deg); }
  .service-row { grid-template-columns: 1fr; }
}
```

## Tables and decisions

- Use monospace for interface names, event names, status codes, and measured limits—not for body prose.
- Keep the decision id, status, and owner visible without expanding a row.
- Show rejected alternatives with their decisive reason; do not gray them into illegibility.
- When an interface table exceeds five columns, give it a full-width page or horizontal scroll in the interactive version. Never compress it below 13px.
- Open questions occupy a distinct final block with an owner and the observation required to close each one.

## Verification

At 768px and 1280px, confirm every architecture node stays inside the board, connectors terminate at node edges, the durable spine is visually central, and the decision/failure sections do not repeat or overlap. Use `measure_page` when alignment or overflow is part of the claim.
