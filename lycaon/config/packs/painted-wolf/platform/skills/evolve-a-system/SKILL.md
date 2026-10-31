---
name: evolve-a-system
description: Review failure, retry, and idempotency policies; design state machines and concurrency; evolve APIs or fix systemic defects.
optional_tools:
metadata:
  paintedwolf.template_resources: references/finish_a_surface.md|references/raise_the_fix_altitude.md|references/evolve_an_api_contract.md|references/design_a_state_machine.md|references/review_failure_and_retry_semantics.md|references/design_concurrent_code.md|references/find_a_concurrency_bug.md
---

# Evolve a system

## Rules for every change

1. **Name the surface and its consumers before designing.** List the callers, generated clients, persisted data, and external APIs that read the shape. Resolve maturity from what the project declares (agent guidance, compatibility policy, changelog), not from caution: durable consumers get additive or versioned change, internal ones get redesign in place.
2. **Give every fact one authority.** A cache, replica, or projection that can disagree with the authority and win is a second source of truth, and that is the bug.
3. **Make invalid states unrepresentable** with an enum, a type, a constraint, or an atomic transition, rather than flags and caller checks.
4. **A timeout is not proof nothing happened.** Classify each failure point as known-failure, known-success, or ambiguous, and make retried effects idempotent under a stable request identity.
5. **Bound concurrency with a reason**, and give every mutable resource exactly one owner responsible for its mutation, cancellation, and shutdown.
6. **Finish in one arc.** Update callers, schemas, registrations, tests, and docs together; delete superseded paths, flags, and forwarders.
7. **Prove the class, not the instance.** Add an invariant, property, or contract test that fails for similar mistakes, and watch it fail once.

## Procedures

Open the one that matches the work:

- [Finish a surface](references/finish_a_surface.md): deliver a coherent surface rather than a patch, choosing the evolution class from declared maturity.
- [Raise the fix altitude](references/raise_the_fix_altitude.md): several related findings may be one missing invariant or boundary.
- [Evolve an API contract](references/evolve_an_api_contract.md): an API consumed outside the module. Follow the spec-to-codegen chain, keep changes compatible, and prove the wire shape.
- [Design a state machine](references/design_a_state_machine.md): async jobs, retries, approvals, protocols, or UI flows whose behavior depends on history.
- [Review failure and retry semantics](references/review_failure_and_retry_semantics.md): timeouts, duplicate delivery, idempotency, and ambiguous outcomes before shipping an integration.
- [Design concurrent code](references/design_concurrent_code.md): parallel workers or queues, with ownership, cancellation, backpressure, and shutdown.
- [Find a concurrency bug](references/find_a_concurrency_bug.md): a data race, deadlock, or leaked goroutine or thread behind intermittent failures.
