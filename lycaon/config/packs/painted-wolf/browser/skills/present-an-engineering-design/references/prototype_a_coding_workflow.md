# Prototype a coding workflow

Enter when the subject is a developer or coding-agent journey whose quality depends on transitions between states. If one static screen answers the design question, use an ordinary mockup instead; do not build a miniature application for a layout-only choice.

1. Write a **scenario contract** with fields for actor, repository state, task, entry signal, authority boundary, success, and escape path. Keep one primary scenario; secondary scenarios become selectable fixtures, not new products.
2. Produce a **state matrix** with five to eight rows. Include entry, investigation or work-in-progress, proposed change, review or approval, verification, and terminal outcome when they exist. Add loading, empty, stale, conflict, or failure only when the workflow can genuinely enter that state. Columns are `state`, `trigger`, `visible facts`, `available actions`, `next state`, and `recovery`.
3. Create a **screen map** with four to seven connected views or one workspace whose regions change by state. Preserve task identity, repository, current evidence, and rollback across every transition so the reviewer never loses the thread.
4. Read [the coding-workflow layout](references/prototype_a_coding_workflow-layout.md). Build a local route when clicks, forms, drawers, approvals, or async transitions are the subject. Use `render_view` only for a static state board; render each state as a separate artifact on the same viewport.
5. Populate the prototype with realistic but clearly synthetic code paths, commands, diffs, check results, timestamps, and failure text. Never present invented test results or repository state as observations about the attached project.
6. Wire the **critical path** end to end. Every visible primary action must either change the state, open an intentionally scoped surface, or be visibly disabled with a reason. Do not ship decorative controls that imply nonexistent behavior.
7. Exercise the critical path with `page_open`, `page_snapshot`, and `page_act`; capture the entry, authority boundary, failure or conflict, and terminal outcome. Use `measure_page` for overflow, alignment, or viewport-fit claims. Stop after one successful pass plus one recovery pass.
8. Return a **prototype handoff** with `scenario`, `states_covered`, `interaction_path`, `open_questions`, `known_inert_controls`, and the local route or artifact ids. Label the prototype as authored intent, not observed product behavior.

Worked state row:

| State | Trigger | Visible facts | Available actions | Next state | Recovery |
|---|---|---|---|---|---|
| Review requested | Scoped checks pass | 4 files, +86/−31, exact test command, worktree identity | Open diff, accept, request changes | Integrated or revising | Discard worktree; retain receipts |

If the local server cannot start, fix one project-local configuration or compile error when it is clearly caused by the prototype. Otherwise return static state renders and name interaction verification as unavailable; do not replace the product stack or kill an unrelated listener.
