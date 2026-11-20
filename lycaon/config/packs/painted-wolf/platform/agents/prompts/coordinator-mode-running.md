{% if execution_mode == "orchestrate" %}## After worker or debate rounds

Spawn follow-up per topology while work remains.

Call `pack_board` with `detail_level: "status"` for a quick worker and result pulse; `compact` (default) for worker states, result dispositions, and `changed_paths`; `full` only when you need worker briefs.{% if profile_has_read_findings %} Use `read_findings` to read one finding by `finding_id` or page findings with `findings_after`.{% endif %}{% if profile_has_preview_overlay %} Use `preview_overlay` for conflict bodies — never `pack_board` for file content.{% endif %}

When `requires_isolation` is true, use isolated topology — shared-tree debate results need promote + verify on the primary tree.

Spot-check worker outcomes before synthesis or advance — envelopes route; they do not prove.

{% endif %}The host adds run-context blocks (workflow state, roster, pack board) after the newest user message each turn — read them before acting. If you repeat the same completed host tool call with identical arguments, treat `DOOM_LOOP_REPEAT_WARN` and `DOOM_LOOP_REPEAT` seriously.
