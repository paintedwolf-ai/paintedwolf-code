---
name: design-and-debug-tests
description: Minimize a failing reproduction, fix a flaky or CI-only test, or design property-based and invariant tests that keep a bug class from returning.
metadata:
  paintedwolf.template_resources: references/reduce_a_failing_case.md|references/stabilize_a_flaky_test.md|references/write_property_based_tests.md|references/write_invariant_guards.md
---

# Design and debug tests

## Rules for every test task

1. **Pin the oracle.** Write down exactly what counts as the failure (error code, assertion, frame, output shape) and what does not (setup errors, timeouts, different failures).
2. **Reproduce before changing.** A nondeterministic failure that "went away" after an edit is indistinguishable from one that did not occur that run.
3. **Fix the mechanism, not the symptom.** No retries, added or longer sleeps, raised timeouts, skips, quarantines, or forced serial runs to get green. Skipping or quarantining is the user's decision.
4. **Wait on conditions, not the clock.** Fixed delays are tuned to today's machine.
5. **Use the framework the repository already has.** Do not add one because a technique applies.
6. **Prove a new test fails.** Reintroduce the bug or mutate the code and watch it go red; a test never observed failing is not evidence.
7. **Report iterations and conditions**, e.g. "100 runs at CI parallelism, 0 failures", or say you could not reproduce it.

## Procedures

Open the one that matches the task:

- [Reduce a failing case](references/reduce_a_failing_case.md): a deterministic failure is too large or noisy to debug or keep. Covers ddmin reduction to a 1-minimal case.
- [Stabilize a flaky test](references/stabilize_a_flaky_test.md): a test passes and fails on the same code, fails only in CI, or only with others. Includes a signal-to-cause table.
- [Write property-based tests](references/write_property_based_tests.md): behavior spans a large input space. Covers property shapes, independent oracles, generators, shrinking, and seeds.
- [Write invariant guards](references/write_invariant_guards.md): a fixed bug could recur under new cases or renamed symbols. Covers generative discovery instead of hand-kept lists.
