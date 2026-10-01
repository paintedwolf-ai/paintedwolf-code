---
name: draw-a-diagram
description: Explain architecture, flows, states, sequences, or changes with a diagram when layout helps more than prose.
# Named for readers whose profile holds it; other profiles follow the skill without it.
optional_tools:
  - surface_note
---

# Draw a diagram

1. Decide the picture earns its place. Three or more interacting parts, a flow with branches, layered structure, or a before/after contrast justify a diagram; two facts in a sentence do not.
2. Pick the form. Box-and-arrow SVG (`mime: "svg"`) for flows, architecture, and state; an HTML grid (`mime: "html"`) for comparisons, matrices, and timelines where text dominates and flow layout beats coordinates.
3. Keep it themable. Color with `--kit-*` tokens — in SVG use `fill="var(--kit-surface)"`, `stroke="var(--kit-border)"`, accent sparingly — label with kit fonts, and pass the `theme` the reader uses.
4. Use the product's real marks when the repo has them. A component box that shows the actual service logo, or nodes drawn with the project's own icons, read faster than generic `#kit-*` icons. In SVG draw a project icon with `<image href="http://lycaon.asset/<root-relative-path>">`; in HTML use `<img src>`; never point `<use href>` at a project file. The mock-before-build skill's project-assets reference covers discovery and reject codes.
5. Size for legibility. Keep SVG label text at 13px or larger in viewBox units, with the viewBox width close to the canvas width (1280 for `desktop`, 1440 for `desktop-wide`) so one unit is about one CSS pixel; wrap long labels onto two `<tspan>` lines, and prefer a wider viewport (`desktop-wide`) over shrinking text.
6. Lay out with simple math. Place boxes on a coarse column/row grid, compute arrow endpoints from box edges rather than centers, and draw arrows before boxes so boxes mask the joins.
7. Caption the artifact with what it explains in the `caption` argument, and say in prose what the diagram simplifies or omits.
8. Present the render by copying its `artifact_id` into `artifact_ids`: on `surface_note` mid-run, or in the final reply's metadata block. A worker's renders reach the coordinator in `proof_json.visual_artifact_ids`. Never embed the id as a Markdown or HTML image (`PRESENT_MARKDOWN_EMBED`). A diagram is an explanation, not evidence; claims about running behavior still need their own grounding.

See [layout recipes](references/layout-recipes.md) for adaptable skeletons — pipeline flow, layered architecture, timeline, and comparison cards.
