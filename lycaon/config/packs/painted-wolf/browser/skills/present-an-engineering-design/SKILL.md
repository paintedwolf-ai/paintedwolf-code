---
name: present-an-engineering-design
description: Present system designs, proposals, decision briefs, developer research, or workflow prototypes.
license: Apache-2.0
metadata:
  author: painted-wolf
  paintedwolf.template_resources: references/build_a_system_design_review.md|references/build_an_engineering_decision_brief.md|references/write_an_implementation_proposal.md|references/synthesize_developer_research.md|references/prototype_a_coding_workflow.md|references/mock_an_agent_control_plane.md
---

# Present an engineering design

1. **Entry check.** Confirm the task involves an architectural choice, cross-component redesign, formal implementation proposal, or workflow prototype requiring stakeholder review. If the change is routine, single-site, or already fully specified, exit this skill and implement directly.
2. **Select the procedure.** Choose exactly one matching procedure below based on the request. Read that reference before acting:
   - [Build an engineering decision brief](references/build_an_engineering_decision_brief.md) — Create a recommendation-first brief with option scorecards, evidence, risks, and an explicit ask when a technical choice needs approval. Supporting [layout](references/build_an_engineering_decision_brief-layout.md).
   - [Build a system design review](references/build_a_system_design_review.md) — Build an architecture review with boundaries, interfaces, decisions, failure recovery, and rollout when a design needs technical scrutiny. Supporting [layout](references/build_a_system_design_review-layout.md).
   - [Write an implementation proposal](references/write_an_implementation_proposal.md) — Produce an engineering proposal with end states, scope contracts, phased workstreams, and acceptance criteria for substantial changes. Supporting [layout](references/write_an_implementation_proposal-layout.md).
   - [Synthesize developer research](references/synthesize_developer_research.md) — Turn developer interviews, feedback, or task logs into a visual synthesis with themes, contradictions, and limits. Supporting [layout](references/synthesize_developer_research-layout.md).
   - [Prototype a coding workflow](references/prototype_a_coding_workflow.md) — Create a clickable coding-tool workflow to evaluate states, approvals, and recovery before implementation. Supporting [layout](references/prototype_a_coding_workflow-layout.md).
   - [Mock an agent control plane](references/mock_an_agent_control_plane.md) — Design a data-dense control plane for monitoring tasks, worktrees, approvals, and verification. Supporting [layout](references/mock_an_agent_control_plane-layout.md).
3. **Assemble the evidence ledger.** Extract observed facts from source code, tests, or telemetry. Every quantitative metric must carry units, baseline, and source attribution. If evidence cannot distinguish options, deliver an evidence-gap brief stating the missing claim rather than manufacturing estimates.
4. **Compose the layout.** Apply the HTML/CSS structure from the companion `-layout.md` file using `--kit-*` tokens, catalog fonts, and `.kit-surface` cards. Maintain an answer-first hierarchy: title, recommendation or design claim, supporting evidence/scorecard, risk register, and the decision ask.
5. **Render the artifact.** For a brief, review board, or proposal, call `render_view` with `viewport.preset: "desktop-wide"` and `viewport.fit: "content"` for a complete capture. Prototypes and control planes follow their procedure: a local route when interaction is the subject, `render_view` only for static state boards.
6. **Inspect the visual.** Confirm recommendation visibility, verify that numbers and labels are not truncated, ensure body text remains legible, and verify that color is not the sole indicator of risk or status.
7. **Return the design handoff.** Emit the structured handoff fields defined in the selected procedure (`recommendation`, `decision_needed`, `material_risks`, `open_questions`, and limits), and present renders by their ids in `artifact_ids` or name the local route. Explicitly state that visual artifacts represent authored intent and analysis, not runtime verification.
