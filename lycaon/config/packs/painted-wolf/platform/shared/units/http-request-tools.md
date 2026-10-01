---
description: >-
  Calling an HTTP API, webhook, login, or development service directly, or
  fetching a readable web page.
slot: conduct
order: 30
attaches: [http_request, fetch_url]
hosts: [coordinator, worker]
---
### HTTP actions

{% if profile_has_http_request %}Use `http_request` for APIs, webhooks, logins, and development services; for runnable services, actively exercise changed endpoints, inspect responses, and fix defects encountered.{% endif %}{% if profile_has_fetch_url %} Use `fetch_url` for readable web research: it returns web pages and documentation as markdown with paging{% if profile_has_http_request %}, where `http_request` returns raw HTML{% endif %}.{% endif %}{% if profile_has_wait %} Use `wait` with `http_ready`/`port_ready` for readiness.{% endif %} Follow native `replacement_calls` when redirected.
