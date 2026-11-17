# Evidence

Original baseline observations below are from `origin/main` `1c29645396` (2026-11-17). Paths are relative to the repository root. Measurements name their source. Section 7 qualifies the original design conclusions; baseline behavior is not the revised target behavior.

## 1. How a plan update reaches the transcript today

- Four places announce a committed plan write, all through `progress.NotifyWriteObservers`:
  - `lycaon/internal/progress/author_update.go:194`: the `update_progress` tool, inside the tool call.
  - `lycaon/internal/session/instructions/manager_progress.go:26`: `BootstrapProgress`, on a visible user intent at turn start.
  - `lycaon/internal/app/workerruntime/hooks.go:33`: adopting the active run on workflow phase entry.
  - `lycaon/internal/workflow/fanout_plan.go:134`: seeding the checklist from a fan-out plan.
- Observers run synchronously, in the writer's goroutine, before the announce call returns (`lycaon/internal/progress/notify.go:54-60`).
- The one observer is bound in `lycaon/internal/app/observations/bindings.go:41-48`. For each write it publishes a progress revision, calls `coalescer.Record` (`:46`), then calls `emitProgressCompletionFromContent` (`:47`).
- `progress.Coalescer` (`lycaon/internal/progress/coalesce.go`) opens a per-session window on the first write and restarts a timer on every write: `time.AfterFunc(c.window, …)` at `:116`, with `DefaultCoalesceWindow = time.Second` at `:11`. When the timer fires, `flush` hands the window's first baseline and latest snapshot to the emitter.
- The emitter (`lycaon/internal/app/observations/progress_changes.go:26-47`) builds the row with `buildProgressUpdate` (`:52`) and appends it with `context.Background()` (`:43`). The append assigns the ordinal.
- `appendProgressTranscript` stamps the workflow run that is active at append time (`lycaon/internal/app/observations/progress_transcript.go:47`), which for a plan update is the flush, not the write.
- `progress_complete` takes no timer. It is appended inside the observer call (`bindings.go:47`, `progress_completion.go:15-56`), so it lands at the write.

## 2. What the timer was meant to do, and what it does

- Stated purpose: "a burst of edits drops a single change row into chat" (`lycaon/internal/progress/coalesce.go:28-29`).
- For ordinary sequential author updates, revision checks prevent two changing writes based on the same existing snapshot. This is not a general ordering guarantee across host writes or concurrent operations. A write to a plan that has rows must carry the current revision; without it the write is refused with `PROGRESS_REVISION_REQUIRED` (`author_update.go:152`), and with a stale one with `PROGRESS_REVISION_CHANGED` (`:155`). The measured sample below supports the ordinary case; correctness must not rely on the model making at most one successful write per step.
- Measured, dev recovery store of 14 sessions (`~/.config/paintedwolf-dev/.restore-recovery-07b6e6db-…/store.db`): 647 assistant steps called `update_progress`; 0 called it more than once.
- Same store: 361 plan updates; in 356 the stored order is assistant row, `update_progress` result, plan update, next assistant row. One row per call.
- The coalescer also orders concurrent writers: it ignores a write whose document version is not newer than the last published one (`lycaon/internal/progress/coalesce.go:88`) and serializes emits per session (`:160-196`). This describes the old coalescer. The revised design preserves transitions transactionally instead of dropping overtaken notifications (`decisions/001-store-at-the-write.md`).

## 3. Fields that exist only because of the delay

- `FlushPayload.ChangedAt` (`lycaon/internal/progress/coalesce.go:13-23`) and the row's `CreatedAt: changedAt` (`progress_changes.go:41`): the write time, carried so the row is dated at the change. Den orders by ordinal, so it only affects the displayed time.
- `FlushPayload.Seq`, a per-session counter in process memory (`lycaon/internal/progress/coalesce.go:148`), written to the wire as `ProgressUpdateMeta.seq` (`progress_changes.go:65`). It restarts at 1 on every engine start.
- `ProgressCompleteMeta.seq` comes from a second in-memory counter (`lycaon/internal/progress/completion.go:55`, `progress_completion.go:41`).
- Both are required wire fields (`docs/openapi/components/schemas/progress.yaml:55-59`, `:196-199`).
- Nothing reads either. Go producers are the only Go references to these fields outside tests and generated code. In Den, no file under `lycaon-den/src` outside tests and generated types reads `progress_update.seq` or `progress_complete.seq`; #2370 removed the last reader (the per-message progress sequence in the sort key).
- Stored rows decode with `db.UnmarshalJSON` into the generated structs (`lycaon/internal/session/store/sql_message_codec.go:44-55`), which ignores unknown keys.

## 4. How Den places these rows

- A row sits at the ordinal of the message that first shows it; a plan update is its own message, in the `row` tier (`lycaon-den/src/chat/transcript/projection/transcript-item-order.ts`, `transcriptItemOrdKey`).
- A plan update closes the open activity group at its own place (`lycaon-den/src/chat/tool/activity-span-model.ts`, `projectActivitySpans`).
- A call's row draws once the host commits its assistant row (`lycaon-den/src/chat/transcript/projection/transcript-items.ts:60`); the host commits it after settling the note and before running any tool (`lycaon/internal/coordinator/promptloop/batch.go:222-228`).
- `update_progress` is in `COORDINATOR_INTERNAL_TOOLS` (`lycaon-den/src/chat/tool/tool-presentation.generated.ts:420`), so its call row is never drawn in chat. A plan update stored between the assistant row and the call's result therefore has no drawn row from its own call to sit above or below.
- Plan rows are transcript chrome and never enter model history (`lycaon/pkg/api/message_transcript.go:92-100`, `:113-115`).

## 5. The defect, observed

Source: `~/.config/paintedwolf-dev/debug/sessions/20261117T140032Z/sse-events.jsonl`, three chats, 211 message events, from the dev engine built at 14:00 UTC from `1c29645396`.

| Chat | Plan update ordinal | Stored after its call's result | Rows stored in between |
|---|---|---|---|
| `d9afd305` | 7 | 0.97 s | assistant row 6 |
| `d9afd305` | 27 | 0.98 s | assistant row 26 |
| `d9afd305` | 53 | 0.97 s | assistant row 52 |
| `99f1cda1` | 5 | 0.93 s | none |
| `99f1cda1` | 16 | 0.92 s | none |

Two more plan updates in `d9afd305` (ordinals 23 and 49) follow a user message with no call behind them: the turn-start bootstrap.

Replaying the events through the projection on `main` (`evidence/replay-row-order.ts`): in the three cases with an assistant row in between, the plan update was drawn, then that assistant row was committed and its command appeared above the plan update. Nothing else moved: all 4 notes were stored before their assistant row's calls showed, and no diff card or command changed place.

The same replay over 13 sessions from 14–16 November showed 3 such events in 2,110, which is why the timer was first left alone. Those sessions' next assistant rows were appended more than a second after the call's result.

## 6. Open PR overlap (swept 2026-11-17)

Only #2435 (draft) touches `lycaon/internal/progress/`: three test files and `progress_gated_tools.go`. None is a file this plan changes. No open PR touches `lycaon/internal/app/observations/`, `docs/openapi/components/schemas/progress.yaml` or `lycaon-den/src/chat/progress/`.

## 7. Review corrections and transaction support (2026-11-17)

Read-only review used the same local repository commit, `1c29645396`; no live overlap sweep or new runtime measurement was performed.

- `progress/author_update.go` invokes observers after `Store.Update` returns. `progress/snapshot_store.go` commits its `db.InTx` operation before that return. An observer lock therefore does not serialize the preceding commits.
- Bootstrap (`session/instructions/manager_progress.go`), run adoption (`app/workerruntime/hooks.go`), and fan-out seeding (`workflow/fanout_plan.go`) reconstruct events with separate snapshot reads around mutations. Those reads are not an atomic before/after pair.
- `app/observations/progress_changes.go` logs append errors; its callback returns no outcome. `progress_transcript.go` can return `written=false` with no active run and resolves run identity at append time. Synchronous invocation does not remove either failure mode.
- `progress.Snapshot` already carries `RunID` and document version. The revised transaction must establish these facts while performing the mutation rather than rediscovering the active run afterward.
- `session/store/sql_transcript_write.go` exposes `AppendMessagesTx` and `AppendMessagesWith`. The latter acquires the session mutation lock before starting the database transaction and wakes the outbox after commit. The revised composition must retain those boundaries while deriving messages from transactional snapshots.
- `buildProgressUpdate` suppresses a nonterminal-to-terminal update, but can emit visible terminal-to-terminal edits. The earlier blanket statement that every complete latest snapshot produces only a completion was too broad.
- A first notification B→C received before A→B makes the rejected publisher start at B and discard A→B. A previously known baseline does not solve lost intermediate completion/reopening edges. Latest-state equivalence is weaker than preserving visible transitions.
- Captured replay shows UI ordering for its input events. It does not prove append-error recovery, crash safety, transaction ordering, or run identity. Those require the deterministic mutation tests in `systemic-invariants.md`.
