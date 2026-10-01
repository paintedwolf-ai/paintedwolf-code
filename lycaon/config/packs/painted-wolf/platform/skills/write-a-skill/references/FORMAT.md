# SKILL.md field table

| Field | Required | Notes |
|---|---|---|
| `name` | Yes (spec) | 1–64 characters; `a-z`, `0-9`, hyphens; no leading/trailing or consecutive hyphens; should match the folder name |
| `description` | Yes | 1–1024 characters (spec); pack skills should use one clear sentence ≤180 characters for prompt-roster realism — what + when only; put `Do not` / workflow coaching in the body |
| `license` | No | License name or bundled file reference |
| `compatibility` | No | Up to 500 characters of environment needs |
| `metadata` | No | String keys to string values. `host_resources` is a comma-separated list of required host-resource entries; a `\|` between ids inside one entry lists interchangeable alternatives (`docker\|podman`), and each entry must have at least one available id for the host to advertise or load the skill |
| `allowed-tools` | No | Experimental; read and shown, never honored here |

Optional bundled directories: `scripts/`, `references/`, `assets/`. The host reads `SKILL.md` at load time and lists other regular files without reading their contents until the agent opens them.

## Pack templates

Pack skill bodies use the isolated pongo profile, so includes are unavailable. At `skills_read` the host renders them with policy vars (`max_in_flight`, `worker_tool_budget_default`, `worker_tool_budget_min` / `_max`, `hunt_wave_workers`, `throwaway_tool_loops_min` / `_max`, `deep_tool_loops_hint`). Invalid pack templates reject with `SKILL_TEMPLATE_INVALID`. Project skills are not templated.

Escape literal `{{` / `{%` with `{% verbatim %}…{% endverbatim %}`. Put host numbers in `SKILL.md`; `references/` via ordinary `read` are not rendered.
