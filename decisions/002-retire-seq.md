# 002 — Retire `seq` from plan rows

## Decision

`seq` is removed from `ProgressUpdateMeta` and `ProgressCompleteMeta` on the wire, with the legacy sequence production retained by PU01. Completion eligibility already belongs to the transactional transition in PU01; PU02 must not retain or reintroduce an in-memory completion gate.

## Why

- Nothing reads it (`evidence.md` § 3). Den ordered by it until #2370; rows are placed by ordinal now.
- It is not a durable fact: both counters live in process memory and restart at 1 with the engine, so two rows of one session can carry the same value.
- Keeping a required wire field that no consumer reads and no producer can make meaningful invites a future reader to trust it.

## Compatibility

- **Wire (`bundle`)**: Den and the host ship together. Change `docs/openapi/components/schemas/progress.yaml`, run `./task openapi:bundle` and `./task codegen:den-types`, and update Go and Den in the same PR. The breaking diff is retained for release review; it needs no acknowledgement.
- **Database (`durable-db`)**: no schema change and no migration. Rows written by earlier builds keep `"seq": n` inside `progress_update_json` and `progress_complete_json`. `db.UnmarshalJSON` into the regenerated structs ignores the key (`sql_message_codec.go:44-55`). No dual read: the field has no reader on either side.
- **Agent-public vocabulary**: not involved. The field never reaches a model.

## Why a separate PR

`PU01` changes behaviour and is host only. This is a wire change that regenerates Den types and touches seven Den test files. Keeping them apart keeps each diff reviewable and lets `PU01` land without waiting on wire review.

## Rejected

- **Derive `seq` from the row's ordinal.** The ordinal is already on every message.
- **Leave the field, write zero.** A required field that is always zero is worse than no field.
