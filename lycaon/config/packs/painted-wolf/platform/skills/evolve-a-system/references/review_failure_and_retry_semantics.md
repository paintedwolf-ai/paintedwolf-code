# Review failure and retry semantics

**Entry check:** the operation can fail after work may already have happened. A timeout describes what the caller observed; it never proves the callee did nothing.

## Workflow

1. **Draw the operation boundary.** List each durable write and external effect, the component that settles it, the request identity that names it, and the observation that confirms completion. Split multi-step work into explicit transitions rather than treating the handler as one retryable call.
2. **Classify every failure point.** Walk these seven and mark each known-failure, known-success, or ambiguous:

   | # | Failure point | Caller sees | Callee state |
   |---|---|---|---|
   | 1 | before send | error | nothing happened |
   | 2 | in transport | timeout | nothing happened |
   | 3 | after receipt, before commit | timeout | **ambiguous** |
   | 4 | after commit, before response | timeout | **committed** |
   | 5 | response lost in return | timeout | **committed** |
   | 6 | local commit before ack | — | committed, will redeliver |
   | 7 | during recovery | — | **ambiguous** |

   Rows 3–5 are indistinguishable to the caller. Any design that treats a timeout as "did not happen" is wrong at those three rows.
3. **State the delivery semantics honestly** — at-most-once attempts, at-least-once with deduplication, or another bounded guarantee. Do not claim exactly-once effects unless a single atomic authority covers both the effect and its durable identity.
4. **Design idempotency around logical intent.** One stable key per logical operation, reused across retries, bound to canonical intent or a payload hash. Store the resulting status and output, and reject key reuse carrying different intent. Define retention, and define what a second request does while the first is still in flight.
5. **Make retry eligibility structured.** Retry only explicitly classified transient failures, and only when the operation is safe to repeat or can confirm the original did not apply. Use a total deadline and attempt budget, exponential backoff with jitter, and server pushback such as `Retry-After`. Nested layers must share one budget rather than multiplying: three layers retrying three times each is 27 attempts.
6. **Handle partial and terminal outcomes.** Resume from durable step state, compensate only where compensation semantics are defined, and route irreversible ambiguity or an exhausted budget to a visible terminal state or human review. Cancellation stops new work; it does not erase committed effects.
7. **Fault-test every boundary.** Inject failure before and after each commit and ack, duplicate and reorder delivery, deliver a late response, run concurrent same-key requests, restart between steps, and exhaust the retry budget. Assert final business effects — not response codes — and emit attempt and outcome metrics without sensitive payloads.

## Stopping rule

The review is complete when all seven failure points in step 2 are classified and every one marked ambiguous has a named resolution: an idempotency key, a confirmation read, or a route to human review. An ambiguous row with no resolution is the finding — report it rather than closing the review.

## Boundaries

- Do not retry an irreversible or non-idempotent operation after an ambiguous outcome because the error resembles a timeout.
- HTTP method names, queue delivery modes, and SDK defaults are inputs to the review, not proof that application side effects are idempotent.
- Do not use unbounded retries, synchronized fixed backoff, or independent retry loops at every layer.
- Do not silently drop exhausted work; persist a diagnosable terminal outcome with enough identity to reconcile it.

## Report

The operation boundary, the seven-point classification, the declared delivery semantics, the idempotency key and its binding, the retry budget and deadline, the fault-injection results, and every ambiguous outcome without a resolution.
