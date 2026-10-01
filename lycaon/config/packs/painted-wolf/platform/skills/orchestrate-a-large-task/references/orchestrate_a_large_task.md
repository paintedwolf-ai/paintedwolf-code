# Orchestrate a large task

**Entry check:** use this procedure for work with several useful worker outcomes, concurrent or dependent. A single bounded assignment also fits when isolation, a worker capability, or extra context materially helps. Keep trivial work inline.

## Workflow

1. Investigate until you can name the deliverables, stable interfaces, constraints, likely paths, and unresolved decisions. Use current external research before dispatch when a changing fact can alter the design.
2. Classify every unresolved fact as blocking or supplemental. Finish blocking research before dependent builders start. Run supplemental research in parallel only when builders have a safe default.
3. Author the progress checklist before the first dispatch — one row per deliverable, updated from worker outcomes. Do not keep a parallel prose plan. Open rows identify remaining outcomes, not a required worker count.
4. Match the topology to the work with **Sizing by shape** below, and read [briefs and topologies](references/orchestrate_a_large_task-briefs-and-topologies.md) when choosing between scouts, staged research, responsibility fan-out, configuration fan-out, alternatives, role sets, or independent fixes. Choose coherent assignments within the available capacity.
5. Pick each worker's `agent_type` from the spawn roster in your prompt. It is host-generated and lists every id you may pass, what each one may edit, and which run commands; an id that is not on it rejects with `DISALLOWED_AGENT`. Match the roster entry's capability to the leg's effect: reading and research, product edits, test edits, review, or running commands.
6. Give each `task()` one focus and a self-contained `brief`: one goal, only material facts and references, decided constraints, and concrete `done_when`. Workers cannot read this conversation or each other's files.
7. Set the remaining fields deliberately:
   - `files` — likely starting points only, not a reading list.
   - `scope.paths` — optional focus hints for any worker; they do not limit access or promotion.
   - `max_tool_loops` — omit unless the shape table names a number; the host funds a productive leg on its own.
   - `child_session_id` — only to continue the same leg with new facts or budget.
   - `scope.base_overlay_id` — only for an intentional dependency on a pending write overlay.
8. Add a recommended worker procedure to `brief.constraints` when one listed skill clearly fits: `Recommended procedure: use the <name> skill.` This does not replace a complete brief. Do not prescribe a skill because its name is adjacent to the topic, and do not invent a task argument for skills.
9. Keep dependent stages sequential. Workers may exchange short grounded findings via `record_finding`, but a message arriving after a design choice is not a prerequisite.
10. Wait for every dispatched leg, inspect complete envelopes, integrate write overlays explicitly, repair partial legs, and verify the combined result.

## Sizing by shape

Host in-flight cap **{{ max_in_flight }}**; worker default **{{ worker_tool_budget_default }}**, range **{{ worker_tool_budget_min }}**–**{{ worker_tool_budget_max }}**.

Use these shapes to identify useful assignments. Group small related outputs when separate workers would add little value.

| Shape | Count | `max_tool_loops` |
|---|---|---|
| Responsibility fan-out — a stable interface with several components behind it | one leg per component with assigned output paths | omit; host default |
| Independent fix set — findings partitioned by root cause | one leg per root cause, non-overlapping paths | omit; host default |
| Configuration matrix — the same change across independent groups | one leg per group | omit; host default |
| Hunt / breadth — unknown layout across distinct areas | one bounded read leg per area; **~{{ hunt_wave_workers }}** in a wave is ordinary when scopes do not duplicate | omit; host default |
| Role set — one deliverable needing complementary roles | one leg per role, same wave, briefed to share via `record_finding` | omit; host default |
| Competing approaches — one decision, several candidate designs | one leg per approach, judged against one frozen rubric | omit; host default |
| Deep single thread — sequential diagnosis or one assigned deep edit path that materially benefits from isolation, worker-only capability, or extra context/tool budget | 1 | ~**{{ deep_tool_loops_hint }}** |
| Throwaway / bounded — smoke, verify, single-path check | 1 per check | **{{ throwaway_tool_loops_min }}–{{ throwaway_tool_loops_max }}** |

Useful independent legs can overlap within the host cap; duration and shared resources still determine latency. The host wakes you per envelope. Start independent outcomes together; sequence legs for dependencies you can name. Use one worker when one bounded assignment is useful.

`max_tool_loops` bounds a leg; it does not buy quality. A worker that needs more rounds asks with `request_budget` and names the work they cover; grant it with `extend_worker_budget` when that work is worth it, or decline it with `decline_worker_budget`. Answer promptly; the worker's final round waits for the answer. Without a grant it returns partial results and unknowns at the bound and can be resumed on the same child, so leaving the field out is the normal choice.

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
