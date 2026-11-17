# SKILL.md field table

| Field | Required | Notes |
|---|---|---|
| `name` | Yes | 1–64 characters; `a-z`, `0-9`, hyphens; no leading, trailing, or consecutive hyphens; matches the folder name |
| `description` | Yes | Up to 1024 characters; bundled skills use one sentence of at most 180 characters, what and when only. It is the text the host ranks |
| `license` | No | License name or bundled file reference |
| `compatibility` | No | Up to 500 characters of environment needs |
| `metadata` | No | String keys to string values. `host_resources` is a comma-separated list of required host-resource entries; a `\|` inside one entry lists interchangeable alternatives (`docker\|podman`), and each entry needs one available id for the host to advertise or load the skill. `paintedwolf.any_of_tools` is a comma-separated list of tools of which the reader needs only one |
| `allowed-tools` | No | Read and shown, never honored |

## Size limits

| Part | Limit |
|---|---|
| `SKILL.md` body | 4 KiB for bundled skills; keep project skills there too, since a preloaded body rides every call of its turn |
| One reference or asset | 8 KiB for bundled skills |
| Whole `SKILL.md` | 64 KiB host limit; larger files are not loaded |
| Listed resources | 32 per skill |

## Resources

Optional directories: `references/`, `assets/`. The host reads `SKILL.md` at load time and lists the other files without reading them.{% if profile_has_skills_read %} The agent opens a file with `skills_read`, passing the skill name as `need` and the relative path as `resource`.{% endif %} Bundled skills ship no `scripts/`.

## Pack templates

Pack skill bodies use the isolated pongo profile, so includes are unavailable. The host renders the body and every `references/*.md` file with the caller's vars; invalid templates reject with `SKILL_TEMPLATE_INVALID`. Project skills are not templated.

The vars describe the caller:

- Sizing: `max_in_flight`, `max_read_workers`, `max_write_workers`, `hunt_wave_workers`.
- Reach: `profile_has_<tool>` is true when the caller's surface offers that tool; `profile_command_mutation`, `profile_command_external_exec`, `profile_command_network_egress`, `profile_has_write_tools`, and `profile_can_request.<tool>` describe its command effects, write scope, and tool requests.
- Posture: `is_coordinator`, `on_worker_branch`, `has_git`, `root_count`.
- Configuration: `configuration.<property>` resolves the caller pack's effective configuration values.
- Declared reader vocabulary (`platform/host/reader-facts.yaml`, at `lycaon/config/packs/painted-wolf/platform/host/reader-facts.yaml` in a source checkout): `agent_tools`, `caps.vision`, `card_{edit_deferred,may_dispatch,may_edit,may_run,only_tools}`, `command_on_floor`, `command_output_files`, `execution_mode`, `has_git`, `is_coordinator`, `lane_s_sync_tools`, `more_tools_loadable`, `native_edit_limit_mib`, `on_worker_branch`, `profile_command_{external_exec,mutation,network_egress}`, `profile_has_{code_rewrite,managed_secrets,page_controls,retrieval,scan_drilldown,summarize,survey_repo,terminal_capture,write_tools}`, `profile_mutation_capable`, `progress_gated_tools`, `read_globs`, `recall_may_widen`, `reject_codes`, `requestable_capabilities`, `root_count`, `runner_{direct_ip,local_listen,loopback_connect,socket,write_root}_tools`, `surface_card_label`, `surface_card_rule`, `surface_id`, `surface_publishes_text`, `tool_check_{scan_drilldown,survey,verify,write}_tools`, `tool_profile`, `visual_review_cadence`, `visual_show_{available,needs_request,page,terminal,terminal_needs_request}`, `web_search_enabled`, `write_globs`, and the families `profile_has_<tool>`, `profile_can_request.<tool>`, `mints.<kind>`, `receipt_kind.<tool>`, and `agent_has_skill_<skill>`.

A skill body is rendered for the reader at the moment it is read and then stays in the transcript. Gate a passage on a reader fact only when the passage would be wrong without it, and state procedure conditionally rather than asserting a capability. A template that reads an undeclared reader name still renders, with that name false, and logs a note naming the template and variable (`prompts: unknown reader fact <var> in <ref>`); occurrence data from a template binding cannot redefine a reader fact. For standing prompt units, `attaches` in front matter names tools or the reach marks `command_mutation`, `command_external_exec`, and `command_network_egress`; the unit renders while the reader holds any one, and attaching a unit to the reach it needs is preferred over wrapping its body in a guard.

Guard an optional tool mention with {% verbatim %}{% if profile_has_<tool> %}…{% endif %}{% endverbatim %}, so a reader whose surface lacks the tool never sees an unreachable call. Preview a render with `pw prompts render --skill <name> [--resource <path>] [--agent ID] [--surface ID] --check`.

The renderer has no whitespace-trim markers, so guard discipline is structural: a fully guarded line renders empty when its guard is false, and a standalone {% verbatim %}{% if %}{% endverbatim %} / {% verbatim %}{% else %}{% endverbatim %} / {% verbatim %}{% endif %}{% endverbatim %} line always renders one blank line. Put the guard inline on the line it guards, keep every branch's lines non-empty, and never place a blank source line next to a standalone tag line. The rendered-prose checks flag numbering gaps, runs of blank lines, and blank lines after table separators, so `--check` catches a broken branch.

Escape literal {% verbatim %}{{{% endverbatim %} / {% verbatim %}{%{% endverbatim %} sequences with a `verbatim` … `endverbatim` block. Other references are returned as written.
