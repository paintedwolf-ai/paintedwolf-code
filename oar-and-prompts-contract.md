# OAR and prompt surface ledger

No model-visible schema, successful result shape, refusal-code vocabulary, prompt copy, or OAR policy change is planned. Review any such diff explicitly rather than admitting it as incidental cleanup.

| Id | Surface | Before | After | PR |
|---|---|---|---|---|
| — | No vocabulary/copy changes planned | — | — | — |

## Surfaces and behavioral boundary

| Surface | Contract |
|---|---|
| `update_progress` arguments and successful result | Preserve their shape and semantics. `author_update.go` participates in the new transaction boundary; it is no longer out of scope. |
| Revision and validation refusals | Preserve `PROGRESS_REVISION_REQUIRED`, `PROGRESS_REVISION_CHANGED`, and existing validation codes; perform checks against the transactional predecessor. |
| Persistence failures | A required transcript insertion failure fails and rolls back the plan operation. This is an intentional error-behavior change from a committed plan plus logged append failure, using existing error handling rather than a new model-facing vocabulary. |
| Transcript progress rows | Continue to exclude them from prompt history (`pkg/api/message_transcript.go`). |
| Shipped prompts and OAR | No policy/copy changes planned. Recheck consumers when moving mutation and notification responsibilities; notification timing does not grant new authority. |
| Den copy | No new user-facing strings or projection policy. |

Do not weaken atomicity to preserve a false success on storage failure. If the implementation requires a new public result or rejection meaning, revise this ledger and apply the agent-public compatibility policy before shipping it.
