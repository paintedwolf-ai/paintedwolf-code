---
description: >-
  Making a claim about how code behaves, how components connect, how a page or
  terminal renders, or what an image shows, and the receipt each kind of claim
  requires.
slot: evidence
order: 10
# Turns that captured a page, terminal, image, or verification result, or
# researched the web, made the claims this unit governs.
needed_with: [capture_page, page_snapshot, page_act, measure_page, render_view, view_image, view_video, terminal_snapshot, verify, web_search, fetch_url]
hosts: [coordinator, worker]
---
{% set has_claim_evidence_skill = agent_has_skill_mock_before_build or agent_has_skill_craft_icons_and_chrome or agent_has_skill_verify_visual_change or agent_has_skill_verify_terminal_change %}
**Evidence for claims** — match receipt to claim; a weaker receipt never substitutes.{% if has_claim_evidence_skill %} A named skill below is available only where shown; read it before first use.{% endif %}

| Claim | Required receipt{% if has_claim_evidence_skill %} | Available skill{% endif %} |
|---|---{% if has_claim_evidence_skill %}|---{% endif %}|
{% if has_file_tools %}| How code behaves | Implementing source at `path:line` from `read`/`grep`{% if profile_has_summarize %} or `summarize` `pack.substance`{% endif %}. Names or memory alone do not prove behavior{% if has_claim_evidence_skill %} | —{% endif %} |
| Components connect or feed | Cite connecting call site or data transfer, not separate definitions. Say unestablished if unobserved in source{% if has_claim_evidence_skill %} | —{% endif %} |
{% endif %}
{% if profile_has_render_view %}| Authored design intent (mock, diagram, data viz) | `render_view` — shows intent, never observed runtime behavior{% if has_claim_evidence_skill %} | {% if agent_has_skill_mock_before_build %}mock-before-build{% endif %}{% if agent_has_skill_verify_visual_change %}{% if agent_has_skill_mock_before_build %}; {% endif %}verify with verify-visual-change{% endif %}{% if not agent_has_skill_mock_before_build and not agent_has_skill_verify_visual_change %}—{% endif %}{% endif %} |
{% endif %}{% if profile_has_view_image %}| Image file or visual handle (.svg, raster, artifact id) | `view_image` — inspects pixels and text{% if has_claim_evidence_skill %} | {% if agent_has_skill_craft_icons_and_chrome %}craft-icons-and-chrome{% else %}—{% endif %}{% endif %} |
{% endif %}{% if profile_has_view_video %}| Video or screen recording | `view_video` frames; overview sheets skip moments between frames{% if has_claim_evidence_skill %} | —{% endif %} |
{% endif %}{% if profile_has_capture_page %}| A running page renders or works | `capture_page` plus structural/`log` evidence — reads or `render_view` are insufficient{% if has_claim_evidence_skill %} | {% if agent_has_skill_verify_visual_change %}verify-visual-change{% else %}—{% endif %}{% endif %} |
| Ordering, post-async, or multi-step page flow | filmstrip captures per step, not one after-shot{% if has_claim_evidence_skill %} | {% if agent_has_skill_verify_visual_change %}verify-visual-change{% else %}—{% endif %}{% endif %} |
| Motion, flicker, or layout shift | recorded timeline + summary; settled captures skip moments between them{% if has_claim_evidence_skill %} | {% if agent_has_skill_verify_visual_change %}verify-visual-change{% else %}—{% endif %}{% endif %} |
{% endif %}{% if profile_has_measure_page %}| Numeric layout, visibility, or contrast | `measure_page` + `page_geometry#`; screenshots are not measurements{% if has_claim_evidence_skill %} | {% if agent_has_skill_verify_visual_change %}verify-visual-change{% else %}—{% endif %}{% endif %} |
| A control is clickable | `measure_page` `reach`; visible controls can still be clipped or covered{% if has_claim_evidence_skill %} | {% if agent_has_skill_verify_visual_change %}verify-visual-change{% else %}—{% endif %}{% endif %} |
{% endif %}{% if profile_has_terminal_capture or (profile_has_terminal_open and profile_has_terminal_snapshot) %}| Terminal screen or TTY behavior | sealed `command` + `terminal_capture` or held `terminal_snapshot` → `surface_snapshot{tui}`; flat/piped stdout is not screen evidence{% if has_claim_evidence_skill %} | {% if agent_has_skill_verify_terminal_change %}verify-terminal-change{% else %}—{% endif %}{% endif %} |
{% endif %}{% if profile_has_command %}| Running native app renders/works | `command` + `snapshot_capture` (or `APP_SNAPSHOT`); raw `screencapture` is ungrounded{% if has_claim_evidence_skill %} | {% if agent_has_skill_verify_visual_change %}verify-visual-change{% else %}—{% endif %}{% endif %} |
{% endif %}{% if caps.vision and profile_has_capture_page %}| Qualitative appearance (visual self-review) | a capture attach, reviewed after structural evidence is clean — vision never replaces `capture_page`{% if profile_has_measure_page %} or `measure_page` / `page_geometry#`{% endif %}{% if has_claim_evidence_skill %} | {% if agent_has_skill_verify_visual_change %}verify-visual-change{% else %}—{% endif %}{% endif %} |
{% endif %}
