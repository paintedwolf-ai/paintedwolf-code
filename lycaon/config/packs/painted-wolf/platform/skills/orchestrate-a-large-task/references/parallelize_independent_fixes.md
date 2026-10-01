# Parallelize independent fixes

**Entry check:** you have a list of concrete items, each with its own evidence, and fixing one does not change the diagnosis of another. If the items likely share one root cause, fix that first — a wave of workers all patching the same bug produces conflicting overlays.

## Workflow

1. Normalize the input into an item table before counting workers. One row per item, each row carrying the evidence that proves it exists:

   | Item | Evidence | Suspected root cause | Assigned paths | Leg |
   |---|---|---|---|---|
   | `TestCheckoutTotal` fails | `want 1050, got 1000` | rounding in `price.go` | `billing/price*.go` | 1 |
   | `TestCartTotal` fails | `want 1050, got 1000` | rounding in `price.go` | `billing/price*.go` | 1 |
   | SARIF `G402` in `tls.go` | finding id `G402-3` | separate | `net/tls.go` | 2 |

2. Group by root cause first. Items sharing a suspected cause go in one leg, as in rows 1–2 above — splitting them races two workers to write the same fix.
3. Assign every item to exactly one leg, and give each leg non-overlapping paths. Assign a shared helper to one leg, or to a foundational leg that runs first and completes before the rest dispatch.
4. Pick each leg's `agent_type` from the spawn roster in your prompt, matching the roster entry's capability to the leg's effect.
5. Put item ids and evidence in `brief.known_facts`, boundaries in `brief.constraints`, and completion checks in `brief.done_when`. Recommend a focused skill when one fits — `design-and-debug-tests`, `design-and-debug-tests`, `triage-security-findings`, or the applicable migration procedure.
6. Require every worker to account for each assigned item as fixed, invalid, duplicate, blocked, or still failing. A green command without per-item accounting is an incomplete result, not a pass.
7. Wait for all legs, integrate complete overlays deliberately, and re-dispatch only the specific items a partial leg left unresolved.
8. Run one combined verification after integration. Isolated workers cannot observe shared-state and ordering failures, so this run is the only evidence that the fixes compose.

## Stopping rule

Reconcile against the original item table: every row ends in one of the five states. Items nobody mentioned are unresolved, never assumed passing. Re-dispatch at most once per item — an item that fails twice needs diagnosis, not another worker.

## Report

The item table with each row's final state, which legs were re-dispatched and why, the combined verification result, and any item still unresolved with its blocker.

Work sequentially instead when items overlap heavily, when one foundational change will invalidate later diagnoses, or when the verification environment cannot run safely in parallel. For wave width and `max_tool_loops` bands, follow the sizing table in `references/orchestrate_a_large_task.md`.
