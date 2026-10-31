---
name: verify-a-change
description: Choose and run the right checks after a code change, triage failing tests or builds, and report what passed and what stayed unverified.
---

# Verify a change

1. Choose validation from the changed behavior, repository instructions, and the user's request. Inspection may suffice for material whose result can be established by reading it. Small edits can still affect behavior; do not decide from filenames alone.
2. When execution is useful, start with the smallest documented check that covers the affected behavior. A selected project command is a default, not an instruction to run a full suite for every edit. Use it when its coverage is needed or explicitly required.
3. Read the digest or summary, not a filtered fragment. On failure, follow [failure triage](references/failure-triage.md). Keep mixed failures distinct; a sandbox refusal does not explain unrelated assertions or build errors.
4. Retry only when a relevant fix or an evidence-backed environment change gives the next run a reasonable prospect of helping. Do not weaken checks, repair unrelated work, or keep rerunning unchanged failures. Three settled attempts is a recovery ceiling, not a quota.
5. Validate the material you will deliver. Recheck behavior affected by later edits, but do not chase unrelated concurrent changes merely to refresh a receipt.
6. A running handle has no verdict. Wait for it instead of duplicating the command. Failed, stopped, timed-out, or boundary-refused runs do not pass. Both execution tools accept the same reviewed capabilities. A stop request is not proof of termination.
7. Report work delivered separately from validation: checks, outcomes, scope, and limitations. Completed work can have blocked validation. Optional assessment metadata helps explain the decision but is not permission to finish or promote. Explicit workflow test gates remain unmet without current passing evidence.
8. For a changed user-facing interface, follow visual evidence guidance. For runnable local services or APIs with an available harness, actively exercise changed endpoints with native `curl` in `command` (`-v` shows the request and response headers, `--fail-with-body` keeps an error body, `-w '%{http_code} %{time_total}'` reports the result), inspect responses, and fix issues before reporting; hermetic unit/integration tests remain primary when no live service is run. Containers are an option when authorized, available, and useful—not a compulsory detour after a failed test.
