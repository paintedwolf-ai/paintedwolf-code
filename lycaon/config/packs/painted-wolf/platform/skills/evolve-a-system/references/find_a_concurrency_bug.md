# Find a concurrency bug

**Entry check:** the failure depends on timing — it is intermittent, or appears only under `-race`, load, or parallelism. A failure that reproduces every run is an ordinary bug; debug it normally.

**The fact that governs everything:** a race detector reports only the races that actually happened during that run. It observes one interleaving; it does not analyze the program. A clean run means "no race was observed on the paths exercised," never "there is no race."

## Workflow

1. **Classify the symptom** — the four families need different tools:

   | Symptom | Family | Tool |
   |---|---|---|
   | Wrong or impossible values, nonsensical panics | data race | race detector under repetition |
   | Hang, no CPU burn | deadlock | full stack dump of every thread |
   | No progress, CPU busy | livelock / starvation | sampling profiler |
   | Memory, handles, or thread count grows | leak | count at two quiet points |

2. **Run under the detector, and make it exercise the path.** `-race` (Go), TSan (C/C++/Rust), or the runtime equivalent, combined with repetition and real parallelism:

   ```bash
   go test -race -run TestCheckout -count=200 -parallel=8 ./billing/
   ```

   A detector on a single serial run of a path that never contends finds nothing and tells you nothing.
3. **For a hang, dump every stack, not the one that looks stuck.** A deadlock is a cycle and you cannot see a cycle from one end — `SIGQUIT` in Go, `jstack` on the JVM, `thread apply all bt` in gdb. Find the two holders waiting on each other.
4. **For a leak, count rather than eyeball.** Snapshot the goroutine or thread count at a quiet point, run the workload, return to a quiet point, and compare. Growth that does not return to baseline is the leak; the stacks in the second snapshot name where.
5. **Fix the synchronization, then prove it under the conditions that exposed it** — detector on, high iteration count, real parallelism. State the iterations and settings; that is the evidence.

## Stopping rule

Run the reproduction at the iteration count where it previously failed, times ten, with the detector on. Passing that is the evidence you report, stated with its numbers. It is not proof of absence, and you should not describe it as one. If a race was observed once and you can no longer reproduce it, report it open with what you know — never as fixed.

## What usually causes it

- **Unsynchronized shared state** — a map, slice, or counter touched from more than one goroutine or thread. The most common by a wide margin.
- **A mutex copied by value**, so each copy locks a different thing and the protection silently does nothing.
- **Lock ordering** — two paths taking the same two locks in opposite orders. The fix is one global acquisition order, not a bigger lock.
- **Channel and lifetime mistakes** — sending on a closed channel, closing twice, or a receiver that exits leaving a sender blocked forever.
- **Cancellation not propagated**, so work continues past its canceled context and outlives its parent scope.
- **A callback invoked from a different thread than its author assumed**, mutating state written as single-threaded.
- **Loop-variable capture** in closures launched in a loop, on runtimes where that variable is shared.

## Do not

- **Do not add a sleep to make it pass.** It changes the timing that exposed the bug without removing the bug; the failure returns on different hardware and is now harder to reproduce.
- **Do not widen a lock's scope until the symptom stops.** Taken far enough this serializes the program — the race is gone, traded for a performance bug that is much harder to attribute later.
- Do not claim a fix from a clean detector run alone.
- Do not disable the race detector in CI because it is slow or noisy; it is the only thing catching this class before users do.
- Do not treat "cannot reproduce" as "fixed".

## Report

The symptom family, the detector and settings used, the iterations run, the specific unsynchronized access or lock cycle found, the fix, and the evidence run with its numbers.
