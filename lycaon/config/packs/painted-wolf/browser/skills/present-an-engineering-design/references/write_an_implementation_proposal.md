# Write an implementation proposal

Enter when the user needs agreement on how a substantial engineering change will be delivered. If the unresolved issue is which architecture to choose, build a system design review first; if the user asked to implement an already accepted brief, do not pause for a proposal.

1. Create a **proposal frame** with `audience`, `sponsor`, `problem`, `desired_outcome`, `decision_needed`, and `valid_until`. Use `Unassigned` or `Not set` for missing governance; never fabricate a sponsor or deadline.
2. Write a **current-state diagnosis** backed by three to five observed facts. Separate symptoms, causes, and constraints. A proposal may describe inferred consequences only when it labels the inference and its basis.
3. Define the **end state** as three to six externally checkable outcomes. Each outcome carries a measure and an acceptance observation; avoid output-only goals such as “build a service” or “improve quality.”
4. Produce a **scope contract** with `in scope`, `not in scope`, `assumptions`, `dependencies`, and `durable surfaces`. Name deleted or retired internal paths when greenfield replacement is intended; name migrations or additive seams when consumers are durable.
5. Build a **delivery plan** with two to five workstreams and three to six phases. Every phase has an outcome, owner role, prerequisites, exit evidence, and a failure branch. Show dependency order instead of implying that parallel-looking boxes can all begin together.
6. Add a **resource and risk page**. State team shape, elapsed range, direct cost only when supplied or defensibly modeled, operational load, top risks, mitigations, and the explicit decision ask. Mark estimates with assumptions and ranges.
7. Read [the implementation-proposal layout](references/write_an_implementation_proposal-layout.md), then render a branded, print-aware HTML proposal. Use `render_view` for a cover plus one-page summary; use a local route when the proposal needs multiple printed pages or expandable technical detail.
8. Inspect the artifact at screen and print widths. Ensure the first two pages communicate the problem, end state, scope boundary, and ask; tables do not split illegibly; and no decorative section displaces decision-critical content.
9. Return a **proposal handoff** with `end_state`, `scope`, `sequence`, `resources`, `risks`, `decision_needed`, `assumptions`, and the artifact or local route.

Worked phase row:

| Phase | Outcome | Prerequisite | Exit evidence | Failure branch |
|---|---|---|---|---|
| Address | Every task receives a stable workspace identity at spawn | Git capability probe | Repository matrix creates and removes isolated worktrees without checkout writes | Keep same-directory default and publish unsupported cases |

If time, cost, or staffing data is missing, show the delivery shape and label those commercial fields `To be estimated after scope confirmation`. Do not turn placeholders into confident numbers for visual completeness.
