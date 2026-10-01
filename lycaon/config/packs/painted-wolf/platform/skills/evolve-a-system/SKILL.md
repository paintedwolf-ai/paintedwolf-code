---
name: evolve-a-system
description: Review failure, retry, and idempotency policies; design state machines and concurrency; evolve APIs or fix systemic defects.
optional_tools:
  - http_request
metadata:
  paintedwolf.template_resources: references/finish_a_surface.md|references/raise_the_fix_altitude.md|references/evolve_an_api_contract.md|references/design_a_state_machine.md|references/review_failure_and_retry_semantics.md|references/design_concurrent_code.md|references/find_a_concurrency_bug.md
---

# Evolve a system

Use the request and observed project/environment to choose the matching procedure below. Read that procedure before acting; load only the variants needed for this task. Resource paths are relative to this skill directory.

- [Finish a surface](references/finish_a_surface.md) — You must use this skill when designing production or otherwise important systems.
- [Raise the fix altitude](references/raise_the_fix_altitude.md) — Test whether a list of findings is one underlying defect before fixing them one at a time when a review, audit, or bug hunt returns several related issues.
- [Evolve an API contract](references/evolve_an_api_contract.md) — You must use this skill when working with an API that is in active use.
- [Design a state machine](references/design_a_state_machine.md) — Model async jobs, retries, approvals, protocols, or UI flows as explicit states, events, guards, effects, and terminal outcomes before coding.
- [Review failure and retry semantics](references/review_failure_and_retry_semantics.md) — Review timeouts, retries, duplicate delivery, idempotency, and ambiguous outcomes before shipping an API, queue, worker, workflow, payment flow, or other integration.
- [Design concurrent code](references/design_concurrent_code.md) — Design concurrent code with explicit mutation responsibility, synchronization, cancellation, backpressure, and shutdown before adding parallel workers or queues.
- [Find a concurrency bug](references/find_a_concurrency_bug.md) — Track down a data race, deadlock, or leaked goroutine or thread when behaviour is intermittent, a process hangs, or a test fails only under -race or high parallelism.

## Boundaries

Follow project policy and the user’s requested scope. A procedure does not grant permission or imply that every listed toolchain is installed. Use only available host resources and tools; delegate or report a missing capability. Preserve original evidence, identify its source, and report verification limits.
