# Reduce a failing case

**Entry check:** the case fails the same way every run. If the result changes by chance, use `references/stabilize_a_flaky_test.md` first — reduction cannot reason from a candidate whose outcome is random.

## Workflow

1. **Pin the failure oracle.** Write down exactly what counts as "still the same failure": the structured error or exit code, the violated assertion, the relevant stack frame, and the required output shape. Fix the environment, version, seed, and setup. The oracle must reject passes, *different* failures, timeouts, and invalid candidates:

   ```text
   oracle: exit 1 AND stderr matches "panic: index out of range \[3\]"
           AND frame parser.parseHeader present
   not the oracle: exit != 0        (accepts setup errors and timeouts)
   ```

2. **Prove the original reproduces.** Run it enough times to establish determinism before removing anything.
3. **Choose a removable structure.** Treat the case hierarchically and reduce in this order — files, requests, operations, declarations, records, fields, then tokens or bytes. Preserving syntax and semantic prerequisites keeps the time budget on meaningful candidates.
4. **Reduce by subsets and complements** (ddmin). The loop is mechanical:

   ```text
   granularity n = 2
   loop:
     split the case into n chunks
     if any single chunk still fails      → keep it, n = 2, repeat
     if any complement still fails        → keep it, n = max(n-1, 2), repeat
     if n < number of chunks              → n = min(2n, chunks), repeat
     else                                 → done, case is 1-minimal
   ```

   Testing complements is what catches interacting elements; dropping that step stalls the search early on any case where two parts are jointly required. Reset external state between attempts, and cache candidate results only when the test is deterministic.
5. **Simplify values after structure.** Replace names, numbers, nesting, timing, and payloads with canonical smaller values, keeping the oracle exact. Re-check from a clean state after every accepted reduction.
6. **Confirm local minimality.** Try removing each remaining unit once. The case is done when no single remaining unit can go without losing the oracle.
7. **Promote the result.** Turn the minimal case into a readable regression fixture, keep the original only where it adds coverage, and replay the reduced case once from a clean state independently before deleting anything.

## Stopping rule

Stop at 1-minimal — one full pass over the remaining units with no successful removal. Do not chase a globally smallest case; that needs an exhaustive search you are not running, and "no single unit can be removed" is the claim you can actually support. If a pass makes no progress at the finest granularity, the case is done.

## Boundaries

- Hold the program under test fixed while reducing its input. Mixing code fixes into the reduction destroys the experiment.
- Do not weaken the oracle to make a candidate count, or accept a parser or setup failure in place of the target behavior.
- Do not delete the sole original reproducer until the reduced case has passed a clean independent replay.
- Minimized data can still carry secrets or personal information; redact by semantics, then re-check the oracle.

## Report

The oracle as pinned, the reduction dimensions used, the original and final sizes, the final required elements and why each is needed, and any nondeterminism or environmental dependency that would not reduce.
