# Design concurrent code

**Entry check:** name the independent work and the measured reason it must overlap — a latency target, a throughput target, or a responsiveness requirement. "It could run in parallel" is not a reason. Sequential code that meets the target is the better design.

## Workflow

1. **Bound the concurrency.** State the maximum useful concurrency and what limits it: cores, connection pool size, downstream rate limit, or memory per task. A number you cannot justify becomes an unbounded worker pool later.
2. **Write the responsibility table.** One row per mutable resource, queue, connection, and child task. This is the artifact:

   | Resource | Mutated by | Closed by | Guarded by | Transfer |
   |---|---|---|---|---|
   | `results` map | collector goroutine only | collector | single writer; workers send on `out` | none |
   | `out` channel | workers send | collector, after `wg.Wait()` | capacity 32 | producers → collector |
   | db conn | task that checked it out | returned to pool on defer | pool | per-task lease |

   A row with two mutators and no guard is the bug, visible before any code exists.
3. **Name the synchronization for each ordering claim.** For every "X must be visible before Y", say which primitive establishes it — a mutex release/acquire pair, a channel send/receive, an atomic with stated ordering, a task join. Sleeps, scheduler behavior, and "this normally finishes first" establish nothing.
4. **Give every task a lifetime.** Tie children to a parent scope, propagate cancellation and deadlines downward, collect errors upward, and either join or detach under a named long-lived supervisor. State explicitly what happens to the siblings when one child fails: cancel all, continue, or collect and report.
5. **Bound demand.** Set queue capacity and worker count, then choose the behavior when full — block the caller, shed load, or fail fast. Unbounded queues convert overload into a delayed out-of-memory kill. Retries must draw from the same budget rather than multiplying it.
6. **Order the shutdown.** Write the sequence explicitly; this order is the one most designs get wrong:

   ```text
   1. stop admitting new work
   2. cancel in-flight contexts
   3. drain the queue (bounded by a deadline)
   4. commit or roll back external effects
   5. close resources — the designated closer from the table closes them
   6. join all tasks, then return
   ```

   Decide whether interrupted units resume, retry, compensate, or stay visible for recovery.
7. **Test adversarial schedules.** Run the race detector, deterministic barriers or fakes, repeated stress, cancellation injected at each boundary, queue saturation, and shutdown while work is in flight. Assert final invariants and the absence of leaked tasks. A fixed sleep is not synchronization evidence, and a test that passes once under a race detector has not been run enough.

## Stopping rule

The design is ready when every row in the responsibility table has exactly one mutator or a named guard, every ordering claim names its primitive, and the shutdown sequence terminates with every resource assigned a closer. Add a lock only when a table row demands it, never to quiet an intermittent failure.

## Boundaries

- Do not add a lock or channel without naming the state it protects and the access rule it enforces.
- Do not launch fire-and-forget work from a request or test without a supervisor, a bounded lifetime, and an error path.
- Do not hold a lock across an unknown callback, blocking I/O, or an external call unless the design proves why it is necessary.
- `references/find_a_concurrency_bug.md` diagnoses an existing failure; this skill establishes the design before or during implementation.

## Report

The concurrency bound and its justification, the responsibility table, the ordering claims with their primitives, the shutdown sequence, and the adversarial schedules exercised.
