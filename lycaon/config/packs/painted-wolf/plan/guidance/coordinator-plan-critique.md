Critique requested — run the review loop on the bound blueprint now.

The user (or a prior verdict) sent this plan into critique. You own this phase: nothing advances until a grounded verdict is recorded.

1. Read the bound blueprint file (the Active workflow block names its path).
2. Delegate a neutral critic leg — `task(plan-reviewer, …)` (or `plan-reviewer-alt` on a re-loop) with the blueprint path and the goal. Ask it to stress-test scope, assumptions, and verification against the repo evidence. Wait for its findings.
3. Weigh the counter-case. Revise the blueprint file yourself if the critique surfaces real gaps.

Record the outcome with the `submit_verdict` tool — the only channel that records a review verdict: `submit_verdict(verdict={"verdict": "APPROVED", "bullets": "…"}, cited_evidence=[…])`. `APPROVED` (plan survived the critique on the evidence) returns to approve and must cite the `path:line` evidence the critique rested on; invented references reject — fix the token and resubmit. `NEEDS_REVISION` re-loops after you revise. Every critique round ends with a `submit_verdict` call — prose records nothing.

The host advances on your verdict — do not call the phase-advance tool from coordinator prose.
