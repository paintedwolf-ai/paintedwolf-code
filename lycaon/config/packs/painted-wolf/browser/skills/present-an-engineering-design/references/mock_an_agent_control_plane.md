# Mock an agent control plane

Enter when the user needs an operational surface for observing or coordinating multiple coding-agent tasks. If the subject is one task's step-by-step journey, prototype a coding workflow instead; if the task is to implement an already specified screen, follow the product design directly.

1. Write an **operator contract** with `operator`, `decision cadence`, `entities watched`, `interruptions`, `actions`, and `cost of missing a signal`. Pick one primary job for the first viewport: notice, triage, approve, recover, or compare.
2. Define a **closed entity table** before drawing cards. A task row normally carries `task id`, `repository`, `workspace`, `phase`, `age`, `change size`, `verification freshness`, `approval state`, `health`, and `owner`. Omit fields that the host cannot establish; never convert missing state into green.
3. Create an **attention model** with three to five mutually distinct states. Each state has a structured cause, visible label, ordering priority, owner, and next action. Use color as reinforcement only; labels such as `Blocked`, `Review waiting`, and `Evidence stale` remain readable without it.
4. Design the **first viewport** around one useful result: compact product bar, context and filters, three to five summary facts, one trend or queue-health view when real data supports it, task worklist, and selected-task detail. Avoid a marketing hero and avoid metric cards that do not change an operator decision.
5. Read [the agent-control-plane layout](references/mock_an_agent_control_plane-layout.md). Build a local route by default so task selection, filters, drawers, stale state, and approval transitions can be reviewed. Use `render_view` only for a static density or hierarchy comparison.
6. Add **representative states**: healthy task, review waiting, structured failure, stale evidence, approval held, empty filtered result, and partial connectivity when each belongs to the product. Keep the last settled data visible during refresh or failure.
7. Wire three interactions at minimum: change filter, select a task, and take or inspect the primary attention action. Use local fixtures unless the user explicitly requests a live backend. Use a throwaway service or container only when backend behavior itself is under review; bind it to loopback and stop only what this task started.
8. Exercise the primary job and one recovery path with the managed page tools. Measure document overflow and worklist/detail alignment at 768px and 1280px. Stop after those two widths and the required states; do not turn a mockup review into exhaustive end-to-end testing.
9. Return a **control-plane handoff** with `operator_job`, `entity_fields`, `attention_model`, `states`, `interactions`, `inert_controls`, `open_questions`, and the local route or artifact ids.

Worked task row:

| Task | Repository | Phase | Change | Verification | Attention | Next action |
|---|---|---|---:|---|---|---|
| PW-1842 | agent runtime | Review | 4 files | Race harness passed 4m ago | Review waiting | Open diff |

When no representative operational data exists, use clearly labeled synthetic fixtures and remove claims about real throughput, failure rate, or productivity. The mockup may test information hierarchy without pretending to measure the system.
