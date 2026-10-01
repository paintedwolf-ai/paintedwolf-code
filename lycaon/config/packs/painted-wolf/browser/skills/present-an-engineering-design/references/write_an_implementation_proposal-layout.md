# Implementation-proposal layout

## Document architecture

Use six sections; a long appendix does not change their order:

1. Cover: proposed outcome, audience, date, confidentiality when supplied.
2. Situation: what is happening, why it matters, why now.
3. End state: three to six measurable outcomes.
4. Scope and approach: boundaries, workstreams, architecture thumbnail.
5. Delivery: phase sequence, dependencies, roles, and acceptance.
6. Decision: resources, risks, assumptions, and next action.

## Cover grammar

The cover uses a 12-column grid with the title in columns 1–8 and one geometric brand motif in columns 8–12. Do not use screenshots as background texture. Keep the title below 12 words and the deck below 45 words.

```html
<article class="proposal">
  <section class="proposal-cover">
    <header class="proposal-brand">…</header>
    <div class="proposal-title">…</div>
    <ol class="proposal-outcomes">…</ol>
    <footer class="folio">…</footer>
  </section>
  <section class="proposal-page situation">…</section>
  <section class="proposal-page approach">…</section>
  <section class="proposal-page delivery">…</section>
  <section class="proposal-page decision">…</section>
</article>
```

## CSS skeleton

```css
:root {
  --proposal-navy: #102b4e;
  --proposal-blue: #215ec5;
  --proposal-gold: #f2c854;
  --proposal-ink: #14213c;
  --proposal-muted: #65738a;
  --proposal-line: #dce2eb;
}
* { box-sizing: border-box; }
.proposal { max-width: 1100px; margin: 0 auto; background: white; color: var(--proposal-ink); }
.proposal-cover { min-height: 760px; padding: 48px; color: white; background: var(--proposal-navy); display: grid; grid-template-rows: auto 1fr auto auto; break-after: page; }
.proposal-title { align-self: center; max-width: 760px; }
.proposal-title h1 { max-width: 16ch; margin: 14px 0; font: 550 68px/.98 var(--kit-font-serif); letter-spacing: -.035em; }
.proposal-title p { max-width: 58ch; color: #cbd9e9; font: 400 19px/1.5 var(--kit-font-serif); }
.proposal-outcomes { display: grid; grid-template-columns: repeat(3, 1fr); gap: 16px; padding: 0; list-style: none; }
.proposal-page { min-height: 760px; padding: 54px 48px; break-after: page; }
.outcome-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 18px; }
.scope-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 24px; }
.workstreams { display: grid; grid-template-columns: repeat(3, 1fr); gap: 1px; background: var(--proposal-line); }
.workstreams article { min-width: 0; padding: 24px; background: white; break-inside: avoid; }
.phase-roadmap { display: grid; grid-template-columns: repeat(var(--phase-count, 4), 1fr); gap: 16px; }
.phase-roadmap article { border-left: 3px solid var(--proposal-blue); padding-left: 16px; break-inside: avoid; }
.decision-card { display: grid; grid-template-columns: minmax(0, 1fr) 320px; gap: 36px; padding: 28px; background: var(--proposal-gold); }
@media (max-width: 760px) {
  .proposal-cover, .proposal-page { min-height: auto; padding: 30px 22px; }
  .proposal-title h1 { font-size: 46px; }
  .proposal-outcomes, .outcome-grid, .workstreams, .phase-roadmap, .decision-card { grid-template-columns: 1fr; }
}
@media print {
  @page { size: Letter; margin: .5in; }
  body { margin: 0; background: white; print-color-adjust: exact; }
  .proposal { max-width: none; }
  .proposal-cover, .proposal-page { min-height: 9.35in; }
  table, figure, article, .decision-card { break-inside: avoid; }
}
```

## Page rules

- Every page has a running subject label and folio; the cover is page 01.
- Use sentence-case headings. A page gets one claim-sized heading, not a banner plus a redundant heading.
- A workstream card contains `outcome`, `owned surfaces`, `dependencies`, and `exit evidence`.
- The phase roadmap encodes dependencies with order and connectors; do not use equal boxes for dependency-ordered work without explaining the order.
- Put `Not in scope` beside `In scope` with equal visual weight.
- The final page repeats the decision ask and lists assumptions close enough to affect it.
- Reserve an appendix for detailed interface tables, evidence, estimates, or repository inventories; do not move risks or limits there.
