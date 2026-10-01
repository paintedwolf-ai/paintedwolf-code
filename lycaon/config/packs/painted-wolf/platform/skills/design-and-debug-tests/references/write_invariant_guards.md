# Write invariant guards

**Entry check:** the bug you just fixed could recur in a place that does not exist yet — a new handler, a renamed symbol, a future fixture. If the bug can only recur in the one function you fixed, write the ordinary regression test and stop.

## Workflow

1. **State the invariant in one sentence** over observable behavior or structure:

   ```text
   every exported handler registers exactly once
   no production path constructs SQL by string concatenation
   parsed output round-trips for every fixture in the corpus directory
   ```

2. **Choose a generative harness the repository already uses:** table-driven cases discovered from a directory, registry or reflection enumeration, an AST or contract scan, a property test, or a golden corpus. The discriminator is where the subjects come from — the system under test, never a list you typed.

   ```text
   list-shaped (rots):      for _, name := range []string{"a", "b", "c"}
   generative (stays true): for _, name := range registry.All()
   ```

   The second form fails the day someone adds `d` and forgets the rule. The first passes forever.
3. **Fail on the class.** Assert the rule for every discovered subject and report all violations at once, not just the first. A guard that stops at the first failure turns a ten-site cleanup into ten runs.
4. **Document the discovery root, not the exceptions.** When enumeration is incomplete by design, say in the test where subjects come from and what is out of scope. If the project must carve out a real exception, put it next to the authority that defines it — a marker on the subject, not a second list in the test file.
5. **Prove it catches the bug.** Temporarily reintroduce the mutation or replay the original failure and watch the guard fail. A guard never observed failing is not yet evidence of anything. Keep the minimized regression alongside the broad guard when both add value.
6. **Run it through the project's normal test entrypoint** and keep the runtime appropriate for the gate that will own it.

## Stopping rule

One guard per invariant. When a second invariant appears, write a second guard rather than widening the first into a general linter — a guard that checks three unrelated rules reports one failure for three different reasons and nobody can tell which fired.

## Boundaries

- Do not replace a failing concrete regression with only a slow unbounded search.
- Do not invent a new test framework when the repository already has one.
- Do not encode product policy as an ever-growing string list in test code when structure or types can express it.

## Report

The invariant, the discovery root and what it enumerates, the mutation that proved the guard fires, any documented carve-out and its defining authority, and the gate the guard runs in.
