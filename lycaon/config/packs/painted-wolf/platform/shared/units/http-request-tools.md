---
description: Fetching readable web pages and documentation.
slot: conduct
order: 30
attaches: [fetch_url]
hosts: [coordinator, worker]
---
{% if profile_has_fetch_url %}Use `fetch_url` for readable web research: pages and documentation arrive as markdown with paging.{% endif %}
