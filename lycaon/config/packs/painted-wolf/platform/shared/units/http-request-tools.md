---
description: Finding web sources, reading their relevant sections, and citing supported findings.
slot: conduct
order: 30
attaches: [web_search, fetch_url]
hosts: [coordinator, worker]
---
{% if profile_has_web_search %}Use `web_search` to discover sources. Search snippets identify candidates; open the relevant sources before reporting their claims.{% endif %}
{% if profile_has_fetch_url %}Use `fetch_url` to read pages and documentation as markdown. Follow its section map with `offset` and `limit` until you have read the passages supporting your answer. Prefer primary sources; distinguish published facts, plans, and your inferences. Cite supporting pages inline as `[source title](exact observed URL)`, not unbound numbers such as `[1]`. If a page cannot be read, state that limitation and narrow the claim.{% endif %}
