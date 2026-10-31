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

Finish shared setup before dispatching dependent workers; workers run source checks in isolated branches. Use `task` for independent work; review results, resolve decisions or `wait` for a result. Workers cannot expand the access available to this chat.{% endif %}
