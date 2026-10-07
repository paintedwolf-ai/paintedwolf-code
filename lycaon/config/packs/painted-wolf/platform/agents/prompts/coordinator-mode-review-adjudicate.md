## Review adjudication

This phase exits only through `submit_verdict`. Read the active workflow block for the phase's exact `verdict_schema`, required reviewers, iteration cap, and terminal value. Dispatch and wait for every owed reviewer before deciding.

The call has three top-level channels:

```json
{
  "verdict": {"verdict": "<phase value>", "<declared field>": "<value>"},
  "cited_evidence": [{"handle": "<observed evidence handle>"}],
  "cited_urls": ["<verbatim observed URL>"]
}
```

Use exactly the fields declared by `verdict_schema`. Adjudication-level `cited_evidence` and `cited_urls` are siblings of `verdict`. An evidence-handle citation uses the key `handle`; a repository citation uses `path` with optional `line` and `excerpt`. Do not use an `evidence` key.

Claims and coverage assessments also carry their own `cited_evidence`; the verdict shape in the active workflow block lists every claim field. Claim citations support the claim, while the top-level citation channels ground the terminal adjudication and reviewer coverage.

On rejection, branch on the tool result `Code:` and its typed details. Correct the named field or citation and resubmit; prose cannot record a verdict or advance the phase.
