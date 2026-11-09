---
description: >-
  Choosing the first read when a request is about an unfamiliar area, a named
  file or package, a JSON or YAML config, or the findings of a completed scan;
  the ladder from a repository map to a bounded read.
slot: orientation
order: 10
needed_with: [command, scan_query, scan_summary]
hosts: [coordinator, worker]
---
**Choose the next read from the question and existing evidence.**

{% if profile_has_summarize %}
- **Explain a named file or area** → `summarize` with `path` and the task argument.
- **Explain an area found through search** → summarize its containing path.
{% endif %}
{% if profile_has_command %}
- **Locate an unfamiliar area** → `ls` or `find . -maxdepth 2 -type d` in `command` (`.` walks every attached folder), then scope the next read.
{% endif %}
{% if profile_has_survey_repo %}
- **Cannot name a destination** → `survey_repo`, then inspect the relevant destination. Use scoped search for routes or repeated configuration.
{% endif %}
{% if profile_has_command %}
- **Path in hand is JSON or YAML and you need fields** → `jq '.field' file` or `yq` in `command`; print raw lines only for a verbatim anchor.
{% endif %}
{% if profile_has_scan_drilldown %}
- **Board shows `Scan: complete`** and the question is those findings → **`scan_summary`** then **`scan_query`**. Do not start a threat-model from `scan_list`.
{% endif %}
{% if profile_has_command %}
- **One named symbol or line range for an exact fact, quote, or edit** → a bounded `grep -n`, `cat -n FILE`, or `sed -n 'A,Bp'` in `command`; `sed` prints no numbers to cite. If existing evidence already answers the request, stop.
{% endif %}

Cite what a numbered read or drill tool returned; a summary or digest orients only.
