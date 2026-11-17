# Milestone plan

## Order

PU01 and PU02 ship together in PR #2467, based on `main` at `1c29645396`. There are no unlanded prerequisites. The exact-transition builder removes process-local counters directly, and the wire cleanup accompanies it in the same reviewable change.

## Baseline and overlap

Source review used `1c29645396` on 2026-11-17. The earlier overlap sweep identified draft #2435 under `internal/progress`; that sweep predates the expanded PU01 scope and is not current admission evidence. Refresh open PR overlap at implementation start and before marking ready, including session transcript persistence, all four callers, app composition, and completion handling. Use isolated worktrees under this folder's `worktrees/` for repository implementation.

## Verification per PR

Use GitHub CI by default. Push unfinished work to a draft PR; mark complete work ready regardless of pending CI, and follow its checks through success as explicitly requested for this implementation. Do not run local gates first or repeat checks CI passed on the same commit. Do not enable auto-merge or merge without authorization.

- **PU01**: CI verifies the affected backend scopes, changed-statement coverage, and budgets. All agent-run testing and generation use the authorized Linux or Mac SSH host. If a failure needs remote reproduction, check `./task test:status`, read the selection/receipt guidance, and choose the smallest supported target covering the failure. Relevant areas include progress, observations, session store/instructions, worker runtime, and workflow. Do not assume the original two-package digest covers the expanded scope.
- **PU02**: generation is implementation work, submitted separately through `./task openapi:bundle` and `./task codegen:den-types`. CI verifies wire drift, affected Go tests, Den types/tests, and budgets. Local reproduction is scoped to an actual need; a full Den run is not a default prerequisite.
- **Planning prose**: review diffs, links, invariants, and policy consistency; no build/lint/test gate.

## Dev acceptance evidence

After PU01 is available in a dev build, prefer an existing naturally occurring debug session. A new live-model run requires explicit authorization; this plan does not authorize one. Use the isolated harness/sidecar procedure for any agent-run probe and do not interfere with Den's shared engine.

For an authorized or already captured session with multiple `update_progress` calls:

1. Export message events with `evidence/export-events.py <debug-session-dir> <out.json>`.
2. Replay through Den from `lycaon-den/` with `SRC=$PWD/src ./node_modules/.bin/vite-node <plan>/evidence/replay-row-order.ts <out.json>`. Any prerequisite managed preparation still enters through `./task`.
3. Expect zero `new command above another step's plan` events attributable to delayed progress publication. An approval beneath its own command is expected. Check that each tool-originated plan row follows its calling assistant row and precedes its call result/next model step; do not require adjacent ordinals. Evaluate bootstrap and phase-entry rows against their own causal operations.
4. Record the build commit, source session, counts, and any unrelated disturbances in `STATUS.md`. Replay is UI ordering evidence, not proof of rollback, crash recovery, run identity, or all concurrent schedules; deterministic transaction tests provide those contracts.

## Implementation overlap review

The expanded sweep found #2466 in transcript documentation; #2445 in generated wire and session tests; #2435 in app composition and progress contracts; #2434/#2398 in the wiring harness; #2417 in search property fixtures; and #2437/#2430/#2411/#2391 in generated wire. These are overlap observations, not dependencies or claims about their current readiness. This branch changes its own scoped call sites and regenerates wire outputs from its source.
