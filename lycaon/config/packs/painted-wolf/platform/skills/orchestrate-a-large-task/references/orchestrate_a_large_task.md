# Orchestrate a large task

**Entry check:** use this procedure for work with several useful worker outcomes, concurrent or dependent. A single bounded assignment also fits when isolation, a worker capability, or extra context materially helps. Keep trivial work inline.

The dispatch steps below are coordinator-only: a worker leg applies the matching decomposition, dependency, and verification guidance within its own assignment and reports dispatch needs to the parent.

## Workflow

1. Investigate until you can name the deliverables, stable interfaces, constraints, likely paths, and unresolved decisions. Use current external research before dispatch when a changing fact can alter the design.
2. Classify every unresolved fact as blocking or supplemental. Finish blocking research before dependent builders start. Run supplemental research in parallel only when builders have a safe default.
3. Author the progress checklist before the first dispatch — one row per deliverable, updated from worker outcomes. Do not keep a parallel prose plan. Open rows identify remaining outcomes, not a required worker count.
4. Match the topology to the work with **Sizing by shape** below, and read [briefs and topologies](references/orchestrate_a_large_task-briefs-and-topologies.md) when choosing between scouts, staged research, responsibility fan-out, configuration fan-out, alternatives, role sets, or independent fixes. Choose coherent assignments within the available capacity.{% if profile_has_task %}
5. Pick each worker's `agent_type` from the spawn roster in your prompt. It is host-generated and lists every id you may pass, what each one may edit, and which run commands; an id that is not on it rejects with `DISALLOWED_AGENT`. Match the roster entry's capability to the leg's effect: reading and research, product edits, test edits, review, or running commands.
6. Give each `task()` one focus and a self-contained `brief`: one goal, only material facts and references, decided constraints, and concrete `done_when`. Workers cannot read this conversation or each other's files.{% else %}
5. Apply the worker guidance in the note above to your own assignment; verify your outcome against its `done_when` before reporting.
6. Do not dispatch; report what a dispatch decision needs to the parent.{% endif %}
7. Set the remaining fields deliberately:
   - `files` — likely starting points only, not a reading list.
   - `scope.paths` — optional focus hints for any worker; they do not limit access or promotion.
{% if profile_has_instruct_worker %}   - To continue an existing worker's leg, use `instruct_worker` instead of `task`.
{% endif %}   - `scope.base_overlay_id` — only for an intentional dependency on a pending write overlay.
   - `after_workers` — sibling producers that must deliver a complete result and land their changes before this leg starts; a producer sent back holds this leg until it delivers again. Do not combine it with `scope.base_overlay_id`.
   - `shared_context` — up to 8192 bytes of interface agreements copied to each collaborating leg; give collaborators the same text.
8. Add a recommended worker procedure to `brief.constraints` when one listed skill clearly fits: `Recommended procedure: use the <name> skill.` This does not replace a complete brief. Do not prescribe a skill because its name is adjacent to the topic, and do not invent a task argument for skills.
9. Keep dependent stages sequential; when a consumer must start only after a producer's overlay is promoted, name the producer jobs with `after_workers`.{% if profile_has_record_finding %} Workers may exchange short grounded findings via `record_finding`, but a message arriving after a design choice is not a prerequisite.{% endif %}
10. Wait for every dispatched leg, inspect complete envelopes, integrate write overlays explicitly, repair partial legs, and verify the combined result.

## Sizing by shape

Host in-flight cap **{{ max_in_flight }}**. A leg runs as long as its work takes; you hear when a round runs long or goes quiet.

Use these shapes to identify useful assignments. Group small related outputs when separate workers would add little value.

| Shape | Count |
|---|---|
| Responsibility fan-out — a stable interface with several components behind it | one leg per component with assigned output paths |
| Independent fix set — findings partitioned by root cause | one leg per root cause, non-overlapping paths |
| Configuration matrix — the same change across independent groups | one leg per group |
| Hunt / breadth — unknown layout across distinct areas | one bounded read leg per area; **~{{ hunt_wave_workers }}** in a wave is ordinary when scopes do not duplicate |
| Role set — one deliverable needing complementary roles | one leg per role, same wave{% if profile_has_record_finding %}, briefed to share via `record_finding`{% endif %} |
| Competing approaches — one decision, several candidate designs | one leg per approach, judged against one frozen rubric |
| Deep single thread — sequential diagnosis or one assigned deep edit path that materially benefits from isolation, a worker-only capability, or extra context | 1 |
| Throwaway / bounded — smoke, verify, single-path check | 1 per check |

Useful independent legs can overlap within the host cap; duration and shared resources still determine latency. The host wakes you per envelope. Start independent outcomes together; sequence legs for dependencies you can name. Use one worker when one bounded assignment is useful.

A long round is not a failed one. A worker that cannot finish reports what remains in its result; resume it on the same child, or re-scope the leg.

## Stopping rule

Orchestration ends when every checklist row is closed by an integrated overlay, an explicit rejection, or a named blocker — not when the last worker returns. Repair a partial leg at most once; a leg that fails twice is a decomposition problem, so re-scope it or take it inline rather than dispatching a third time. Do not add a wave to chase a deliverable no one claimed: put it on the checklist and decide it deliberately.

## Dispatch shape

Batch ready peers when practical; additional independent legs may follow while workers run. `brief` is a structured object, not prose.

```text
task(
  agent_type="<id from the spawn roster>",
  brief={
    "goal": "Implement the named component.",
    "known_facts": ["<fact the leg cannot recover on its own>"],
    "constraints": ["<settled interface, naming, or non-goal>",
                    "Recommended procedure: use the `evolve-a-system` skill."],
    "done_when": ["<observable acceptance criterion>",
                  "verify passes on the final revision"],
    "context_refs": ["path/to/reference.go:120"]
  },
  files=["named/start/path"],
  scope={mode: "write", paths: ["assigned/**"]}
)
```

## Report

State the topology and why, each leg's outcome (integrated, repaired, rejected, still open), the combined verification evidence, and any deliverable no leg claimed. Report only evidence that survived integration.
