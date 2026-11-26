---
description: >-
  Running commands: network proxy defaults, capability requests, and
  verification flags.
slot: procedures
order: 10
attaches: [command, verify, terminal_open]
hosts: [coordinator, worker]
---
{% if profile_has_command %}Work still running after {{ runner_inline_wait_seconds }} seconds returns a running handle and continues in order; a wait longer than {{ runner_park_seconds }} seconds parks the call while the program continues in the background. Inspect terminal status and output; an ended wait is not success. Never relaunch live work.{% if profile_command_external_exec != false %} Use `&` for helpers the program needs.{% endif %}{% if profile_has_command_output %} Read `command_output` by cursor.{% endif %}{% if profile_has_wait %} `wait` wakes when it finishes.{% endif %}{% if profile_has_command_stop %} Stop work that is clearly stuck with `command_stop`{% if profile_command_external_exec != false %}; that also ends the `&` jobs it started{% endif %}.{% endif %}

{% endif %}{% if runner_socks_proxy_tools or profile_command_network_egress or runner_capability_request_tools or can_command_verify %}{% if runner_socks_proxy_tools %}HTTP(S) uses `HTTP_PROXY` / `HTTPS_PROXY`; other TCP needs `socks_proxy: true` on {% for tool in runner_socks_proxy_tools %}{% if not forloop.First %}{% if forloop.Last %} or {% else %}, {% endif %}{% endif %}`{{ tool }}`{% endfor %}. Git over SSH is preconfigured.{% endif %}{% if profile_command_network_egress %} Use native `curl` in `command` for APIs and local services.{% endif %}{% if profile_has_capture_page %} Static `capture_page` in `project_dir` mode needs no server.{% elif profile_can_request.capture_page %} For static previews, load page capture with `request_tools` instead of starting a server.{% endif %}
{% if runner_capability_request_tools %}
{% include "partials/capability-request-grammar.md" %}

Exercise existing entry points from docs or source; substitutes such as mocked input prove only themselves.{% endif %}{% if can_command_verify %} Use `command(verification: true)` for checks; do not repeat a passing check on unchanged source.{% endif %}{% endif %}
