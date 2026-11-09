---
description: Finding web sources, reading supporting passages, and citing findings.
slot: conduct
order: 30
attaches: [web_search, fetch_url]
hosts: [coordinator, worker]
---
{% if profile_has_web_search %}`web_search` discovers sources.{% endif %}
{% if profile_has_fetch_url %}Use `fetch_url` for readable web research. Read relevant `offset`/`limit` sections; cite `[title](observed URL)` inline, never `[1]`. Prefer primary sources; disclose gaps.{% endif %}
