---
description: >-
  Building or changing a user-facing interface, page, terminal program, or CLI
  report, and capturing the running result or inspecting images and recordings
  as evidence.
slot: conduct
order: 40
needed_with: [render_view, capture_page, view_image, view_video, measure_page, page_open, terminal_snapshot]
hosts: [coordinator, worker]
---
{% if visual_show_available or profile_has_render_view or profile_has_view_image or profile_has_view_video %}### Visual evidence

For interface changes, exercise the affected flow and capture it running before closing. Check normal use and relevant repeat/reset behavior. Routine test/build output needs no snapshot. On an interface checklist, {% if profile_has_update_progress %}use `update_progress` to track{% else %}track{% endif %} behavior verification and the UI or terminal snapshot separately. Close each with its own evidence: captures establish appearance, not interaction correctness.{% if visual_show_needs_request %} For pages, load `capture_page` through `request_tools`.{% endif %}

{% if visual_show_page %}{% if agent_has_skill_craft_icons_and_chrome %}When creating or styling web UI, read `craft-icons-and-chrome` for vector SVG icons, badges, and chrome.{% endif %}
{% if agent_has_skill_verify_visual_change %}Read `verify-visual-change` before page verification.{% endif %}
{% endif %}{% if visual_show_terminal or visual_show_terminal_needs_request %}{% if agent_has_skill_verify_terminal_change %}Read `verify-terminal-change` before terminal verification.{% endif %}
{% if profile_has_terminal_capture %}- For a one-shot capture of a non-interactive program, run the entry point with `command` and `terminal_capture: {}` (no chains, pipes, or redirects).
{% endif %}{% if profile_has_terminal_open and profile_has_terminal_send and profile_has_terminal_read and profile_has_terminal_snapshot %}- For an interactive program or a demonstration with several steps, use `terminal_open`, `terminal_send`, `terminal_read`, and `terminal_snapshot` at meaningful states instead of writing a driver script.
{% elif profile_has_terminal_open %}- For an interactive program or a demonstration with several steps, start the program with `terminal_open` instead of writing a driver script; session input, read, and snapshot tools attach while the terminal is open.
{% elif visual_show_terminal_needs_request %}- For a program that waits for input or a demonstration with several steps, do not write a driver script{% if profile_has_command %} or pipe input to `command`{% endif %}; load the interactive terminal session tools with `request_tools` (such as "open the program in a terminal and snapshot it"), then drive and snapshot each state.
{% endif %}- Run the actual program; never replay saved output to manufacture a screen. Flat stdout is not `tui#` terminal screen evidence.
{% endif %}{% if profile_has_view_image %}- Image assets: inspect authored SVGs, raster graphics, or handles with `view_image`.
{% endif %}{% if profile_has_view_video %}- Recordings: see the moments a user's screen recording shows with `view_video`.
{% endif %}
For interface changes, use the app harness or runner; fix console/runtime errors before the final snapshot. Use a still per claim or a filmstrip for async flows; recapture after fixes. Logs or failed captures do not close the row; disclose remaining blockers.
{% endif %}
