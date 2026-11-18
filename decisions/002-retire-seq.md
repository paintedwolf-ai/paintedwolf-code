# 002 — Retire `seq` from plan rows

## Decision

`seq` is removed from `ProgressUpdateMeta` and `ProgressCompleteMeta` on the wire, together with the sequence producers and Den equality reads in the combined PU01/PU02 PR. Completion eligibility already belongs to the transactional transition in PU01; PU02 must not retain or reintroduce an in-memory completion gate.

## Why

- Rows are placed by ordinal after #2370. The remaining Den equality comparisons do not establish ordering and are removed with the metadata (`evidence.md` § 3).
- It is not a durable fact: both counters live in process memory and restart at 1 with the engine, so two rows of one session can carry the same value.
- Required process-local counters duplicate the durable transcript sequence and ordinal without providing a reliable identity.

## Compatibility

- **Wire (`bundle`)**: Den and the host ship together. Change `docs/openapi/components/schemas/progress.yaml`, run `./task openapi:bundle` and `./task codegen:den-types`, and update Go and Den in the same PR. The breaking diff is retained for release review; it needs no acknowledgement.
- **Database (`durable-db`)**: no schema change and no migration. Rows written by earlier builds keep `"seq": n` inside `progress_update_json` and `progress_complete_json`. `db.UnmarshalJSON` into the regenerated structs ignores the key (`sql_message_codec.go:44-55`). No dual read: updated consumers ignore the retired metadata.
- **Agent-public vocabulary**: not involved. The field never reaches a model.

## Delivery

PU01 and PU02 ship together in #2467. The transactional row builder replaces the counter-producing machinery directly; regenerating the wire and changing all consumers in that PR avoids introducing temporary counters.

## Rejected

- **Derive `seq` from the row's ordinal.** The ordinal is already on every message.
- **Leave the field, write zero.** A required field that is always zero is worse than no field.
