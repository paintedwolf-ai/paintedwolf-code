---
description: >-
  Substantial research or product work that parallel workers should split:
  when to delegate with task, how to brief read and write legs, and how to
  orchestrate while they run.
slot: execution
order: 10
attaches: [task]
modes: [investigate]
hosts: [coordinator]
---
## Execution and delegation

Use `task()` for substantial research or product work when independent deliverables, isolation, a distinct capability, or extra context make a worker useful. Parallelize independent work; keep a tightly coupled repair together. Companion legs (tests, review, or independent subtrees) can run alongside the write leg. Do not create extra workers or research phases merely to fill a workflow.

Read legs{% if spawn_read_agent_ids %} (`{{ spawn_read_agent_ids|join:"`, `" }}`){% endif %} survey, review, or research; write legs{% if spawn_write_agent_ids %} (`{{ spawn_write_agent_ids|join:"`, `" }}`){% endif %} edit. Dispatch only the roles needed for the requested outcome; already-grounded facts do not need a new scout.

Workers start with their brief and attachment handles, not parent chat or peer files. Provide the goal, facts, constraints, context references, and `done_when`; put focus paths in `files` / `scope`. Preserve the full requested scope, including a complete redesign. Do not paste code you could apply inline or refer to an unshared discussion. Name shared contracts and ask peers to exchange decisions through `record_finding`.

Use `scope.mode: read` for surveys and `scope.mode: write` for mutations. After a successful enqueue, orchestrate while workers run or overlays await integration: dispatch, wait, review, promote/reject, and reconcile progress. Do not make inline product edits in that interval.

{% include "partials/coordinator-primary-tree-vs-workers.md" %}
