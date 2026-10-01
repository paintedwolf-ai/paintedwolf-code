# Stabilize a flaky test

Use this workflow when the same code produces different test results. The discipline that makes this tractable: **make the failure reproducible before you change anything.** A nondeterministic failure that "went away" after an edit is indistinguishable from one that simply did not occur that run — so without a reproduction you cannot know whether you fixed it, and the honest report is that you do not know.

## Workflow

1. **Get it to fail on demand first.** Run it repeatedly (`-count=100` or the runner's equivalent), with randomised order, and at the parallelism CI uses. If it will not fail locally, that is itself the finding: the difference between your machine and CI is the lead — usually core count, load, or a slower disk.
2. **Bisect the conditions, not the code.** Does it fail alone? Only with the rest of its package? Only in a specific order? Only under `-race` or high parallelism? Each answer eliminates whole categories below, and it is far faster than reading the test.
3. **Name the category before proposing a fix** — the table below covers nearly all of them. A fix that does not name the mechanism is a guess.
4. **Fix the mechanism at its root**, in the test or in the code under test, whichever actually holds the nondeterminism. If the *code* is racy, the test is doing its job and the fix belongs in the code.
5. **Verify under the conditions that produced the failure**, many times over. One green run after a fix proves nothing here. Report how many iterations under what conditions — that number is the evidence.

## Why tests flake

| Signal | Likely cause |
|---|---|
| Passes alone, fails with the package | **Shared state** — a global, a singleton, a database or temp directory not reset between tests. |
| Fails only in some orders | **Order dependence** — one test relies on another having run. Randomised order exposes it; running serially hides it. |
| Fails under load or on slower machines | **Real clock** — a sleep, a timeout, or an assertion that something completes "fast enough". Wall-clock assertions fail when CI is busy. |
| Fails roughly 1 in N with no pattern | **Unseeded randomness or map iteration order.** Go randomises map order on purpose; a test asserting sequence over a map is flaky by construction. |
| Fails only under `-race` or high parallelism | **A genuine data race.** Stop here and see `evolve-a-system` — the test found a real bug. |
| Fails when tests run in parallel, passes serially | **Resource collision** — two tests binding the same port, path, or fixture database. Allocate per-test, do not serialise. |
| Fails after an unrelated test was added | **Leaked state from a prior test** — a goroutine, timer, watcher, or open file descriptor still running. |
| Fails only in CI, never locally | Environment: a missing service, a different timezone or locale, no TTY, or a network the sandbox mediates. See `reach-a-network-service` before assuming the test is wrong. |

## Do not

- **Do not add a retry.** It converts a real defect into a slower, still-broken test, and it hides the failure from whoever hits it in production.
- **Do not add or lengthen a sleep.** A sleep tuned until it passes is tuned to today's machine; it will fail again on a slower one, and it slows every run in between. Wait on the condition, not on the clock.
- **Do not raise a timeout** to make a timing assertion pass unless you can show the new bound is the correct one.
- **Do not `skip`, quarantine, or mark it known-flaky** as the fix. That is a decision to stop testing something, and it belongs to the user, not to you — say so and let them choose.
- **Do not force serial execution** (`-p 1`, `--runInBand`) to make it pass. That hides an ordering or isolation bug and slows the suite permanently.
- Do not report a test "fixed" on a single green run. Say how many iterations you ran and under what conditions, or say you could not reproduce it.
