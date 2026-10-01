---
name: measure-interface
description: Measure rendered size, spacing, alignment, overlap, position, or contrast, including after interactions.
---

# Measure an interface

Use this skill when the question is quantitative or state-sensitive: whether a control rendered after an interaction, an overlay covers another control, cards align, a gap changed, a target fits in the viewport, or text has numeric contrast. A screenshot can corroborate appearance but cannot establish those facts.

1. Choose the page binding that preserves the state under review.
   - For an authenticated, interactive, or post-action state: `page_open`, then `page_act` as needed, and retain its returned `id`.
   - For an initial static export or one-shot running route: call `measure_page` with exactly one of `project_dir` or loopback `url`.
2. Inspect structural evidence before choosing selectors. Use `page_snapshot` for a live page, or the structural output from `capture_page` for a one-shot page. Prefer stable ids and `data-testid` selectors; each selector must resolve to exactly one node.
3. Measure only the nodes needed to answer the question. With a live page, pass `id` and `selectors` to `measure_page`; do not reopen the route. Request `metrics` only for styles material to the claim; a custom list replaces the defaults, so for a contrast claim include `color` and `background-color` and select the element that paints the background (a translucent background reports no contrast). Add `annotate: true` when a bounding-box overlay will make the result easier to review.
4. Read the report as evidence: `rendered` means the element has a nonzero box. `reach` says whether a pointer lands on it: `receives` (with `points_receiving` of `points_sampled`), `covered` (with `covered_by`), `clipped` (with `clipped_by`), `outside_viewport`, or `not_rendered`. `rect` and `viewport` are CSS pixels, and `relations` give `gap`, `alignment`, `overlap`, `distance_to_viewport_edge`, `contrast`, and `pointer_blocked`. Treat absent contrast as unavailable, not a passing result.
5. If a selector rejects, branch on its `Code:`. Use a more specific selector for `MEASURE_SELECTOR_AMBIGUOUS`; inspect the current live page state and correct the selector for `MEASURE_SELECTOR_EMPTY`; reduce the set (at most 16) for `MEASURE_SELECTORS_BOUNDS`; fix the syntax for `MEASURE_SELECTOR_INVALID`; reopen the page for `PAGE_NOT_FOUND` (the id was closed or reaped).
6. State the measured fact and cite the resulting `page_geometry#` evidence. Do not convert the report into a broader visual, usability, or accessibility claim than it supports.
7. Close a live page with `page_close` when it is no longer needed.

See [measurement patterns](references/patterns.md) for focused, high-value probes.
