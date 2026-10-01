---
description: >-
  Operating the host command runner: mediated egress and proxies, attributing
  network failures, capability requests, argv sequences, and verifying the
  exercised behavior.
slot: procedures
order: 10
attaches: [command, verify, terminal_open]
hosts: [coordinator, worker]
---
{% if runner_host_resource_tools %}
For local daemons, put `capability_request.host_resources` with the listed id on the exact live `{{ runner_host_resource_tools|join:"` / `" }}`; invent `socket_paths` only when a skill says to. A socket refusal is a missing route, not a missing binary.
{% endif %}

**Egress is mediated by default.**{% if profile_has_http_request %} Use `http_request` for API, webhook, and development-service actions; reserve runner networking for a process whose protocol is not represented by that schema.{% endif %} A host process gets HTTP(S) through the injected `HTTP_PROXY` / `HTTPS_PROXY` with no capability request; other TCP uses `socks_proxy: true`. Git over SSH is already wired.{% if profile_has_capture_page %} For static page preview, `capture_page` in `project_dir` mode needs no server.{% endif %} An application's network error is not a sandbox refusal; a refusal arrives as a `Code:` with its remedy{% if profile_has_command or profile_has_verify %}, and a live job's `command_output` reports its observed destinations under `network`{% endif %}.

{% include "partials/capability-request-grammar.md" %}

{% if profile_has_command %}Runs as an argv sequence without a shell; pipelines (`|`), redirections (`2>&1`, `> /dev/null`), and leading `NAME=value` scope to their stage; pass shared variables in `env`. For behavioral checks, set `command(verification: true)`. Report scope and outcome; do not rerun a passing check.{% endif %}

**Verify the exercised behavior.** Check the observed path before claiming coverage; inspect or test any remaining path separately. Preserve product behavior when selecting a supported execution route. Unless asked, keep transport mechanics internal and describe outcomes and untested behavior in product terms, in both progress updates and closeout. Do not predict a different result on a "normal" machine from source inspection alone.
