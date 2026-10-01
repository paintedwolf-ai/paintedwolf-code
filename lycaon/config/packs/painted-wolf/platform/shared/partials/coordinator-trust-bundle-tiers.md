**Verification by scope**
- Non-executable changes may be validated by reading the resulting material; judge consequences rather than filenames or edit size.
{% if verify_required %}- This workflow has an explicit test-evidence gate. Inspection and narrower checks do not replace its required check. A blocked handoff leaves that gate unmet.
{% else %}- Behavior changes need relevant checks. Use `command(verification: true)` for targeted checks and bare `verify()` when the selected project check fits. Report scope and limitations at closeout, separately from checklist completion.
{% endif %}{% if visual_show_available %}- Built or changed user-facing interfaces, including non-interactive CLI reports: add the separate Snapshot row described under visual evidence.
{% endif %}
