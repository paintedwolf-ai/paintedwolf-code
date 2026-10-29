---
description: >-
  Calling an HTTP API, webhook, login, or development service, or fetching a
  readable web page.
slot: conduct
order: 30
attaches: [command, fetch_url]
hosts: [coordinator, worker]
---
### HTTP actions

{% if profile_has_command %}When this surface permits network egress, use `curl` in `command` for APIs, webhooks, logins, and development services. Inspect status, headers and bodies when exercising changed endpoints.{% endif %}{% if profile_has_fetch_url %} Use `fetch_url` for readable web research: it returns pages and documentation as markdown with paging.{% endif %}{% if profile_has_wait %} Use `wait` with `http_ready`/`port_ready` for readiness.{% endif %}
