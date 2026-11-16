---
name: write-a-skill
description: Author or edit a SKILL.md procedure for this host, with its frontmatter, description, references, and size limits.
license: Apache-2.0
compatibility: Requires a project with a skills directory the host scans
metadata:
  author: painted-wolf
---

# Write a skill

## Mechanics

1. Create a folder named for the skill id: lowercase letters, numbers, and single hyphens.
2. Add `SKILL.md` with YAML frontmatter. `name` matches the folder. `description` is one sentence of at most 180 characters saying what the skill handles and when, using nouns a user would type. The host ranks skills by `Skill: <name>` plus this description, so generic verbs ("run", "debug", "create") match everything; name the artifacts and tools. Put `Do not` guidance and workflow coaching in the body.
3. Keep the body under 4 KiB. The host preloads the top-ranked skill at turn start or first loadable tool call, and the body rides every call of that turn.
4. Move detail into `references/` files of at most 8 KiB each, linked by relative path.{% if profile_has_skills_read %} The agent opens one with `skills_read` (`need` = skill name, `resource` = path) when needed.{% endif %} Optional `assets/` hold specimens.
5. Field table, host bounds, and template rules: [FORMAT.md](references/FORMAT.md).

## Body standard

The body is executed by whichever model is running, including a small one. Write for the weakest reader you ship to.

1. **Open with the entry check.** Give the observation that proves the skill applies and what to do when it fails.
2. **One action per numbered step.** Name the tool or exact command the step runs. Shell work is a `command` program (`rg -n 'Pattern' src/`, `git log --oneline -n 10`, `curl -sS -i <url>`), not a separate tool per utility. Native `git apply`, `patch -p1` and `sed -i`{% if profile_command_mutation %} retain review, revision capture and recovery{% else %} are mutation programs and need a mutation-capable command scope{% endif %}.
3. **Name the artifact each step produces.** If it is a table, give its columns; if it is a decision, give the options.
4. **Show one worked example**: a filled-in row, command, or minimal snippet. Long examples belong in `references/`.
5. **Bound every search** with a count, depth, or stop condition.
6. **Give the failure branch**: retry, narrow, escalate, or stop and report. Branch on structured rejection `Code:` values, not error prose.
7. **Close with the report shape**: the fields the caller needs back.

When a step is a judgment, give the criteria and options. Do not enumerate `agent_type` ids in a body; the host injects the live spawn roster. A skill grants nothing; its commands still go through capability, approval, and confinement checks.

A skill body is rendered for the reader when read and stays in the transcript. Gate a passage on a reader fact only when it would be wrong without it, and state procedure conditionally rather than asserting a capability. Reader facts are the names in `platform/host/reader-facts.yaml` (or [FORMAT.md](references/FORMAT.md) in an installed build). An undeclared reader name renders false and logs a note naming the template and variable; binding occurrence data cannot redefine a reader fact.

## Check before finishing

- `name`, folder, and links resolve; every referenced file exists.
- The description reads as a ranking card: concrete nouns, no generic verb list.
- Body and each reference fit their size limits{% if profile_has_command %} (`wc -c SKILL.md references/*` in `command`){% endif %}.
- Render what you wrote: `pw prompts render --skill <name> --check` for the body, plus `--resource <path>` per reference, and fix every violation.
{% if profile_has_skills_read %}- Read the skill back with `skills_read` and confirm it loads without a template or metadata error.{% endif %}
- Guard a tool mention the reader may not hold in {% verbatim %}{% if profile_has_<tool> %}{% endverbatim %} … {% verbatim %}{% endif %}{% endverbatim %}.
- Guard what `command` does: file writes need `profile_command_mutation`, external programs `profile_command_external_exec`, and network requests such as `curl` `profile_command_network_egress` (units attach to `command_mutation`, `command_external_exec`, or `command_network_egress` in `attaches`).
