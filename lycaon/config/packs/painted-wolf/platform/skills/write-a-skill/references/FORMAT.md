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

Optional directories: `references/`, `assets/`. The host reads `SKILL.md` at load time and lists the other files without reading them. {% if profile_has_skills_read %}The agent opens a file with `skills_read`, passing the skill name as `need` and the relative path as `resource`.{% endif %} Bundled skills ship no `scripts/`.

## Pack templates

Pack skill bodies use the isolated pongo profile, so includes are unavailable. The host renders the body and every `references/*.md` file with the caller's vars; invalid templates reject with `SKILL_TEMPLATE_INVALID`. Project skills are not templated.

The vars describe the caller:

- Sizing: `max_in_flight`, `worker_tool_budget_default`, `worker_tool_budget_min` / `_max`, `hunt_wave_workers`, `throwaway_tool_loops_min` / `_max`, `deep_tool_loops_hint`.
- Reach: `profile_has_<tool>` is true when the caller's surface offers that tool; `profile_command_mutation`, `profile_has_write_tools`, and `profile_can_request` describe its command effects, write scope, and tool requests.
- Posture: `is_coordinator`, `on_worker_branch`, `has_git`, `root_count`.
- Configuration: `configuration.<property>` resolves the caller pack's effective configuration values.

Guard an optional tool mention with {% verbatim %}{% if profile_has_<tool> %}…{% endif %}{% endverbatim %}, so a reader whose surface lacks the tool never sees an unreachable call. Preview a render with `pw prompts render --skill <name> [--resource <path>] [--agent ID] [--surface ID] --check`.

Escape literal {% verbatim %}{{{% endverbatim %} / {% verbatim %}{%{% endverbatim %} sequences with a `verbatim` … `endverbatim` block. Other references are returned as written.
