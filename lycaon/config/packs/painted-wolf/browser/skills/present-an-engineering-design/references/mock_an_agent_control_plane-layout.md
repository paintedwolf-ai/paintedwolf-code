# Agent control-plane layout

## First viewport

```text
┌ 52px product bar: product · scope · search · activity · user ┐
├ page title + context ─────────────── filters · primary action ┤
├ fact ───── fact ───── fact ───── fact ────────────────────────┤
├ optional 160px trend / queue-health visualization ────────────┤
├ task worklist ───────────────────────────┬ 260px detail pane ┤
└──────────────────────────────────────────┴───────────────────┘
```

The worklist, not the summary cards, is the dominant surface. At least one actionable task row must appear above the fold at 1280×720.

## DOM skeleton

```html
<main class="agent-control-plane">
  <header class="product-bar">…</header>
  <section class="control-main">
    <header class="control-heading">…</header>
    <section class="control-facts">…</section>
    <figure class="queue-health">…</figure>
    <section class="control-workspace">
      <div class="task-worklist" role="grid">…</div>
      <aside class="task-detail" aria-live="polite">…</aside>
    </section>
  </section>
</main>
```

Use a semantic table when rows are read more often than selected. Use an ARIA grid only when keyboard row selection is implemented. The selected row and detail pane always name the same task id.

## CSS skeleton

```css
:root {
  --control-bg: #f4f6f9;
  --control-top: #162033;
  --control-paper: white;
  --control-line: #dde2e9;
  --control-accent: #3167db;
  --control-good: #25805a;
  --control-watch: #b46d1d;
  --control-bad: #bd4d3a;
}
* { box-sizing: border-box; }
.agent-control-plane { min-height: 100vh; background: var(--control-bg); color: #172238; }
.product-bar { height: 52px; display: grid; grid-template-columns: 190px minmax(0, 1fr) auto; align-items: center; padding: 0 18px; color: white; background: var(--control-top); }
.control-main { padding: 22px; }
.control-heading { display: flex; align-items: end; justify-content: space-between; gap: 18px; }
.control-facts { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 10px; margin: 16px 0 10px; }
.control-facts article, .queue-health, .task-worklist, .task-detail { min-width: 0; background: var(--control-paper); border: 1px solid var(--control-line); border-radius: 7px; }
.control-facts article { padding: 14px; }
.control-facts strong { display: block; margin: 5px 0; font-size: 24px; font-variant-numeric: tabular-nums; }
.queue-health { height: 170px; padding: 14px 18px; }
.control-workspace { display: grid; grid-template-columns: minmax(0, 1fr) 260px; gap: 10px; margin-top: 10px; }
.task-row { display: grid; grid-template-columns: 1.05fr .9fr 1.2fr .75fr .7fr; gap: 8px; align-items: center; min-height: 46px; padding: 8px 13px; border-bottom: 1px solid #edf0f3; }
.task-row[aria-selected='true'] { background: #eef4ff; box-shadow: inset 3px 0 var(--control-accent); }
.task-detail { padding: 16px; }
.attention-critical { color: var(--control-bad); }
.attention-watch { color: var(--control-watch); }
.attention-healthy { color: var(--control-good); }
@media (max-width: 900px) {
  .control-facts { grid-template-columns: repeat(2, 1fr); }
  .control-workspace { grid-template-columns: 1fr; }
  .task-worklist { overflow-x: auto; }
  .task-row { min-width: 680px; }
}
@media (max-width: 560px) {
  .control-main { padding: 12px; }
  .control-heading { align-items: start; }
  .queue-health { height: 140px; }
}
```

## Density rules

- Use 14px for row labels and actionable metadata; reserve 12px for timestamps and secondary identifiers.
- Right-align counts, durations, money, percentages, and change totals; use tabular numerals.
- Summary facts contain a comparison or threshold, not a large number alone.
- Charts state unit, time range, baseline, and data freshness. Omit the chart when it only decorates synthetic values.
- Filters summarize active constraints and provide a one-action clear path.
- The detail pane begins with attention state, task id, current phase, and the next action. Put logs and long evidence behind disclosure.

## State rules

- `Loading`: retain layout and last settled values; do not replace the worklist with a full-page spinner.
- `Empty`: name the active filter and offer Clear filters; do not imply there are no tasks globally.
- `Partial`: keep successful repositories visible and name unavailable sources.
- `Stale`: show the last observation time and a refresh action; never present it as live.
- `Failure`: preserve selection and filters while reporting the failed operation beside the affected surface.
- `Approval held`: name the exact action, repository/worktree, consequence, and reviewing person or role when known.
