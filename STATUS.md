# Status

**State**: implemented in [PR #2467](https://github.com/paintedwolf-ai/paintedwolf-code/pull/2467), ready at `bb305d34ea820ad39101299ab8a57f2744d37152`; final-head CI pending. No merge or deployment performed.

| Id | Branch | Head | PR | State |
|---|---|---|---|---|
| `PU01` + `PU02` | `fix/plan-transcript-transactions` | `bb305d34ea820ad39101299ab8a57f2744d37152` | #2467 | final CI pending |

## Worktrees

- Local: `worktrees/implementation`, based on `main` at `1c29645396`.
- Active Linux checkout: `cdbeckman@10.0.20.5:/home/cdbeckman/plan-transcript-transactions`. Retained verification evidence is in `evidence/verification/`.

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

## Resumed and finished (2026-11-17 to 2026-11-18)

- Merged `origin/main` `057f888ba8` into the branch; the merge was clean.
- Fixed the failures from CI run 44120296624: the invocation progress test's database handle, a re-enter hook fixture returning a value, and Den's `messages-equal.ts`, which still compared the retired `seq`. The plan's claim that nothing in Den read `seq` was wrong.
- Removed further `seq` literals from two Den end-to-end specs and the ordering fixture, and cleared one lint finding.
- A plan write needs a workflow run, because the row is stamped with it in the transaction. The wiring harness's `SeedProgress` now binds the session's workflow with `EnsureSessionWorkflow` first, which fixed 37 wiring, conformance and security tests. Three wiring tests were changed to use the session's own run.
- Verified on the two Linux test hosts added on 2026-11-17. On the final head: `lint:full`, `test:integration:changes` and `test:digest` for `test/wiring` passed. On earlier heads, before fixture-only changes: scoped `test:digest` for the other touched Go packages and `test/security`, and the Den typecheck, full suite, lint, knip and changed coverage.
- A fresh checkout on a test host needs `./task gitengine:fetch` before Git-dependent packages can pass, and `./task setup-dev -- --frontend` before Den checks.

## Reassessment and CI repair (2026-11-18)

- Current main `0efb0f49c8` still uses the one-second coalescer and separate plan/transcript commits. Inspected all 166 open PRs and relevant diffs. #2472 fixes delivery of already committed outbox records, not atomic plan publication. #2567 removes memory progress fixtures; #2538 restructures session repositories. Both need composition with this PR, neither supersedes its behavior.
- Preserved the intervening work through `973d626297`. CI run 44271458330 found an unreachable extracted `MemoryStore.update` helper and insufficient changed-statement coverage for decision-resolution errors. Kept the memory mutation cohesive in `Update`, preserving unlock-before-notification and cancellation behavior. Added durable rollback/retry tests for each worker decision persistence boundary and missing/foreign decisions.
- Linux progress and worker suites at `2aec4fbdb83` passed 717 tests/subtests, with 3 skipped and one stress-tagged file excluded. Receipt and logs: `evidence/verification/resume-progress-worker/`.
- Merged main `0efb0f49c8` cleanly as `69e63f6bb2`. CI run [44361006053](https://github.com/paintedwolf-ai/paintedwolf-code/actions/runs/44361006053) passed lint and confirmed worker changed coverage at 84%, but workflow changed coverage remained 50%. Added registered fanout failure/replay recovery and child phase-entry error propagation tests in `bb305d34ea8`. Final-head CI: [44367934936](https://github.com/paintedwolf-ai/paintedwolf-code/actions/runs/44367934936); Linux `coverage:changes` passed: 2,723 tests/subtests (20 skipped), then 4,651 (3 skipped), no failures. Changed coverage: progress 95%, worker 84%, workflow 95%. Receipt matches final source tree `e361f97f734a3968eba9cd332a567c8c4faa7fc2`; retained in `evidence/verification/resume-coverage/`.
- The previous run also failed an unchanged Linux launch-guard test while starting its fixture (entry descriptor was an eventpoll descriptor rather than a sealed specification). The same focused Linux test passed at the current head (3 tests/subtests, no skips); no launch-guard source was changed. Receipt: `evidence/verification/resume-launchguard/`.

## Remaining work

Finish final-head CI and repair any remaining relevant failures. Preserve transactional mutation locking and ordinals when composing with #2538, and keep the atomic SQL implementation when #2567 removes memory fixtures. No unlanded PR is needed to land this implementation on current main.

The acceptance replay in `milestone-plan.md` remains separate dev-build qualification. No live-model or installed-app qualification has been performed.
