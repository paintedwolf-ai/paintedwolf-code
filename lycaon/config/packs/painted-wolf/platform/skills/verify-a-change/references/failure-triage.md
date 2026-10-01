# Verification failure triage

Maintained sources of truth: the project's documented command wrapper and handoff policy. Host verification receipts and required gates remain authoritative.

## Diagnose before editing

1. Read the complete digest and identify the earliest root failure, not the largest downstream count.
2. Classify it as an assertion or behavior failure, compile or type failure, lint or contract failure, missing dependency or environment, timeout or flake, or an unrelated pre-existing failure.
3. Name the changed path or behavior that could plausibly connect the failure to the edit. If no connection is evident, inspect working-tree state and relevant source before changing anything.
4. Use bounded `read`, `grep`, or `summarize` to trace the failing symbol and its callers. Do not edit a test until the intended behavior and production path are understood.

## Iterate proportionally

- Use the project's supported scoped test target while fixing one known failure.
- Preserve the original assertion and safety coverage unless the requested behavior legitimately changes the contract.
- Do not add skips, retries, sleeps, ignores, broad exception handling, or reduced test scope merely to make the result green.
- Treat a formatter, generator, or dependency command as part of the fix only when the repository declares it.
- After a scoped check passes, run the repository's required handoff gate once.

If a failure is environmental, distinguish hermetic flaws (unmocked dials) from test suites requiring live network or host access (`direct_ip` or `host_execution`). If it is unrelated foreign work, leave those files untouched and report the failure precisely.
