# PU01 — Store plan rows with the write

- **Layer**: backend progress mutations + session transcript persistence + observation bindings; no Den or OpenAPI change.
- **Depends on**: none. Recheck open PR overlap across the expanded mutation and persistence scope.
- **Invariants**: `I1`–`I5`.
- **Decision**: `decisions/001-store-at-the-write.md`.
- **Prompt-visible entries**: none planned; persistence errors remain honest operation failures.

## Problem

The one-second coalescer can assign a plan update an ordinal after the next assistant row. Replacing it with a synchronous post-commit callback fixes the ordinary sequential race but does not provide commit ordering, atomic durability, or commit-bound run identity.

## Change

1. Read backend policy, SQL persistence, compatibility, and package layering. Inspect budgets before editing large code. Map the four callers' enclosing locks and transactions; identify the session-lock-before-writer composition point and its narrow dependencies.
2. Compose progress mutation and transcript insertion into one database transaction using the existing session transcript persistence machinery. Preserve ordinal allocation, message validation, mutation-event outboxing, and post-commit wakeup. Do not nest a transcript writer transaction inside a progress transaction.
3. Route `author_update.go`, `session/instructions/manager_progress.go`, `app/workerruntime/hooks.go`, and `workflow/fanout_plan.go` through that boundary. Obtain exact before/after snapshots and run identity inside it; remove separate read/write/read event reconstruction. Preserve author revision validation and item identities. Propagate storage errors from host-originated mutations.
4. Move row classification/building to a dependency-appropriate pure component usable by the transaction composition. Preserve the visible diff policy and derive completion eligibility from the durable transition. Remove the process-local completion arm as a correctness mechanism; retain unused required sequence values only until PU02.
5. Remove `progress.Coalescer`, its timer, queues, flush/close lifecycle, and observer-owned transcript appends. Do not add the previously proposed post-commit `Publisher`. Observation bindings retain notification responsibilities after commit.
6. Update `docs/den-chat-items.md` with the causal ordering guarantee, without claiming adjacency to the assistant message or infallible event delivery.

## Tests

- Replace coalescer clock/window tests with the deterministic transaction cases in `systemic-invariants.md`. Preserve useful existing diff, ordinal, and run-stamp assertions at the new boundary.
- Exercise all four production mutation paths with the real SQL store, including stale revision rejection and no-op retry.
- Inject append/commit failures and cancellation; verify plan, transcript, ordinal allocation, and outbox roll back together. Reopen after commit before notification and prove the row remains and existing outbox recovery delivers it.
- Delay and reverse notifications after concurrent commits, starting with no process-local publication history. Cover completion/reopening and run changes explicitly.
- Verify missing-run handling and unbound adoption without silently losing visible changes or seeded content.
- Keep tests deterministic and compatible with `LYCAON_LLM_MOCK=1`. Select exact supported scopes from `./task --list` and `scripts/verification-plan.json` if focused local reproduction is needed; default verification is CI (`milestone-plan.md`).

## Done when

All four mutation paths commit their required rows with the plan transition; no timer or post-commit row-creation path remains. Failure/restart, causal ordering, completion edges, and run identity have executable coverage. The PR documents the transaction composition and lock order. Submit the complete PR ready for CI without waiting on checks. The optional authorized dev replay is qualification evidence tracked separately, not a reason to keep finished implementation in draft.
