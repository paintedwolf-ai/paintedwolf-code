# 001 — Store plan rows with the write

## Decision

Commit a visible plan transition and its transcript row in the same SQLite transaction. Remove the coalescer. Post-commit observers notify consumers; they do not create transcript rows or decide which committed transitions survive.

This supersedes the original synchronous `Publisher.Record` proposal. A lock acquired after commit cannot order commits, and a void publication callback cannot distinguish a stored row from a failed append. Synchronous notification alone also leaves a crash window between the plan commit and row insertion.

## Transaction boundary

The progress operation owns the plan transition. Transcript persistence retains responsibility for message validation, ordinal allocation, insertion, and message outbox records. Compose these responsibilities under one existing database writer transaction, with narrow transaction-aware ports; do not introduce a second subsystem owner, nested writer transaction, or a progress import of the app composition layer.

For each of the four write kinds:

1. Enter the session mutation boundary using the same lock order as transcript mutations: session mutation lock before database writer transaction. Audit all four callers for enclosing locks/transactions before implementing the composition.
2. Read the current progress snapshot and required workflow-run facts in that transaction. Validate the requested mutation, including existing author revision and item-identity checks.
3. Produce the exact before/after transition and persist the plan mutation. Bootstrap, run adoption, and fan-out seeding must stop reconstructing transitions with separate reads around their write.
4. Classify the visible transition from those snapshots. Build any row using the transition's run identity and append through the existing transaction-aware transcript path in the same transaction.
5. Commit the plan, row, ordinal, and normal message outbox records together. Only after commit, wake the outbox and publish progress invalidation through the existing notification path.

`session/store/sql_transcript_write.go` already supplies `AppendMessagesTx` and `AppendMessagesWith`. Reuse the transcript implementation rather than duplicating ordinal allocation or outbox insertion. The current `AppendMessagesWith` accepts prebuilt messages, whereas this operation derives its messages from transactional reads: choose a narrow composition entry point that supports that dependency while retaining the session mutation lock. Merely calling `AppendMessagesTx` from a new database transaction is insufficient without auditing the session mutation boundary and post-commit wakeup.

The implementation PR must identify the concrete composition entry point, dependency direction, lock order, and post-commit notification path. This is an implementation design obligation, not permission to leave row publication outside the transaction.

## Ordering and concurrent writes

The database transaction determines transition order. Each committed transition is classified once against its own immediate predecessor. Do not collapse a chain into a latest snapshot, suppress old notifications as a substitute for persistence, or keep a process-local last-published snapshot.

For A→B followed by B→C, both transitions are considered even if their post-commit notifications arrive in reverse order. This also preserves completion followed by reopening. On first use after restart, the durable current snapshot supplies the predecessor; an empty publisher cache has no role.

The row follows messages committed before its transaction and precedes messages committed after it. For `update_progress`, it is stored after the calling assistant row and before the originating call returns or its next model request starts. Other legitimate messages may intervene: adjacency to the assistant row is not required. Concurrent operations are ordered by transaction serialization, not by goroutine entry or return time.

## Failure, cancellation, and restart

- A failure to persist a required transcript row fails the whole mutation. Neither the plan change nor its row becomes durable. Propagate the operation error through the caller's existing error path; host bootstrap/adoption/seeding callers must not silently report success.
- Cancellation before a successful commit rolls back both. Cancellation after commit cannot undo either. Do not use `WithoutCancel` to try to repair a partially committed operation.
- A crash before commit leaves neither effect; a crash after commit leaves both. Existing message outbox recovery handles notification delivery. No new delayed transcript-repair queue is needed.
- Retrying the same author request after a committed change follows the existing revision checks. A repeated no-op must not create another row. Completion eligibility derives from the committed before/after transition, not an in-memory reservation that resets on restart.
- No pending publication state needs flushing at shutdown. Drain admitted mutations using their existing lifecycle; unregistering an observer must not discard a row for an already committed mutation.

## Workflow-run identity

Stamp the row with the run bound to the resulting plan transition, validated within the same transaction. A later active-run lookup must not relabel it. Run adoption/reset binds the new run as part of that mutation; the prior snapshot still belongs to its prior run.

For an unbound document, resolve and validate the applicable run within the mutation transaction before a visible write. Preserve existing seeded content when adopting it. If a visible mutation cannot obtain a valid run, fail without mutation rather than commit a change and silently omit its row. A nonvisible unbound setup operation may remain rowless; make that classification explicit and test it.

## Row classification and `seq`

Preserve the existing visible diff policy: no visible change yields no update; a transition from nonterminal to terminal yields one completion instead of an update; other visible changes use the existing update builder, including changes between terminal snapshots. Run reset/reopening prepares the next completion edge. Do not treat a skipped notification as a skipped transition.

Replace process-local completion eligibility with transition-based eligibility in PU01. Until PU02 removes the required wire fields, retain only the compatibility sequence production needed by those fields; it must not control persistence, ordering, deduplication, or completion eligibility. Gaps or resets in these unused counters carry no authority.

## Scope and compatibility

PU01 now includes the four mutation paths, progress persistence, transaction composition with session transcript persistence, and observation bindings. It remains backend-only. The intended design changes transaction composition using existing tables, not database shape. If implementation demonstrates a new durable shape is necessary, classify it under `docs/compatibility.md` and revise this plan before coding that shape; do not retain a blanket no-migration claim over a schema change.

No new model-visible fields, refusal codes, or prompt copy are planned. Storage failure becomes an honest failed operation instead of a successful plan write with a missing transcript row.

## Rejected

- **Synchronous post-commit publisher with a per-session lock:** cannot guarantee commit order, preserve the first overtaken transition, or close the crash gap.
- **Keep a timer or shorten its window:** a later model step can still acquire an earlier ordinal.
- **Flush at tool-batch completion:** leaves a durability gap and does not cover host-originated mutations as one transaction.
- **Re-anchor in Den:** makes the client reconstruct causal ordering instead of rendering host ordinals.
- **Durable deferred publication:** would require ordering/recovery state and could still insert the row after later transcript messages. Atomic insertion uses the existing transaction domain.
