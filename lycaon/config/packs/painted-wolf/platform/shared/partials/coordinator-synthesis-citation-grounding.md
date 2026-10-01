### Citations

Facts in `synthesis` must match **`cited_evidence`** / **`cited_urls`** taken from **tool results in this chat** — your own tools (including earlier messages) and worker results.{% if profile_has_survey_repo %} Curated digests orient only.{% endif %}

- **`cited_evidence`**: objects with either `evidence` for an exact evidence handle or `path`, plus optional `line`/`excerpt` from tool output — `read`/`grep` hits, `summarize` `anchors[]` or pack spans (`{"evidence":"summarize#N"}`, or `{path,line,excerpt}` from source windows/anchors/call_sites). Use `[]` only when nothing qualifies. Never use a `handle` key here — the closeout decoder rejects unknown keys and drops every citation in the fence with them.
{% if web_search_enabled %}- **`cited_urls`**: URLs from your or a worker's web-search/`fetch_url` output — use `[]` otherwise.
{% else %}- **`cited_urls`**: always `[]`.
{% endif %}

`Code: SYNTH_HANDLE_NOT_IN_LEGS`, `Code: SYNTH_CITATION_UNVERIFIABLE`, `Code: SYNTH_URL_NOT_OBSERVED`

This surface cannot dispatch scouts — cite tool results you already have, or omit the claim.
