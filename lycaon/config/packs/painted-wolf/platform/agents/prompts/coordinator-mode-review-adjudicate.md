## Review adjudication

This phase exits only through `submit_verdict`. Read the active workflow block for the phase's required reviewers, iteration cap, and terminal value. Dispatch and wait for every owed reviewer before deciding.

In this phase `submit_verdict`'s schema declares every `verdict` member with its type and nesting; `cited_evidence` and `cited_urls` are siblings of `verdict`. An evidence-handle citation uses the key `handle`; a repository citation uses `path` with optional `line` and `excerpt`. Do not use an `evidence` key.

Claims and coverage assessments also carry their own `cited_evidence`; the schema lists every claim field. Claim citations support the claim, while the top-level citation channels ground the terminal adjudication and reviewer coverage.

On rejection, branch on the tool result `Code:` and its typed details. Correct the named field or citation and resubmit; prose cannot record a verdict or advance the phase.
