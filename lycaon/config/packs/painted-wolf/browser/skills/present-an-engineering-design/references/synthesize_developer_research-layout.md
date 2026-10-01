# Developer-research synthesis layout

## Editorial structure

```html
<article class="research-dossier">
  <header class="research-cover">
    <div class="series">Field notes / issue</div>
    <div class="core-finding">…</div>
    <dl class="study-scope">…</dl>
  </header>
  <main class="research-body">
    <section class="thesis-and-quote">…</section>
    <section class="theme-matrix">…</section>
    <aside class="contradiction">…</aside>
    <section class="implications-and-limits">…</section>
  </main>
</article>
```

Use an editorial rather than dashboard hierarchy: one dominant claim, long-form reading measure, evidence table, then implications. Do not convert qualitative research into a row of decorative KPI cards.

## CSS skeleton

```css
:root {
  --research-paper: #fffdf8;
  --research-cover: #efe6d9;
  --research-ink: #312a22;
  --research-muted: #76695c;
  --research-line: #ddd5c8;
  --research-accent: #9a4b36;
  --research-dark: #302a24;
}
* { box-sizing: border-box; }
.research-dossier { max-width: 1080px; margin: 0 auto; color: var(--research-ink); background: var(--research-paper); }
.research-cover { display: grid; grid-template-columns: 120px minmax(0, 1fr); gap: 38px; padding: 40px 44px 32px; background: var(--research-cover); }
.series { padding-top: 8px; border-top: 2px solid #8a6d4f; color: #8a6d4f; font: 700 11px/1.4 var(--kit-font-mono); text-transform: uppercase; letter-spacing: .1em; }
.core-finding h1 { max-width: 28ch; margin: 0; font: 520 40px/1.14 var(--kit-font-serif); letter-spacing: -.025em; }
.study-scope { grid-column: 2; display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; padding-top: 18px; border-top: 1px solid #cfc2b2; }
.study-scope strong { display: block; font: 600 24px/1 var(--kit-font-serif); }
.research-body { padding: 42px 44px; }
.thesis-and-quote { display: grid; grid-template-columns: 1.3fr 1fr; gap: 48px; }
.thesis { max-width: 64ch; font: 500 19px/1.55 var(--kit-font-serif); }
blockquote { margin: 0; padding-left: 22px; border-left: 3px solid #d37a5e; font: italic 18px/1.45 var(--kit-font-serif); }
blockquote cite { display: block; margin-top: 14px; color: var(--research-muted); font: 700 10px/1.5 var(--kit-font-sans); text-transform: uppercase; letter-spacing: .08em; }
.theme-row { display: grid; grid-template-columns: 1.35fr 1fr .65fr 1.1fr; gap: 16px; align-items: center; padding: 15px 10px; border-top: 1px solid var(--research-line); }
.contradiction { display: grid; grid-template-columns: 48px 1fr; gap: 18px; margin-top: 30px; padding: 24px; color: #f7f2e9; background: var(--research-dark); }
.implications-and-limits { display: grid; grid-template-columns: 1.2fr 1fr; gap: 48px; margin-top: 34px; }
.limits { padding-left: 28px; border-left: 1px solid var(--research-line); }
@media (max-width: 760px) {
  .research-cover, .thesis-and-quote, .implications-and-limits { grid-template-columns: 1fr; }
  .series, .study-scope { grid-column: 1; }
  .study-scope { grid-template-columns: repeat(2, 1fr); }
  .limits { padding: 20px 0 0; border-left: 0; border-top: 1px solid var(--research-line); }
  .theme-row { grid-template-columns: 1.3fr .8fr .7fr; }
  .theme-row > :last-child { display: none; }
}
@media print {
  @page { size: Letter; margin: .55in; }
  .research-cover, blockquote, .theme-row, .contradiction { break-inside: avoid; }
}
```

## Evidence notation

- Use stable source labels such as `DEV-07`, `SUPPORT-14`, or `TRACE-03`.
- Quotes carry source label, role/cohort, method, and session number; omit personal names by default.
- Strength labels are `Anecdote`, `Emerging`, `Moderate`, and `Strong qualitative signal`. Never use `Statistically significant` without a valid quantitative design.
- A source-count display uses filled and empty marks only when the exact denominator is stated beside it.
- Interpretation callouts begin `We infer`; recommendations begin with an action verb; observations describe what occurred.
- Put limits on the first rendered page or immediately after the study scope. Repeat them in the appendix when the document is long.

## Appendix

A multi-page dossier may add:

1. Method and recruitment.
2. Evidence inventory.
3. Coded observation table.
4. Theme-to-source map.
5. Discussion guide or task scenarios.

Keep identifying data out of the visual appendix unless the user explicitly requests attribution and the source permits it.
