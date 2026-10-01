# Parallelize a configuration matrix

**Entry check:** the same acceptance criteria apply to every cell, and the cells differ only by configuration. If a cell needs different criteria, it is separate work, not a matrix row.

## Workflow

1. Write the matrix out before dispatch — one row per required cell, with every dimension as a column. Record deliberate omissions as rows marked untested; do not let workers choose coverage.
2. Freeze one result shape every cell reports back:

   | Cell | Setup | Action | Outcome | Evidence | Blocker |
   |---|---|---|---|---|---|
   | node 22 · linux | `nvm use 22` | `npm test` | pass | 412 passed, 0 failed | — |
   | node 20 · linux | `nvm use 20` | `npm test` | product failure | 3 failed, `date-fmt.test.ts` | — |
   | node 22 · windows | — | — | untested | — | no runner |

3. Group cells into legs when setup dominates execution or one worker can run several without losing isolation. One worker per cell is a choice, not the default.
4. Pick each leg's `agent_type` from the spawn roster in your prompt, matching the roster entry's capability to what the leg does: running builds and tests, editing product source, or writing configuration-specific tests. A leg that only runs commands must not get a write scope.
5. Repeat the exact configuration in `brief.known_facts` and shared acceptance criteria in `brief.done_when`. Recommend the same platform or build skill when it applies to every leg.
6. Keep outputs isolated. Do not dispatch concurrent write legs to shared paths unless they are intentionally producing alternatives for later selection.
7. Wait for every declared group and assemble the result matrix. Classify each cell as pass, product failure, environment failure, unsupported, or untested — five outcomes, never collapsed into pass/fail.
8. Integrate only source changes that generalize under the acceptance contract, then rerun the smallest representative combined matrix that would catch integration drift.

## Stopping rule

The run ends when every declared cell carries one of the five outcomes and the representative rerun is green. An environment failure is a final state for that cell, not a reason to keep retrying — retry a cell once, then record it as environment-failed with the reason. Never widen the matrix mid-run to explain a failure; finish the declared cells and propose the new dimension separately.

## Boundaries

- Do not claim matrix support from a single successful cell.
- Do not merge unavailable and failing configurations into one status; "no runner" and "test failed" lead to opposite next actions.
- Do not let a worker widen its own cell list.

## Report

The filled matrix, the five-way classification per cell, which source changes were integrated, the rerun evidence, and every cell left untested with the reason.
