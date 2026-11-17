# Systemic invariants

Use deterministic barriers, fixtures, injected failures, and transaction/reopen boundaries. No sleeps or wall-clock thresholds. Tests must drive the actual mutation entry points, not merely fabricate observer events.

| Id | Invariant | Bound by |
|---|---|---|
| `I1` | A visible plan transition and its row share a transaction. The row follows earlier committed transcript writes and precedes later ones. For a tool-originated change it precedes the originating call's return and next model step; assistant-row adjacency is not required. | Real SQL tests interleaving all four write kinds with ordinary appends; compare committed transaction order, including concurrent appends before the operation returns (`PU01`) |
| `I2` | A committed visible transition has its required row; a failed transaction has neither effect. Restart cannot lose or duplicate the row. | Inject failures before plan persistence, during transcript insertion, and before commit; reopen after rollback and after commit before notification; cover cancellation and outbox recovery (`PU01`) |
| `I3` | No visible change creates no row; a nonterminal→terminal transition creates one completion instead of an update; other visible diffs use the existing update policy. Reopening allows a subsequent completion. | Table over `DiffSteps` kinds, no-ops, initial plans, terminal→terminal edits, completion, reopening, run reset, retry, and restart (`PU01`) |
| `I4` | Concurrent committed transitions are classified against their immediate durable predecessor. Delayed, duplicated, or reversed post-commit notifications cannot lose, merge, reorder, or duplicate transcript rows. | Real SQL mutation tests using barriers, including first transitions after startup and pending→complete→pending→complete; inspect committed snapshots and rows, not only the final snapshot (`PU01`) |
| `I5` | Each row carries the resulting plan transition's transactionally validated run identity. Later run switches cannot relabel it. | Run switch between commit and notification, adoption/reset across runs, initially unbound seeded content, and missing-run refusal without partial mutation (`PU01`) |
| `I6` | Den preserves existing drawn-row order with plan messages stored before or after their originating call result. | `transcript-row-stability.test.ts` generator covers historical and new host orders, mixed tool batches, and delayed event delivery (`PU02`) |

Rows may contain summary-only diffs under the existing detail cap. Do not claim that transcript metadata is a complete event log from which every intermediate plan can be reconstructed; use captured transactional snapshots to verify classification.
