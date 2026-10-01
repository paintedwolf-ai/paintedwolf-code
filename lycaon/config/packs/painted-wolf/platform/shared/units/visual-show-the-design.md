---
description: >-
  Building or changing a user-facing interface, page, terminal program, or CLI
  report, and capturing the running result or inspecting images and recordings
  as evidence.
slot: conduct
order: 40
attaches: [render_view, capture_page, view_image, view_video, measure_page, page_open, terminal_snapshot]
hosts: [coordinator, worker]
---
### Visual evidence

When building or changing a user-facing interface (pages, interactive terminals, CLI reports) or when requested, capture the running result before closing. Routine test/build output alone needs no snapshot. If you author `## Progress`, add a separate{% if visual_show_page %} `Snapshot the running UI`{% endif %}{% if visual_show_page and visual_show_terminal %} or{% endif %}{% if visual_show_terminal %} `Snapshot the terminal`{% endif %} row, not a Verify label.

{% if visual_show_page %}{% if agent_has_skill_craft_icons_and_chrome %}When creating or styling web UI, read `craft-icons-and-chrome` for vector SVG icons, badges, and chrome.{% endif %}
{% if agent_has_skill_verify_visual_change %}Read `verify-visual-change` before page verification.{% endif %}
{% endif %}{% if visual_show_terminal %}{% if agent_has_skill_verify_terminal_change %}Read `verify-terminal-change` before terminal verification.{% endif %}
{% if profile_has_terminal_capture %}- One-shot terminal proof: `command` with `terminal_capture: {}` and the exact process's `capability_request` in the same call. One program only: no command chains, pipes, or redirects.
{% endif %}{% if profile_has_terminal_open and profile_has_terminal_snapshot %}- Continuing interaction: `terminal_open`, `terminal_send`, `terminal_read`, and `terminal_snapshot` at material states.
{% endif %}- Run the actual program; never replay saved output to manufacture a screen. Flat stdout is not `surface_snapshot{tui}` evidence.
{% endif %}{% if profile_has_view_image %}- Image assets: inspect authored SVGs, raster graphics, or handles with `view_image`.
{% endif %}{% if profile_has_view_video %}- Recordings: see the moments a user's screen recording shows with `view_video`.
{% endif %}
After edits, exercise the flow via app harness or runner and capture: fix console/runtime errors before final snapshot. Use a still per claim or a filmstrip for async flows; recapture after fixes. Logs or failed captures do not close the row; disclose remaining blockers.
