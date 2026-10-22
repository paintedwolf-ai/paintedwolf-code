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
{% if profile_command_mutation or profile_has_verify or profile_has_terminal_open %}### Sandbox and worker checks

Complete shared setup needed by dependent workers before dispatching them; workers run source checks in isolated branches, and delegation never widens sandbox permissions. Do not run parallel commands alongside active workers.{% endif %}
