# Compare implementation approaches

**Entry check:** you can name at least two approaches that differ in structure, not just in detail, and you cannot pick between them from what you already know. If one option is clearly right, build it — a comparison you already know the answer to is wasted work.

## Workflow

1. Write the rubric before dispatching anything, and do not change it after results arrive. Score every criterion the same way for every option:

   | Criterion | Weight | How it is measured |
   |---|---|---|
   | Correctness | must-pass | the frozen acceptance cases all pass |
   | Complexity | 3 | files touched, new concepts introduced |
   | Compatibility | must-pass | no durable contract broken |
   | Operability | 2 | failure modes observable, recovery documented |
   | Verification cost | 2 | added suite runtime, new infrastructure needed |

   Mark each criterion must-pass or weighted. A must-pass failure eliminates an option no matter how it scores elsewhere.
2. Name each alternative by its distinguishing idea — "shared write-through cache" and "per-request materialization", not "option A" and "option B". Two workers given the same vague prompt return the same design twice.
3. Give every alternative the same scope, inputs, done criteria, and recommended procedural skill. State which shortcuts are acceptable in a prototype and which invariants are mandatory regardless.
4. Pick each worker's `agent_type` from the spawn roster in your prompt, use write mode, and suggest its prototype path as the focus.
5. Keep alternatives independent. Do not relay one worker's findings to another; convergent options are the one outcome that makes the comparison worthless.
6. Wait for every alternative, preview each complete result, and fill the rubric from evidence. Score what the rubric asked for — polish it did not request earns nothing.
7. Apply the elimination rule: any must-pass failure is out. If every option is eliminated, reject them all and revise the decision rather than merging the least-bad compromise.
8. Reject the losing overlays before integrating the winner, then run integrated verification and delete prototype-only artifacts.

## Stopping rule

Three alternatives is the normal ceiling; beyond that the rubric stops discriminating and the integration cost dominates. Run one round. If the winner is unclear after scoring, the rubric was underspecified — sharpen the criterion that tied and re-score the existing results rather than dispatching more options.

## Report

The rubric as frozen, each option's score with the evidence behind it, the winner, the decisive criterion, what the rejected options were better at, and any uncertainty the comparison did not settle.

Use a fixed workflow instead when the number of alternatives, judge, gates, and transition order must always be identical.
