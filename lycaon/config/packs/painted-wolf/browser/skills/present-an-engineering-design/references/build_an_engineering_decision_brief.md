# Build an engineering decision brief

Enter only when a named technical choice has at least two credible options and a real person or group must decide. If the work is still broad exploration, compare approaches first; if the choice is already made and needs execution detail, write an implementation proposal instead.

1. Write a **decision frame** with exactly four fields: `decision`, `decider`, `decision_by`, and `constraints`. If the decider or deadline is unknown, label it `Unassigned` or `Not scheduled`; do not invent governance.
2. Build an **evidence ledger** of three to seven decisive facts. Each row contains `claim`, `observation`, `source`, `freshness`, and `limits`. Use observed repository/runtime evidence or user-supplied facts; label modeled estimates and synthetic scenario data conspicuously.
3. Produce an **option scorecard** with two to four options and three to six criteria. Show criterion weights, a 1–5 score, a one-line rationale, and any disqualifier. Scores summarize the written evidence; they never replace it.
4. Draft an **answer-first brief** in this order: decision ribbon, recommendation of at most 120 words, three-to-five metric or fact tiles, why now, evidence, option scorecard, risk register, and one decision ask. Every risk row contains `risk`, `likelihood`, `impact`, `mitigation`, and `owner`.
5. Read [the decision-brief layout](references/build_an_engineering_decision_brief-layout.md), then render the brief. Use `render_view` at `desktop-wide` for a one-screen brief. Use a local HTML route with print CSS when the complete brief needs more than one 4096px canvas; keep the decision and limits on page one.
6. Inspect the rendered artifact. Confirm the recommendation is visible before supporting detail, numbers carry units and provenance, no option is clipped, and color is not the only score or risk signal. On overflow, split evidence into an appendix before shrinking body text below 14px.
7. Return a **decision handoff** with `recommendation`, `decision_needed`, `options`, `material_risks`, `limits`, and the artifact or local route. State that authored visuals communicate the analysis; they do not establish new evidence.

Worked scorecard row:

| Option | Isolation 35% | Startup cost 15% | Recovery 25% | Compatibility 25% | Total | Disqualifier |
|---|---:|---:|---:|---:|---:|---|
| Worktree by default | 5 — no shared writes | 4 — +1.8s median | 5 — discardable | 4 — Git only | 4.60 | None observed |

If evidence cannot distinguish the options, stop at an evidence-gap brief: name the unresolved claim, the smallest observation that would settle it, and who owns that observation. Do not manufacture a recommendation to complete the layout.
