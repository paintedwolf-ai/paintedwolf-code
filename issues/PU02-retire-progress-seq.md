# PU02 — Retire `seq` from plan rows

- **Layer**: OpenAPI + `lycaon` + `lycaon-den` (wire sync; no database)
- **Depends on**: `PU01` in the same PR (#2467)
- **Invariants**: `I6`
- **Decisions**: `002`
- **Prompt-visible entries**: none

## Problem

`ProgressUpdateMeta.seq` and `ProgressCompleteMeta.seq` are required wire fields fed by in-memory counters. Nothing reads them, and they restart with the engine. See `evidence.md` § 3.

## Change

1. **`docs/openapi/components/schemas/progress.yaml`**: remove `seq` and its `required` entry from `ProgressCompleteMeta` (`:55-59`) and `ProgressUpdateMeta` (`:196-199`). Run `./task openapi:bundle`, then `./task codegen:den-types`. Never edit the generated files.
2. Remove the legacy plan-update sequence field/counter alongside PU01 at the transaction-composition location. There is no post-commit `Publisher` in the revised design.
3. Remove remaining completion sequence production alongside PU01. Completion eligibility must already derive from transactional before/after state; no in-memory reservation remains authoritative.
4. Update the row builders at their post-PU01 locations to stop setting `Seq`; remove sequence parameters. Implemented in `internal/progress/transcript.go`.
5. **Den tests**: drop `seq` from the progress literals in the seven test files that carry it (`progress-model.test.ts`, `messages-equal.test.ts`, `transcript-row-stability.test.ts`, `transcript-progress.test.ts`, `transcript-ordering.test.ts`, `SessionTranscript.tools.test.tsx`, `WorklogPanel.test.tsx`). No Den source file reads the field.
6. **`transcript-row-stability.test.ts`**: the session generator stores each plan update on either side of its call's result, chosen by the seeded generator, so the invariant holds for both host orders (`I6`).

## Tests

- Go tests that asserted `Seq` values drop those assertions; what they bound (one row per change, one completion per edge) is asserted by row counts.
- CI checks generated types, Den consumers, and affected tests. Follow `milestone-plan.md`; do not require a full local Den run before CI.
- Add one case to `lycaon/internal/session/store/sql_progress_update_test.go` that reads back a stored plan update and a stored completion whose JSON still carries `seq`, and asserts the decoded steps and changes.

## Done when

Neither meta type has `seq` in the OpenAPI source, the generated Go or the generated Den types. A transcript page containing rows written before this change loads and renders. Budgets pass.
