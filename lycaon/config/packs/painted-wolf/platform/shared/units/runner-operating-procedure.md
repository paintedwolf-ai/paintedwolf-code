---
description: >-
  Running commands: network proxy defaults, capability requests, and
  verification flags.
slot: procedures
order: 10
attaches: [command, verify, terminal_open]
hosts: [coordinator, worker]
---
{% if profile_command_mutation != false or profile_has_verify or profile_has_terminal_open %}Network access goes through a proxy automatically (`HTTP_PROXY` / `HTTPS_PROXY` for HTTP(S), `socks_proxy: true` for other TCP, and git over SSH is preconfigured).{% if profile_has_http_request %} Prefer `http_request` for HTTP APIs and webhooks.{% endif %}{% if profile_has_capture_page %} For static page preview, `capture_page` in `project_dir` mode needs no server.{% endif %}

{% include "partials/capability-request-grammar.md" %}

{% if profile_has_command %}For behavioral checks, set `command(verification: true)`.{% endif %}{% endif %}
