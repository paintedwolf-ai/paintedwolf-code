---
description: >-
  Choosing the first read when a request is about an unfamiliar area, a named
  file or package, a JSON or YAML config, or the findings of a completed scan;
  the ladder from a repository map to a bounded read.
slot: orientation
order: 10
# Turns that oriented themselves in the project or read scan findings needed the ladder.
needed_with: [list_dir, summarize, survey_repo, find, jq, scan_query, scan_summary]
hosts: [coordinator, worker]
---
**Choose the next read from the question and existing evidence.**

{% if profile_has_summarize %}
- **Explain a named file or area** → `summarize(path=…, task=…)`.
- **Explain an area found through search** → summarize its containing path.
{% endif %}
{% if profile_has_list_dir %}
- **Locate an unfamiliar area** → `list_dir({"path":"."})`, then scope the next read.
{% endif %}
{% if profile_has_survey_repo %}
- **Cannot name a destination** and the ask is unknown layout, duplicated config/stores, or where HTTP routes live → **`survey_repo(bundle=layout_overview|ssot_drift|api_routes)`** once, then {% if profile_has_summarize %}**`summarize`** the destination it named{% else %}drill the destination{% endif %}.
{% endif %}
{% if profile_has_jq %}
- **Path in hand is JSON/YAML/TOML and you need fields** → **`jq(path, query=…)`**. `read` only for a verbatim anchor.
{% endif %}
{% if profile_has_scan_drilldown %}
- **Board shows `Scan: complete`** and the question is those findings → **`scan_summary`** then **`scan_query`**. Do not start a threat-model from `scan_list`.
{% endif %}
- **One named symbol or line range for an exact fact, quote, or edit** → bounded **`grep`** / **`read`**. If existing evidence already answers the request, stop.

Cite {% if profile_has_summarize %}`summarize` source windows or anchors (`summarize#N`), or {% endif %}drill-tool output; digest orients only.
