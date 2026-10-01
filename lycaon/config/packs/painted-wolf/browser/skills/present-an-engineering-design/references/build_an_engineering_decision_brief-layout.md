# Engineering decision-brief layout

Use this as a composition contract, not as copy to reproduce verbatim.

## Canvas and hierarchy

- Default canvas: `desktop-wide`, content fit, 1440px wide.
- Reading column: 1180px maximum, centered.
- Grid: 12 columns with 24px gutters.
- Body text: 15–17px and no wider than 68 characters.
- Recommendation: 30–42px serif or display face; it is the largest text after the page title.
- Spacing rhythm: 8, 12, 20, 32, 48px.
- Color roles: one accent for the recommendation, amber only for watch items, danger only for a material blocker.

## Required DOM order

```html
<article class="decision-brief">
  <header class="decision-header">
    <div class="decision-title">…</div>
    <dl class="decision-frame">…</dl>
  </header>
  <section class="recommendation">…</section>
  <section class="fact-strip">…</section>
  <main class="decision-body">
    <section class="case">…</section>
    <aside class="scorecard">…</aside>
  </main>
  <section class="risk-register">…</section>
  <footer class="decision-ask">…</footer>
</article>
```

The recommendation uses columns 1–9 and the decision deadline/ask uses columns 10–12. The fact strip contains three to five equal cells. The main argument uses seven columns and the scorecard five. On widths below 860px, every region becomes one column and the decision ask moves directly below the recommendation.

## CSS skeleton

```css
:root {
  --brief-ink: var(--kit-fg);
  --brief-muted: var(--kit-muted);
  --brief-paper: var(--kit-surface);
  --brief-line: var(--kit-border);
  --brief-accent: var(--kit-accent);
  --brief-watch: #9a6518;
}
* { box-sizing: border-box; }
.decision-brief { max-width: 1180px; margin: 0 auto; background: var(--brief-paper); color: var(--brief-ink); }
.recommendation { display: grid; grid-template-columns: minmax(0, 9fr) minmax(180px, 3fr); gap: 32px; padding: 36px; background: var(--brief-accent); color: var(--kit-accent-fg); }
.recommendation h2 { max-width: 28ch; margin: 0; font: 600 36px/1.15 var(--kit-font-serif); }
.fact-strip { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); border-bottom: 1px solid var(--brief-line); }
.fact-strip > div { min-width: 0; padding: 22px 24px; border-right: 1px solid var(--brief-line); }
.fact-strip strong { display: block; font: 650 30px/1 var(--kit-font-serif); font-variant-numeric: tabular-nums; }
.decision-body { display: grid; grid-template-columns: minmax(0, 7fr) minmax(300px, 5fr); gap: 48px; padding: 36px; }
.scorecard { align-self: start; padding: 22px; background: color-mix(in srgb, var(--brief-muted) 8%, transparent); }
.risk-register { display: grid; grid-template-columns: repeat(3, 1fr); border-top: 1px solid var(--brief-line); }
.risk-register article { break-inside: avoid; padding: 20px 24px; border-right: 1px solid var(--brief-line); }
@media (max-width: 860px) {
  .recommendation, .decision-body { grid-template-columns: 1fr; }
  .fact-strip { grid-template-columns: repeat(2, 1fr); }
  .risk-register { grid-template-columns: 1fr; }
}
@media print {
  @page { size: Letter; margin: 0.55in; }
  .decision-brief { max-width: none; }
  .recommendation, .fact-strip, .scorecard, .risk-register article { break-inside: avoid; }
}
```

## Content constraints

- Title as a question when the decision can be stated honestly that way.
- Recommendation begins with a verb: `Adopt`, `Defer`, `Replace`, `Pilot`, or `Reject`.
- Fact tiles contain a value, unit, baseline, and source note; use prose instead of a tile when any is absent.
- Scorecards show rationale beside scores and the weighting method beneath the table.
- Put coverage gaps in a bordered block on the first page, never only in an appendix.
