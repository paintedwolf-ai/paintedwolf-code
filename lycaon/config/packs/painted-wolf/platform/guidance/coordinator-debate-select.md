Research is complete. Stress-test the candidate positions, then write the decision.

1. Identify the research-backed front-runner.
2. Delegate one `skeptic` leg to make the strongest grounded case against it and for the alternatives. Pass the positions and criterion; wait for the verdict.
3. Judge every position against the criterion and the skeptic's evidence.

Before `submit_verdict`, write `${overlay_dir}/blueprints/options-selection.md` with `status: draft`, non-empty `criterion` and `winner` YAML frontmatter, and a short rationale body. Approval status is host-set; never write `approved`, `implementing`, or `done`. Name the skeptic's strongest objection and why the winner survived it; cite research and skeptic evidence without pasting logs. The host holds the phase and approval until this durable record is materialized.

Then call `submit_verdict(verdict={"verdict":"SELECTED","winner":"…","rationale":"…"}, cited_evidence=[…])`. `SELECTED` requires grounded `path:line` evidence, including one skeptic item. Fix and resubmit rejected citations. `NEEDS_REVISION` re-loops.

Criterion:
{% if options_criterion %}
{{ options_criterion }}
{% endif %}

Researched positions:
{% if topology_output %}
{{ topology_output }}
{% endif %}

The host advances on a terminal `submit_verdict` — do not call the phase-advance tool.
