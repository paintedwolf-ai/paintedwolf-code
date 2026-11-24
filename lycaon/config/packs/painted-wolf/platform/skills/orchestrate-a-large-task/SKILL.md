---
name: orchestrate-a-large-task
description: Split broad work across task workers and integrate it, for staged research, parallel fixes, configuration matrices, competing approaches, or repo-wide rule sweeps.
metadata:
---

# Orchestrate a large task

**Entry check:** the work has several useful worker outcomes, concurrent or dependent, or one bounded assignment that benefits from isolation, a worker capability, or extra context. Keep trivial work inline.{% if not profile_has_task %}

You are a worker for this procedure: apply decomposition, dependency, and verification within your own assignment and report work that needs another role to the parent. Do not dispatch, invent worker results, or widen scope.{% endif %}

## The loop

1. **Investigate until you can name** the deliverables, stable interfaces, constraints, likely paths, and open decisions.
2. **Classify each open fact as blocking or supplemental.** Finish blocking research before dependent builders start.
3. **Seed deliverables**{% if profile_has_update_progress %} via `update_progress(replace=[…])`{% endif %}{% if profile_has_task %} before first dispatch{% endif %} (one pending row per outcome){% if profile_has_update_progress %}; close rows via `update` and `acknowledge_results`{% endif %}.
4. **Choose topology from the work's shape**, not capacity. At most **{{ max_in_flight }}** workers run at once.{% if profile_has_task %}
5. **Pick `agent_type` from the prompt's spawn roster**, matching effect (read, edit, test, review, run). Unlisted ids fail.
6. **Give each `task()` one focus and a self-contained `brief`**: goal, facts, constraints, concrete `done_when`.{% if profile_has_instruct_worker %} Reuse an idle worker in that area via `instruct_worker` for follow-ups.{% endif %} Workers share neither this chat nor sibling files.{% else %}
5. **Verify your own assignment against its `done_when` before reporting.**
6. **Hand back evidence, not prose**: integrated overlays, explicit rejections, or a named blocker.{% endif %}
7. **Keep dependent stages sequential.**{% if profile_has_record_finding %} `record_finding` shares short grounded findings; it is not a prerequisite channel.{% endif %}
8. **Wait for every worker, inspect complete envelopes, integrate write overlays explicitly, and verify the combined result.**

Stop when every checklist row is closed by an integrated overlay, an explicit rejection{% if profile_has_shelve_overlay %} or shelved result{% endif %}, or a named blocker. Send a partial result back to its worker{% if profile_has_instruct_worker %} with `instruct_worker`{% endif %} at most once; a second failure is a decomposition problem.

## Procedures

{% if profile_has_task %}Read the full orchestration procedure before the first dispatch; it holds the sizing table, the `task()` fields, and the dispatch shape.

- [Orchestrate a large task](references/orchestrate_a_large_task.md): briefing, width, and integration mechanics. Topology choices: [briefs and topologies](references/orchestrate_a_large_task-briefs-and-topologies.md).
- [Research then implement](references/research_then_implement.md): versions, APIs, standards, or unfamiliar architecture could change the build.
- [Compare implementation approaches](references/compare_implementation_approaches.md): architecture, library, algorithm, or migration shape is undecided; judge isolated alternatives against a frozen rubric.
- [Parallelize a configuration matrix](references/parallelize_a_configuration_matrix.md): one acceptance contract across platforms, versions, providers, or flags.
- [Parallelize independent fixes](references/parallelize_independent_fixes.md): separable failures or findings partitioned by root cause without overlapping paths.
- [Sweep a policy class](references/sweep_a_policy_class.md): one rule applied everywhere in scope, with a guard so it cannot return.
{% else %}The orchestration procedure and its references are held by the coordinating parent; request specifics through your report if a worker needs one.
{% endif %}
## Report

The topology and why; each worker's outcome (integrated, repaired, rejected, open); the combined verification evidence; and any deliverable no worker claimed.
