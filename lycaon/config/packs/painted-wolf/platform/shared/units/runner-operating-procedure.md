---
description: >-
  Running commands: network proxy defaults, capability requests, and
  verification flags.
slot: procedures
order: 10
attaches: [command, verify, terminal_open]
hosts: [coordinator, worker]
---
{% if profile_command_mutation != false or profile_has_verify or profile_has_terminal_open %}HTTP(S) uses `HTTP_PROXY` / `HTTPS_PROXY` automatically; other TCP needs `socks_proxy: true`. Git over SSH is preconfigured.{% if profile_has_command %} When this surface permits network egress, use native `curl` in `command` for APIs, webhooks and development services.{% endif %}{% if profile_has_capture_page %} Static `capture_page` in `project_dir` mode needs no server.{% endif %}

{% include "partials/capability-request-grammar.md" %}

Exercise existing entry points from docs or source; substitutes prove only themselves.{% if profile_has_command %} Use `command(verification: true)` for checks; do not repeat a passing check on unchanged source.{% endif %}{% endif %}
