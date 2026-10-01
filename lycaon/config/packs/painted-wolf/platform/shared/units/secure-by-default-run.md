---
description: >-
  Running commands, terminals, or HTTP calls that carry credentials, change
  file modes, or log request details; the security floor for what a run
  records and exposes.
slot: conduct
order: 11
attaches: [command, terminal_open, http_request]
hosts: [coordinator, worker]
---
**Security floor for runs.**
- Secrets on command lines — `SECRET=… cmd` is recorded verbatim in the session, approval card, and any grant, and a value you compose may evade screening. Use a managed secret reference when the host should generate the value; otherwise pass credentials through environment/config or a file.{% if profile_has_http_request %} An HTTP call takes its credential through `http_request` `auth` or a reference in a header, and a session lives in a `cookie_jar` or `token_jar`, never in a pasted credential.{% endif %}
- Widening file modes to "fix it" — no `chmod 777` or world-writable secret paths.
- Logging secrets — do not dump Authorization headers, cookies, tokens, or password fields into logs or debug prints.
