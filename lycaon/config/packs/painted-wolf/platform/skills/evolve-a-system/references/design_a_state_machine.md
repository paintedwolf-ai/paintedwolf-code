# Design a state machine

**Entry check:** behavior depends on what happened earlier, not only on the current input. If every request can be answered from its own arguments, there is no machine here — write the function.

## Workflow

1. **Write the state vector.** List the smallest set of authoritative variables that decide behavior, and name the states they can be in. Separate durable state from derived presentation. Independent booleans are the failure mode: three flags are eight states, most of them unnamed and unreachable — collapse them into one enum.
2. **List the events.** Commands, observations, timeouts, cancellation, duplicate delivery, late delivery, dependency results, restart or recovery, and administrative actions. Events name what occurred; states name what is true afterward.
3. **Write the transition table.** This is the artifact — one row per `(state, event)` pair you accept, plus explicit rows for the ones you reject:

   | State | Event | Guard | Durable effect | Emitted effect | Next state | Result |
   |---|---|---|---|---|---|---|
   | `pending` | `approve` | approver ≠ author | write decision row | none | `approved` | ok |
   | `pending` | `approve` | approver = author | none | none | `pending` | rejected: self-approval |
   | `approved` | `approve` | same request id | none | none | `approved` | ok (duplicate) |
   | `approved` | `approve` | different request id | none | none | `approved` | rejected: already decided |
   | `approved` | `cancel` | — | none | none | `approved` | rejected: terminal |

   Rows three and four are the ones weak designs omit: a repeated event is either the same intent arriving twice (succeed) or a conflicting one (reject), and collapsing them into a single no-op hides a real bug.
4. **Mark the commit point.** For each row with a durable effect, state what is true if the process stops immediately before it and immediately after it. An irreversible external effect must not sit in a gap where no record of it exists.
5. **State the properties.** Write the impossible states, the uniqueness and ordering invariants, the valid terminal states, and which non-terminal states must eventually advance. Safety ("two approvals never both commit") and liveness ("a pending request eventually resolves") are different claims; deadlines and human waits support only the second.
6. **Walk the adversarial sequences.** Run each of these through the table and record the outcome: duplicate event, reordered pair, two concurrent events on one entity, cancellation mid-transition, timeout racing success, and restart between the durable and emitted effect. Every one must land on a defined row.
7. **Implement one transition authority.** Centralize validation and mutation in a single function that consumes the table, persist enough identity to make replay idempotent, and generate every projection from the authoritative state. Test the table row by row plus the six sequences from step 6.

## Stopping rule

The model is done when every `(state, event)` pair is either a row in the table or explicitly out of the alphabet, and all six adversarial sequences land on a defined row. Do not add states to make a sequence work until you have checked whether an existing guard already covers it.

## Boundaries

- Do not use prose status labels without defining their transition semantics and terminality.
- Do not make every unknown transition a no-op; duplicate success, stale, invalid, and unsupported are four different results.
- Do not claim "exactly once" from a state enum alone; external effects need durable identity and atomic coordination.
- Keep implementation detail out until states and events are stable enough to compare competing designs.

## Report

The state vector, the transition table, the commit points, the properties, the six sequence outcomes, and any environmental assumption the model does not cover.
