# Status

**State**: implemented in [PR #2467](https://github.com/paintedwolf-ai/paintedwolf-code/pull/2467), paused at the user’s request; PR is draft with one known CI compile failure. No merge or deployment performed.

| Id | Branch | Head | PR | State |
|---|---|---|---|---|
| `PU01` + `PU02` | `fix/plan-transcript-transactions` | `58e6f84e24e1f106caec9571331bcf44a828c458` | #2467 | implemented together; paused with CI failure |

## Worktrees

- Local: `worktrees/implementation`, based on `main` at `1c29645396`.
- Linux: `cdbeckman@10.0.20.5:/home/cdbeckman/plan-transcript-transactions`. Remote work is ephemeral; source is pushed, and scoped verification evidence is copied into `evidence/verification/` here.

## Resume log

- **2026-11-16**: Row-placement work built and marked ready as #2369 (host: a note is stored before its assistant row's calls show) and #2370 (Den: a row keeps the place it is first drawn at). The host plan-update timer was left out because the sessions replayed then showed no misplacement from it.
- **2026-11-17**: #2465 delivered both. The dev build's first session on that commit showed notes, diffs and commands holding their place, and 3 of 7 plan updates landing below a later command (`evidence.md` § 5). Traced to the one-second timer. Decided to store plan rows at the write and retire `seq` (`decisions/001-store-at-the-write.md`, `decisions/002-retire-seq.md`).
- **2026-11-17 (plan check)**: Every file, line range and symbol cited in this folder was checked mechanically against `1c29645396` (77 references, 0 problems). `evidence/export-events.py` plus `evidence/replay-row-order.ts` reproduce the § 5 measurement from the debug session (211 events, 3 commands landing above a plan update).

- **2026-11-17 (review revision)**: Replaced the post-commit publisher with shared-transaction plan/transcript persistence. Specified rollback/restart and reversed-notification tests, transaction-bound run identity, and precise causal ordering. Expanded PU01 to all four callers and session persistence; retained PU02 as wire cleanup. Updated verification to CI-first and made new live-model acceptance explicitly conditional on authorization. Historical overlap and replay counts were not reverified. Planning documents only; no product changes or test gates run.

- **2026-11-17 (implementation)**: Built the shared transaction through the existing session transcript store, retaining session-lock-before-database ordering and durable message outbox publication. All four mutation paths use transactional snapshots and propagate failures. Fan-out command replay retries idempotent seeding. Completion derives from durable terminal edges. Removed the timer, observer-owned rows, completion arm, sequence producers/fields, and obsolete run-accessor interface. PU01/PU02 share one PR to avoid temporary counter machinery.

## Verification

- Remote generation: `db:sqlc`, `openapi:bundle`, and `codegen:den-types`; generated outputs copied back and committed.
- Focused Linux run: session store, instructions, workflow, and phases passed. The progress run exposed invalid child-run fixtures and a real nil-item-ID serialization defect; both were corrected. The corrected progress suite passed 118 tests/subtests.
- Retained scoped logs and receipts: `evidence/verification/initial-focused/` and `evidence/verification/progress-pass/`.
- Added transaction rollback (including commit/outbox failures), restart delivery, delayed notifications, concurrent writers, cancellation, run identity, historical JSON decoding, and caller failure tests. Final CI qualifies the final head, including tests added after the scoped pass.
- Final focused Linux run at `be58f909bdd`: 512 tests/subtests passed, 16 skipped, across progress, app, instructions, and workflow. Thirteen integration-tagged files were excluded by `test:short`. The receipt and logs are retained in `evidence/verification/final-focused/`; captured source tree `0879c00f287dc2b9b6b30d7872fefc8c13b903d1`.
- No live-model or installed-app qualification claimed. The captured-session replay remains separate evidence.

- CI exposed shared-fixture compile errors (duplicate import and read-transaction interface mismatch). Fixed these and replaced obsolete observer-based outbox contract entries with the actual callback transaction boundary. Host contract rerun: 185 passed, retained in `evidence/verification/host-contract/`.

- API fixture verification at `58e6f84e24e`: all 30 API packages compiled. Twenty-eight passed initially; the two Git-dependent packages needed the pinned engine installed on the fresh remote worktree. After `gitengine:fetch`, both passed (518 tests/subtests, 1 skipped). Receipts are retained in `evidence/verification/api-fixture/` and `evidence/verification/api-git-pass/`.

## Remaining work

Work paused at the user’s request to spin down. All implementation commits are pushed at `58e6f84e24e1f106caec9571331bcf44a828c458`.

Resume with CI run [44120296624](https://github.com/paintedwolf-ai/paintedwolf-code/actions/runs/44120296624). Its `fast / ready-tests (3/4)` job reported that `lycaon/internal/invocation/program_progress_test.go:69` passes `f.database` (declared `db.Handle`) to `sessionstore.NewSQL`, which requires `db.ReadHandle`. Fix that fixture capability mismatch and inspect the completed run for any additional failures. The earlier shared API fixture mismatch and outbox contract issues are already fixed and verified.

The PR was returned to draft because this repair is unfinished. After repairing and pushing, mark ready and follow all final-head checks through success. No merge, deployment, live-model run, or installed-app qualification has been performed. The local GitHub watcher was stopped; GitHub CI may finish independently. No remote managed test remains running.

A complete planning-folder and retained-verification snapshot is stored on GitHub branch `handoff/plan-rows-at-the-write` in the same repository. The snapshot excludes implementation worktrees; their source is on `fix/plan-transcript-transactions`.
