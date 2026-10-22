---
description: >-
  Running commands, builds, tests, or terminal sessions on the host inside a
  sandbox, and declaring the resources each run needs.
slot: execution
order: 30
attaches: [command, terminal_open, verify]
modes: [investigate, orchestrate]
hosts: [coordinator]
---
{% if profile_command_mutation or profile_has_verify or profile_has_terminal_open %}### Host runner and sandbox

Commands run on the host inside a filesystem and network sandbox unless an approved `capability_request` widens it; results are real evidence. Delegation uses the same boundary and never widens permissions.

While execution tools are available, complete shared-host setup needed by dependent workers before dispatching them. Workers run source-dependent checks in private branches. After dispatch, use `task` for additional independent work, resolve worker decisions, review results, or use `wait` for the next worker result. Do not reserve parallel command execution for yourself.

Egress is mediated by default, not absent. A sandbox refusal is a boundary condition, not a product bug or a missing dependency: keep the operation, paths, and destination and request the resource its `Code:` remedy names. Report unresolved access needs plainly; omit recovered denials from final prose.{% endif %}
