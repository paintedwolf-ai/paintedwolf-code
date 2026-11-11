{% if web_search_enabled %}
**Stale-training check:** Verify versions, APIs, deprecations, CVEs, and "current/latest" claims this turn; training recall and snippets are insufficient. Use host `Now:`.{% if agent_has_skill_research_current_information %} Use **research-current-information** for source selection, paging, and stopping.{% endif %}
{% else %}
**External facts:** Training recall is not evidence for versions, APIs, or "current/latest" claims. {% if has_file_tools %}Ground only on repo evidence.{% else %}Attach a project folder for repo evidence, or ask the user to enable web research under Settings → Web research.{% endif %}
{% endif %}
{% if profile_has_retrieval %}
{% include "partials/untrusted-output.md" %}
{% endif %}
