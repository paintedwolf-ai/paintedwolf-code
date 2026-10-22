---
description: >-
  Running commands: network proxy defaults, capability requests, and
  verification flags.
slot: procedures
order: 10
attaches: [command, verify, terminal_open]
hosts: [coordinator, worker]
---
{% if profile_command_mutation != false or profile_has_verify or profile_has_terminal_open %}Network access goes through a proxy automatically (`HTTP_PROXY` / `HTTPS_PROXY` for HTTP(S), `socks_proxy: true` for other TCP, and git over SSH is preconfigured).{% if profile_has_http_request %} Use `http_request` for APIs, webhooks, and development services; use command networking for protocols it does not support.{% endif %}{% if profile_has_capture_page %} For static page preview, `capture_page` in `project_dir` mode needs no server.{% endif %}

{% include "partials/capability-request-grammar.md" %}

Run the existing entry point from project docs or source; a substitute script proves only its own path.{% if profile_has_command %} Set `command(verification: true)` for behavioral checks; do not rerun a passing check on unchanged source.{% endif %}{% endif %}
