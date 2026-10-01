---
name: visualize-data
description: Render a small chart or comparison table when counts, trends, benchmarks, durations, or sizes merit a visual.
# Named for readers whose profile holds it; other profiles follow the skill without it.
optional_tools:
  - surface_note
---

# Visualize data

1. Chart only values you actually collected in this session, and name the source in the `caption`. A chart presents findings; it is not evidence for new claims, and estimating numbers to fill a chart is fabrication.
2. Choose table versus chart. Eight or fewer values where exact numbers matter read best as an HTML table on kit typography with right-aligned numeric columns in `'JetBrains Mono'`; use a chart when the point is shape — trend, ranking, distribution, or outliers.
3. There is no scripting in `render_view`, so compute the geometry yourself before writing SVG. Map values to coordinates explicitly — for bars in a 500px-tall plot area, `height = value / maxValue * 500` and `y = plotBottom - height` — and write the computed numbers into the markup.
4. Keep scales honest. Bar baselines sit at zero; axes carry real units and tick labels; time runs left to right at even intervals; never truncate an axis to exaggerate a difference.
5. Style with kit tokens so the chart matches both themes — `var(--kit-accent)` for the primary series, `var(--kit-muted)` for comparisons and axis text, `var(--kit-border)` for gridlines, `var(--kit-danger)` only for genuinely bad values. For a chart shipped inside a branded mockup, the project's own palette may come from its repo stylesheet via the asset origin (see the mock-before-build skill's project-assets reference); kit tokens stay the default.
6. Label directly. Put values on or beside marks when there are few; add a legend only past three series. Title the chart with the finding, not the metric name.
7. Present the render by copying its `artifact_id` into `artifact_ids`: on `surface_note` mid-run, or in the final reply's metadata block. A worker's renders reach the coordinator in `proof_json.visual_artifact_ids`. Never embed the id as a Markdown or HTML image (`PRESENT_MARKDOWN_EMBED`). Keep the underlying numbers available in prose or a worker result so the reader can check the chart.
