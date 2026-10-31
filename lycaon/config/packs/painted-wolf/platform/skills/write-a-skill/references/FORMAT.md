# SKILL.md field table

| Field | Required | Notes |
|---|---|---|
| `name` | Yes | 1–64 characters; `a-z`, `0-9`, hyphens; no leading, trailing, or consecutive hyphens; matches the folder name |
| `description` | Yes | Up to 1024 characters; bundled skills use one sentence of at most 180 characters, what and when only. It is the text the host ranks |
| `license` | No | License name or bundled file reference |
| `compatibility` | No | Up to 500 characters of environment needs |
| `metadata` | No | String keys to string values. `host_resources` is a comma-separated list of required host-resource entries; a `\|` inside one entry lists interchangeable alternatives (`docker\|podman`), and each entry needs one available id for the host to advertise or load the skill. `paintedwolf.template_resources` is a `\|`-separated list of references rendered like the body |
| `optional_tools` | No | Tools the body mentions that some agents offered the skill may not hold |
| `allowed-tools` | No | Read and shown, never honored |

## Size limits

| Part | Limit |
|---|---|
| `SKILL.md` body | 4 KiB for bundled skills; keep project skills there too, since a preloaded body rides every call of its turn |
| One reference or asset | 8 KiB for bundled skills |
| Whole `SKILL.md` | 64 KiB host limit; larger files are not loaded |
| Listed resources | 32 per skill |

## Resources

Optional directories: `references/`, `assets/`. The host reads `SKILL.md` at load time and lists the other files without reading them. The agent opens a file with `skills_read`, passing the skill name as `need` and the relative path as `resource`. Bundled skills ship no `scripts/`.

## Pack templates

Pack skill bodies use the isolated pongo profile, so includes are unavailable. At `skills_read` the host renders the body and any `paintedwolf.template_resources` with policy vars (`max_in_flight`, `worker_tool_budget_default`, `worker_tool_budget_min` / `_max`, `hunt_wave_workers`, `throwaway_tool_loops_min` / `_max`, `deep_tool_loops_hint`). Invalid pack templates reject with `SKILL_TEMPLATE_INVALID`. Project skills are not templated.

Escape literal `{{` / `{%` with `{% verbatim %}…{% endverbatim %}`. Other references are returned as written.
