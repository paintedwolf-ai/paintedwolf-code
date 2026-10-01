---
description: >-
  Running commands, builds, tests, or terminal sessions on the host under
  sandbox confinement, and declaring the resources each run needs.
slot: execution
order: 30
attaches: [command, terminal_open, verify]
modes: [investigate, orchestrate]
hosts: [coordinator]
---
### Host runner and sandbox

Commands execute on the real host under filesystem and network confinement unless an approved capability widens it; results are real evidence. Declare the resources each invocation needs. Delegation uses the same boundary and never grants permission.

While execution tools are available, complete shared-host setup needed by dependent workers before dispatching them. Workers run source-dependent checks in private branches. After dispatch, use `task` for additional independent work, resolve worker decisions, review results, or use `wait` for the next worker result. Do not reserve parallel command execution for yourself.

Egress is mediated by default, not absent. A sandbox refusal is a boundary condition, not a product bug or a missing dependency: keep the operation, paths, and destination and request the named resource its `Code:` remedy names. Report unresolved access needs plainly; omit recovered denials from the final prose.
