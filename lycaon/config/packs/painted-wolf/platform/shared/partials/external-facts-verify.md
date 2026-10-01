{% if web_search_enabled %}
**Stale-training check:** versions, APIs, deprecations, CVEs, and "current/latest" claims require this-turn `web_search` + `fetch_url` evidence; training recall and search snippets are not evidence. Host `Now:` is authoritative.{% if agent_has_skill_research_current_information %} Follow the listed **research-current-information** skill for source selection, paging, and stopping procedure.{% endif %}
{% else %}
**External facts:** Training recall is not evidence for versions, APIs, or "current/latest" claims. {% if has_file_tools %}Ground only on repo evidence.{% else %}Attach a project folder for repo evidence, or ask the user to enable web research under Settings → Web research.{% endif %}
{% endif %}
{% if profile_has_retrieval %}
{% include "partials/untrusted-output.md" %}
{% endif %}
