---
description: >-
  Operating the host command runner: mediated egress and proxies, attributing
  network failures, capability requests, bash programs, and verifying the
  exercised behavior.
slot: procedures
order: 10
attaches: [command, verify, terminal_open]
hosts: [coordinator, worker]
---
{% if profile_command_mutation != false or profile_has_verify or profile_has_terminal_open %}{% if runner_host_resource_tools %}
For local daemons, pass `capability_request.host_resources` with the listed id on `{{ runner_host_resource_tools|join:"` / `" }}`; use `socket_paths` only when a skill names an exact socket.
{% endif %}

**Egress is mediated by default.**{% if profile_has_http_request %} Use `http_request` for API, webhook, and development-service calls; reserve runner networking for protocols outside that tool.{% endif %} Processes get HTTP(S) through `HTTP_PROXY` / `HTTPS_PROXY` automatically; other TCP uses `socks_proxy: true`, and git over SSH is already wired.{% if profile_has_capture_page %} For static page preview, `capture_page` in `project_dir` mode needs no server.{% endif %} An application network error is not a sandbox refusal; refusals return a `Code:` with a remedy{% if profile_has_command or profile_has_verify %}, and `command_output` reports observed destinations under `network`{% endif %}.

{% include "partials/capability-request-grammar.md" %}

{% if profile_has_command %}For behavioral checks, set `command(verification: true)`. Report scope and outcome; do not rerun a passing check.{% endif %}

**Verify the exercised behavior.** Run existing entry points from project docs or source. Substitute scripts cover only their own path; check others separately. Keep transport mechanics internal unless asked, and describe outcomes and untested behavior in product terms.{% endif %}
