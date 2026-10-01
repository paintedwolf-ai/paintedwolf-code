# Investigate a memory leak

**Entry check:** memory grows across a repeatable workload and does not return after the workload stops. A leak is retained state outliving its usefulness — a high allocation rate, high RSS, or one out-of-memory event proves none of that on its own.

## Workflow

1. **Make the growth reproducible and graph the slope.** Hold workload shape and concurrency constant, record runtime and build versions, and track over time: process RSS, managed heap, allocation rate, GC count, object counts, goroutine or thread counts, and throughput. Normalize memory by completed work when the system legitimately accumulates state.
2. **Separate the memory domains** — this decides which tool answers next:

   | Observation | Domain | Look at |
   |---|---|---|
   | managed heap grows after comparable GC cycles | retained objects | heap profile, dominators |
   | RSS grows, managed heap flat | native / runtime | mapped files, thread stacks, allocator fragmentation, subprocesses |
   | goroutine or thread count grows | task lifetime | creation stacks at two snapshots |
   | RSS flat, throughput falling | not a leak | look elsewhere |

   Do not tune the GC to flatten a curve caused by a lifecycle bug.
3. **Capture a baseline and a later profile under equivalent conditions.** Prefer the runtime's own profiler — Go heap profiles, V8 heap snapshots, JVM histograms or dumps, .NET counters and dumps, Python `tracemalloc`, or an already-installed native profiler. Record collection time, completed workload count, and profiler overhead with each capture.
4. **Compare retained state, not allocations.** Use in-use and retained views, dominators, growth by type or stack, and paths from roots. Allocation profiles show churn; they cannot show what stayed alive. For goroutine, task, listener, timer, or thread leaks, compare counts and creation stacks as well as bytes.
5. **Trace reachability to the missing lifecycle step.** The usual roots: unbounded caches, registries, queues, subscriptions, retry state, per-request globals, forgotten cancellation, unclosed resources, and callbacks holding large graphs. Name the specific root keeping the object reachable before proposing a fix.
6. **Fix the lifecycle and verify the slope.** Add the missing bound, eviction, cancellation, close, or reference release, then repeat the same workload past the previous failure point and show retained memory reaching a stable plateau.

## Stopping rule

Run the fixed build past the point where the original failed, at the same workload, and require the retained-memory slope to flatten — not merely to rise more slowly. A slower climb is a smaller leak. If the plateau does not appear within twice the original failure point, the root you named was not the only one.

## Collection safety

- Heap snapshots and dumps can pause the process, allocate substantial extra memory, or trigger the very OOM you are chasing. Measure headroom first; use production only with explicit approval.
- Dumps and profiles can contain credentials, request bodies, personal data, and decrypted secrets. Never commit or upload them, and do not quote sensitive object contents.
- Do not install a profiler, expose a diagnostics endpoint, restart a shared process, or raise a memory limit to collect evidence without approval.
- Do not read lower RSS immediately after a restart or a forced collection as success.

## Report

The workload and duration, the memory domain, profile timestamps with workload counts, the dominant growth and its retaining root, the fix, and before/after slopes showing the plateau.
