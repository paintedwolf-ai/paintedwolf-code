---
name: write-a-skill
description: Create or edit an Agent Skills procedure and validate its metadata, resources, and activation cues.
license: Apache-2.0
compatibility: Requires a project with a skills directory the host scans
metadata:
  author: painted-wolf
---

# Write a skill

## Mechanics

1. Create a folder whose name is the skill id: lowercase letters, numbers, and single hyphens only.
2. Add `SKILL.md` with YAML frontmatter. `name` matches the folder. `description` is one clear sentence covering what + when in ≤180 characters. User-provided descriptions stay ambient; stock descriptions stay ambient when an agent pins them, and all descriptions remain discoverable through the catalog. Put `Do not` and workflow coaching in the body.
3. Write the body to the standard below. Bodies are read on demand by `skills_read` and cost nothing until opened, so spend words on precision rather than brevity.
4. Optional folders: `scripts/`, `references/`, `assets/`. Paths in the body are relative to this skill's directory.
5. See [the field table](references/FORMAT.md) for every frontmatter field, host bound, and pack-template note.

## Body standard

The body is executed by whichever model is running, including a small one. Write for the weakest reader you ship to.

1. **Open with the entry check.** Give the one observation that proves the skill applies, and what to do when it fails. A reader who cannot confirm the precondition should leave rather than improvise.
2. **One action per numbered step.** Name the tool, command, or file the step operates on. "Call `scan_summary`" and "write a table with columns X, Y, Z" are steps. "Consider the failure modes" is a topic.
3. **Name the artifact each step produces.** A step producing nothing observable cannot be checked, skipped, or resumed. If the artifact is a table, give its columns; if it is a decision, give the options.
4. **Show one worked example.** A single filled-in row, command, or minimal snippet carries more than the paragraph describing it. Long examples belong in `references/`.
5. **Bound every search.** Any step that can repeat needs a stopping rule — a count, a depth, or the condition that ends it. "Until minimal" is not a stopping rule.
6. **Give the failure branch.** For each step that can fail, say whether to retry, narrow, escalate, or stop and report.
7. **Close with the report shape.** List the fields the caller needs back, so a dispatcher can check the result against the assignment.

Judgment skills need this more than procedural ones, not less. When a step is genuinely a decision, give the criteria and the options rather than the adjective: "choose the narrowest scope that still reproduces" is a decision; "be careful about scope" is not.

Do not enumerate `agent_type` ids in a body. The host injects the live spawn roster with every id, capability, and pool cap; a hand-written list drifts the moment an agent is added or renamed. `no_hardcoded_agent_ids_contract_test.go` enforces this.

A skill grants nothing by itself. Tools and commands still go through the ordinary capability, approval, and containment checks.
