---
name: diagnose-performance
description: Investigate slow queries, performance regressions, memory leaks, and disk, memory, or descriptor exhaustion.
metadata:
  paintedwolf.template_resources: references/diagnose_a_slow_database_query.md|references/hunt_a_performance_regression.md|references/investigate_a_memory_leak.md|references/diagnose_resource_exhaustion.md
---

# Diagnose performance

Use the request and observed project/environment to choose the matching procedure below. Read that procedure before acting; load only the variants needed for this task. Resource paths are relative to this skill directory.

- [Diagnose a slow database query](references/diagnose_a_slow_database_query.md) — Diagnose a slow SQL or document-database query from timing, waits, plans, cardinality, statistics, and indexes when a query, report, endpoint, or job is slow or regressed.
- [Hunt a performance regression](references/hunt_a_performance_regression.md) — Find the measured cause of an unexplained slowdown in a build, request, test suite, or job.
- [Investigate a memory leak](references/investigate_a_memory_leak.md) — Prove and locate retained-memory growth with controlled load, runtime metrics, and heap profiles when RSS, heap, object counts, goroutines, or workers grow over time.
- [Diagnose resource exhaustion](references/diagnose_resource_exhaustion.md) — Diagnose failures from exhausting disk, memory, file descriptors, or inodes when builds fail oddly, a process is killed, or writes fail with space apparently free.

## Boundaries

Follow project policy and the user’s requested scope. A procedure does not grant permission or imply that every listed toolchain is installed. Use only available host resources and tools; delegate or report a missing capability. Preserve original evidence, identify its source, and report verification limits.
