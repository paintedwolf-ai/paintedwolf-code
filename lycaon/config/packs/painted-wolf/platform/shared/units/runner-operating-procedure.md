---
description: >-
  Running commands: network proxy defaults, capability requests, and
  verification flags.
slot: procedures
order: 10
attaches: [command, verify, terminal_open]
hosts: [coordinator, worker]
---
{% if profile_has_command %}After 30 seconds, foreground work returns a running handle and continues in order; explicit waits may park. Running is not success: inspect readiness; never relaunch live work.{% if profile_command_mutation != false %} Use `&` for services.{% endif %}{% if profile_has_command_output %} Read `command_output` by cursor.{% elif profile_has_request_tools %} Load `command_output` via `request_tools`.{% endif %}{% if profile_has_command_stop %} Stop with `command_stop`.{% elif profile_has_request_tools %} Load `command_stop` via `request_tools` to stop.{% endif %} `timeout_ms` ends execution.

{% endif %}{% if profile_command_mutation != false or profile_has_verify or profile_has_terminal_open %}HTTP(S) uses `HTTP_PROXY` / `HTTPS_PROXY`; other TCP needs `socks_proxy: true`. Git over SSH is preconfigured.{% if profile_has_command %} When this surface permits network egress, use native `curl` in `command` for APIs and local services.{% endif %}{% if profile_has_capture_page %} Static `capture_page` in `project_dir` mode needs no server.{% elif profile_can_request.capture_page %} For static previews, load page capture with `request_tools` instead of starting a server.{% endif %}

{% include "partials/capability-request-grammar.md" %}

Exercise existing entry points from docs or source; substitutes prove only themselves.{% if profile_has_command %} Use `command(verification: true)` for checks; do not repeat a passing check on unchanged source.{% endif %}{% endif %}
