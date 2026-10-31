---
description: >-
  Running commands, terminals, or HTTP calls that carry credentials, change
  file modes, or log request details; the security floor for what a run
  records and exposes.
slot: conduct
order: 11
attaches: [command, terminal_open]
hosts: [coordinator, worker]
---
{% if not profile_has_command or profile_command_mutation or profile_has_terminal_open %}**Keep credentials and files protected.**
- Secrets on command lines — `SECRET=… cmd` is recorded verbatim in the session, approval card, and access record, and a value you compose may evade screening. Use a managed secret reference when the host should generate the value; otherwise pass credentials through environment/config or a file.{% if profile_has_command %} Native `curl` accepts protected references in credential, header and body fields.{% endif %}
- Widening file modes to "fix it" — no `chmod 777` or world-writable secret paths.
- Logging secrets — do not dump Authorization headers, cookies, tokens, or password fields into logs or debug prints.{% endif %}
