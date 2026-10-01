# Hunt a performance regression

**Entry check:** you can run something that produces a number from the same input every time. If you cannot produce the number on demand, you cannot verify a fix either — build the measurement first, or report that you could not and stop.

## Workflow

1. **Build the repeatable measurement.** A benchmark, a timed command, a request with a fixed payload. Fix the input, the machine, the power state, and the cache condition, and write them down — they are part of the number.
2. **Measure the noise floor before trusting any comparison.** Run it at least 5 times *on the same commit* and record the spread:

   ```text
   same commit, 10 runs: 1.88 1.91 1.87 2.04 1.89 1.90 1.93 1.88 1.91 1.89
   median 1.90s, spread ±0.09s (±4.7%)
   → a regression smaller than ~5% is not measurable here
   ```

   If the spread is comparable to the regression you are chasing, **stop and reduce noise or increase the workload.** Bisecting inside the noise floor follows randomness to a confident wrong answer.
3. **Locate before explaining.** Profile the slow path. CPU, allocation, and wall-clock are different profiles answering different questions, and the most useful early signal is whether wall-clock grew while CPU did not — that means *waiting*, so look at I/O, a lock, or a network call rather than at algorithmic cost.
4. **Bisect with the measurement as the predicate.** Set the threshold from step 2's noise floor, not from a round number:

   ```bash
   git bisect start <slow-rev> <fast-rev>
   git bisect run sh -c 'test "$(./bench --median-ms)" -lt 1950'
   ```

5. **Check the dependency graph, not just your source.** A lockfile diff can move a dependency several versions and account for the entire regression. This is the common miss, because the commit that changed behavior touches nothing you wrote.
6. **Verify the fix with the same measurement under the same conditions,** and report both numbers with the run count and spread. "Faster" is not a result; `1.90s → 1.11s, ±0.05 over 10 runs` is.

## Stopping rule

Stop when the bisect names a commit whose effect exceeds the noise floor and the fix restores the measurement to the fast side of it. If the bisect lands inside the noise floor, the answer is "not measurable at this workload" — widen the workload and rerun, or report the limit. Do not accept a culprit you cannot demonstrate twice.

## What misleads people here

| Trap | Why it fools you |
|---|---|
| A single before/after pair | Two samples from a noisy distribution routinely show an improvement that does not exist |
| Comparing across conditions | Different machine, load, battery, or cache warmth — compare back to back in one session |
| The first slow-looking function | Inefficient-looking code can account for nothing; optimize what the profile names, never the reverse |
| Cold start counted as steady state | JIT warmup, pool filling, cache population — pick one and be consistent |
| A benchmark that optimizes away | An unused result may be deleted by the compiler; a suspiciously fast number is usually this |
| Symptom location | A slow suite may be one fixture; a slow endpoint may be a dependency's retry backoff |

## Do not

- Do not begin optimizing before you can measure — that is a rewrite with no way to know whether it helped.
- Do not change the benchmark and the code in the same step; one of them has to stay fixed to mean anything.
- Do not report an improvement without the run count and variance behind it.
- Do not leave profiling instrumentation, sampling hooks, or a benchmark harness enabled in the shipped path.

## Report

The measurement and its fixed conditions, the noise floor with run count, the profile finding, the culprit commit or dependency bump, and before/after numbers with spread.
