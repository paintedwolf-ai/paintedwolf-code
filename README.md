# Plan rows at the write

Commit a plan transition and its transcript row together, removing the one-second delay that can place a plan update after the next assistant row.

Start at `STATUS.md`. Source evidence is recorded in `evidence.md` against `1c29645396`. The 2026-11-17 review supersedes the original synchronous post-commit publisher design with shared-transaction persistence.

## Crux

1. Den positions rows by host-assigned ordinals. A plan update delayed by `progress.Coalescer` can acquire an ordinal after the next assistant row; removing the timer addresses the observed race.
2. The captured dev session showed three affected tool-originated updates. The historical sample found no repeated `update_progress` calls in one assistant step; that observation does not prove all concurrent or host-originated transitions can be merged safely.
3. Synchronous post-commit observers alone cannot guarantee commit order, recover a failed row append, or close the crash gap between the plan and transcript writes. The existing synchronous completion path is precedent for timing, not proof of atomicity.
4. Progress and transcript state already share one database transaction domain. Persist the exact transition's row there, with its run identity, and leave observers responsible only for post-commit notification.

## Decision and delivery

| Id | Ships | Layer |
|---|---|---|
| `PU01` | Atomic plan/transcript persistence across all four mutation paths; coalescer and observer-owned row creation removed | Backend progress, session persistence, callers, and observations |
| `PU02` | Unused `seq` fields and legacy production removed; Den stability tests cover historical and new host orders | OpenAPI + backend + generated Den types/tests |

PU01 and PU02 are implemented together in [PR #2467](https://github.com/paintedwolf-ai/paintedwolf-code/pull/2467), based on `main`. The transactional row builder eliminates the sequence producers directly, so a separate intermediate implementation would add disposable machinery. See `STATUS.md` for the verified head and remaining qualification.

### What changes for a reader

A visible plan transition has its transcript row when the mutation commits. For `update_progress`, that row follows its calling assistant message and is stored before the call returns and the next model step begins. Legitimate concurrent messages may intervene; the guarantee is causal ordering, not adjacent ordinals. Den continues to render host positions.

## Scope boundaries

- Preserve existing visible diff rules, including terminal-to-terminal edits; completion is emitted for the nonterminal-to-terminal edge instead of an update. No visible change yields no row.
- Keep `update_progress` arguments, successful result shape, existing validation/revision refusal codes, and model-visible copy unchanged. A required row insertion failure rolls back the mutation and surfaces through the existing operation error path.
- PU01 includes transactional composition and all four write paths, not just the observer. Use existing tables; a newly discovered schema requirement needs compatibility classification and a plan revision.
- PU02 removes only unused JSON fields/types and their producers. Older stored rows retain ignored `seq` keys without rewriting history.
- No Den projection changes are planned. The dev replay is separate qualification evidence; CI is the default implementation verification route.

## Files

- `STATUS.md`: state, PRs, resume log.
- `milestone-plan.md`: landing order, overlap, CI policy, and optional authorized dev acceptance.
- `evidence.md`: baseline code facts, measurements, and review corrections.
- `systemic-invariants.md`: ordering, atomicity, completion, concurrency, run identity, and Den contracts.
- `oar-and-prompts-contract.md`: model-visible scope and persistence-error distinction.
- `decisions/001-store-at-the-write.md`, `decisions/002-retire-seq.md`: design and compatibility.
- `issues/PU01-plan-rows-at-the-write.md`, `issues/PU02-retire-progress-seq.md`: implementation work.
- `evidence/export-events.py`, `evidence/replay-row-order.ts`: captured-session UI ordering probes.
