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
2. Add `SKILL.md` with YAML frontmatter. `name` matches the folder. `description` is one sentence of at most 180 characters saying what the skill handles and when, using the nouns a user would type. The host ranks skills by `Skill: <name>` plus this description, so generic verbs ("run", "debug", "create") match everything; name the artifacts and tools. Put `Do not` guidance and workflow coaching in the body.
3. Keep the body under 4 KiB. The host preloads the best-ranked skill at turn start or at the turn's first loadable tool call, and the body then rides every model call of that turn beside the standing prompt, including on small local models.
4. Move detail into `references/` files of at most 8 KiB each, linked from the body by relative path. The agent opens one with `skills_read` (`need` = the skill name, `resource` = the path) only when it needs it. Optional `assets/` hold specimens.
5. Field table, host bounds, and template rules: [FORMAT.md](references/FORMAT.md).

## Body standard

The body is executed by whichever model is running, including a small one. Write for the weakest reader you ship to.

1. **Open with the entry check.** Give the observation that proves the skill applies and what to do when it fails.
2. **One action per numbered step.** Name the tool or the exact command the step runs. Shell work is a `command` program (`rg -n 'Pattern' src/`, `git log --oneline -n 10`, `curl -sS -i <url>`), not a separate tool per utility. Native `git apply`, `patch -p1` and `sed -i` retain review, revision capture and recovery.
3. **Name the artifact each step produces.** If it is a table, give its columns; if it is a decision, give the options.
4. **Show one worked example**: a filled-in row, command, or minimal snippet. Long examples belong in `references/`.
5. **Bound every search** with a count, a depth, or the condition that ends it.
6. **Give the failure branch**: retry, narrow, escalate, or stop and report. Branch on structured rejection `Code:` values, not error prose.
7. **Close with the report shape**: the fields the caller needs back.

When a step is a judgment, give the criteria and the options: "choose the narrowest scope that still reproduces" is a decision; "be careful about scope" is not.

Do not enumerate `agent_type` ids in a body. The host injects the live spawn roster with every id and capability; a hand-written list drifts when agents change.

A skill grants nothing. Its commands still go through the ordinary capability, approval, and confinement checks.

## Check before finishing

- `name`, folder, and links resolve; every referenced file exists.
- The description reads as a ranking card: concrete nouns, no generic verb list.
- Body and each reference fit their size limits (`wc -c SKILL.md references/*` in `command`).
- Read the skill back with `skills_read` and confirm it loads without a template or metadata error.
