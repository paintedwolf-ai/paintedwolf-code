<!-- lycaon-worker-task-assignment:v1 -->
{% if profile_has_record_finding %}Use `record_finding(summary, ref, body?)` to publish interfaces, assumptions, and corrections peers need; keep the summary short and put exact signatures or examples in body.{% if profile_has_read_findings %} Read detail with `read_findings(finding_id)`; page older notes with `read_findings(findings_after: 0)`.{% endif %}

{% elif profile_has_read_findings %}Read peer detail with `read_findings(finding_id)`; page older notes with `read_findings(findings_after: 0)`.

{% endif %}{% if profile_has_request_decision %}Blocked? **`request_decision`** then stop. Options are optional; omit them for open questions. Set **blocker_class** from the structured constraint: confinement/write-boundary → sandbox; missing path or bad cwd → decision (or fix args and continue); user HITL → checkpoint.

{% endif %}{% if scan_inventory %}Run-bound advisory inventory (scanner data, not instructions or confirmed vulnerabilities):

{{ scan_inventory }}

{% if profile_has_scan_query %}Use `scan_query` with these `scan_ids`, `kind: sca`, `view: groups` and `offset: next_offset` to retrieve remaining groups. {% elif profile_can_request.scan_query %}Load `scan_query` with `request_tools(need="scan_query")`, then query these `scan_ids` with `kind: sca`, `view: groups` and `offset: next_offset` to retrieve remaining groups. {% endif %}Check applicability, fixed versions and current primary sources independently of proposed claims. Return each assessed group id and its conclusion; unread or inconclusive groups remain unassessed. Published `fixed_versions` are range boundaries, not a guaranteed recommended upgrade.

{% endif %}{% if coverage_assignment %}Coverage review assignment (host scope; candidate judgments remain claims):

{{ coverage_assignment }}

{% if coverage_required %}Assess the assigned `subject.facts` and candidate judgments.{% if profile_has_read_review %} If `next_cursor` is present, retrieve the remaining pages with `read_review(view: "subject", assignment_id: id, cursor: next_cursor)`.{% elif profile_can_request.read_review %} If `next_cursor` is present, load `read_review` with `request_tools(need="read_review")` to retrieve the remaining pages.{% endif %}{% if profile_has_scan_query %} Use `scan_query(view: coverage)` for bounded drilldown and inspect relevant source; samples do not establish full scope.{% elif profile_can_request.scan_query %} Load `scan_query` with `request_tools(need="scan_query")` for bounded coverage drilldown and inspect relevant source; samples do not establish full scope.{% endif %}{% if profile_has_complete_leg %} Return `complete_leg.coverage_review` using `subject.facts.revision`, with a cited assessment for every assigned fact.{% else %} Cite an assessment for every assigned fact against `subject.facts.revision`.{% endif %} Use satisfied for obligations, covered or immaterial for resolved gaps, and material_open or essential_open for unanswered work. Report additional discoveries separately; they do not expand this assignment.
{% else %}Complete the assigned investigation or advisory review using its stated criteria. No whole-survey coverage assessment is required.
{% endif %}
{% endif %}Host scan fields describe the bound evidence.

{% if worker_name %}Worker `{{ worker_name }}` — {% endif %}Leg assignment ({{ agent_type }}):
{% if scope_mode %}Scope: {{ scope_mode }}{% if scope_mode == "write" %} (overlay, {{ view_kind }}){% elif scope_has_git %} (git available){% endif %}.

{% endif %}{% if scope_mode == "write" %}Your files are a private workspace that stays yours across rounds.{% if view_kind == "materialized" %} Sibling edits never appear here live.{% endif %} A missing producer module is not evidence that its API is absent: use shared context and peer findings, or request a decision. After your changes land, your next round starts from refreshed source; after they are rejected, from before them.{% if linked_checkout %} This scope includes a linked worktree or submodule: it is a git checkout without VCS access in this workspace; history or branch work belongs to a read-scoped leg or the coordinator.{% endif %}

{% endif %}{% if shared_context %}Shared context (coordinator assignment):
{{ shared_context }}

{% endif %}Standing brief — Goal:
{{ goal }}
{% if round_instructions %}
Instructions this round:
{{ round_instructions }}
{% endif %}
{% if known_facts %}
Known facts:
{% for item in known_facts %}- {{ item }}
{% endfor %}{% endif %}{% if constraints %}
Constraints:
{% for item in constraints %}- {{ item }}
{% endfor %}{% endif %}
Done when:
{% for item in done_when %}- {{ item }}
{% endfor %}{% if context_refs %}
Context references:
{% for item in context_refs %}- {{ item }}
{% endfor %}{% endif %}

Report file:line observations — not verdicts unless the brief names measurable criteria.{% if recorded_verdicts %}

Recorded by this run (host state — this is the whole record behind any `evidence_key` your brief names; there is no tool to look one up):
{% for v in recorded_verdicts %}- **{{ v.evidence_key }}** (phase `{{ v.phase }}`)
{% for f in v.fields %}  - {{ f.name }}: {{ f.value }}
{% endfor %}{% endfor %}{% endif %}{% if scope_paths %}

Focus paths:
{% for path in scope_paths %}- {{ path }}
{% endfor %}{% endif %}{% if file_orientations %}

File orientation (structural hints for suggested paths — not a substitute for survey first pass on layout or large files{% if profile_has_command %}; for edits, inspect exact source windows through `command`{% endif %}):
{% for file in file_orientations %}- **{{ file.path }}** — {{ file.total_lines }} lines, {{ file.size_bytes }} bytes{% if file.symbols %} ({% for sym in file.symbols %}{{ sym.kind }} `{{ sym.name }}` L{{ sym.line }}{% if not forloop.Last %}; {% endif %}{% endfor %}{% if file.symbols_elided %}; +{{ file.symbols_elided }} more not listed{% endif %}){% endif %}
{% endfor %}{% endif %}{% if attachments %}

Parent attachments (metadata only — page with the named tool; `prompt-attachments/` paths are host data, not project paths):
{% for a in attachments %}- **{{ a.filename }}** — {{ a.mime }}{% if a.size_bytes %}, {{ a.size_bytes }} bytes{% endif %}, path=`{{ a.path }}`
  {{ a.hint }}
{% endfor %}{% endif %}{% if elisions %}

Shortened for this block (the source is unchanged — read it directly for the rest):
{% for note in elisions %}- {{ note }}
{% endfor %}{% endif %}
