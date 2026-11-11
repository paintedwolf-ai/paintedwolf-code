# Briefs and topologies

The dispatch guidance below is coordinator-only: a worker leg applies the matching guidance within its own assignment and reports dispatch needs to the parent.

## Topology selection

| Situation | Dispatch shape | Stop condition |
|---|---|---|
| Unknown repository layout | Parallel bounded read scouts, then builders | Scouts identify assigned paths and the remaining decision |
| Current external fact changes design | Research, coordinator synthesis, then builders | Each blocking claim has fetched evidence or is reported unknown |
| Supplemental external question | Research beside independent builders | Builders have a safe default that does not depend on the answer |
| Stable cross-component interface | One write worker per assigned component | Every shared invariant appears in each affected brief |
| Configuration matrix | One leg per independent configuration group | Every declared cell has an outcome or explicit omission |
| Competing approaches | Independent alternatives against one frozen rubric | Reject losers and integrate one selected result |
| Independent fix set | Partition by root cause and non-overlapping paths | Every finding maps to exactly one leg |
| Integrated change needs confidence | Parallel read-only review and verification legs | Findings are resolved or reported as remaining risk |
| Complementary facts from different roles | Role set in one wave (e.g. web + repo research, research + implement){% if profile_has_record_finding %}; brief `record_finding` sharing{% endif %} | Each role's questions have grounded notes or explicit unknowns |

Do not mix read scouts and write workers in one slice when the scouts are expected to discover write assignments. Do not parallelize stages whose outputs determine the next stage's interface or scope.

## Sizing by shape

Host sizing integers are in [the orchestration procedure](references/orchestrate_a_large_task.md) (**Sizing by shape**{% if profile_has_skills_read and root_count %}, rendered at `skills_read`{% endif %}). This file is topology-only.

Every row above names what to count — components, root causes, configuration groups, areas, roles, approaches. That count is the wave. Prefer{% if profile_has_task %} another focused `task()` over a kitchen-sink brief, and{% endif %} a role set{% if profile_has_record_finding %} with `record_finding` sibling notes{% endif %} when complementary agents should share facts mid-flight.

## Dependencies and shared agreements

Name dependencies in the dispatch fields rather than hoping waves line up:

- `after_workers` — the consumer leg starts only after the listed producer jobs finish and their write overlays are promoted; unusable producer output cancels the wait. Use it for stages that build on promoted output, and do not combine it with `child_session_id` or `scope.base_overlay_id`.
- `scope.base_overlay_id` — stacks a write leg on a pending, unpromoted overlay so both legs edit the same line of work; the base overlay must still promote for the stack to land.
- `child_session_id` — resumes the same worker on its existing snapshot for follow-up with new facts or budget; it keeps the old snapshot, so it is not a way to pick up a sibling's new edits.
- `shared_context` — up to 8192 bytes of interface agreements copied to each collaborating leg; give every collaborator the same text so their interfaces cannot diverge.

## Self-contained worker brief

Use this order so a worker can act without the parent transcript:

1. **Goal:** one observable outcome.
2. **Known facts:** repository receipts or external evidence that the worker may trust as the coordinator's synthesis.
3. **Decisions:** interfaces, formats, naming, compatibility bounds, and choices already settled.
4. **Assigned output:** paths or external state assigned only to this leg.
5. **Constraints:** project policy, generated-file rules, non-goals, and irreversible-action boundaries.
6. **Recommended procedure:** at most one clearly applicable skill, stated as advice.
7. **Done criteria:** edits, checks, evidence, and completion-report expectations.
8. **Allowed uncertainty:** questions that may remain unresolved without blocking the leg.

Avoid pasted conversation, loaded conclusions, open-ended repository searches, or instructions to wait for sibling file state. If a worker must learn a fact before acting, make that a prior leg.
