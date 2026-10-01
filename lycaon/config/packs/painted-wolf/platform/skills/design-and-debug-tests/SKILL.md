---
name: design-and-debug-tests
description: Minimize reproductions, diagnose flaky tests, and design property-based or invariant regression tests.
metadata:
  paintedwolf.template_resources: references/reduce_a_failing_case.md|references/stabilize_a_flaky_test.md|references/write_property_based_tests.md|references/write_invariant_guards.md
---

# Design and debug tests

Use the request and observed project/environment to choose the matching procedure below. Read that procedure before acting; load only the variants needed for this task. Resource paths are relative to this skill directory.

- [Reduce a failing case](references/reduce_a_failing_case.md) — Minimize a failing input, configuration, trace, fixture, or program while preserving the same failure oracle when a reproduction is too large or noisy to debug or keep.
- [Stabilize a flaky test](references/stabilize_a_flaky_test.md) — Find and fix the cause of a test that passes and fails on the same code when a test is intermittent, fails only in CI, or fails only when run with others.
- [Write property based tests](references/write_property_based_tests.md) — Design property-based tests with invariants, generators, shrinkers, seeds, and independent oracles when behavior spans a large input space or examples miss interactions.
- [Write invariant guards](references/write_invariant_guards.md) — Add broad invariant or contract tests without hand-maintained allow/deny lists when a bug class could recur under new cases or renamed symbols.

## Boundaries

Follow project policy and the user’s requested scope. A procedure does not grant permission or imply that every listed toolchain is installed. Use only available host resources and tools; delegate or report a missing capability. Preserve original evidence, identify its source, and report verification limits.
