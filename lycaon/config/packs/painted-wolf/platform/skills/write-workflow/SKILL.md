---
name: write-workflow
description: Author and validate workflow phases, gates, human decisions, and durable orchestration.
---

# Write a project workflow

1. Confirm the workflow's human decision points and durable outcome before writing a manifest. A workflow is for machine-visible phases and gates, not a freeform checklist.
2. Create exactly `{workspace}/${overlay_dir}/workflows/<workflow_id>/workflow.yaml`; reject a bare workflow YAML in the parent directory.
3. Use only the generic gate kit for project workflows. Do not copy a bundled plan, options, bugbash, or security domain gate into project content.
4. Declare each phase's coordinator surface, surface template, and mode references explicitly; satisfy the host leaveability requirements and use `orchestration_complete` for a terminal phase.
5. Validate through the existing project workflow validator. Branch on structured diagnostic `code` and fix the manifest rather than parsing a prose message.
6. State that validation proves loadability and host leaveability, not model behavior, evidence quality, or live timing. Add harness/e2e coverage where the workflow changes a user-facing path.

See [workflow contract](references/workflow-contract.md) for allowed gates, leaveability, and validator commands. If the work needs a new gate engine feature, host tool, or bundled domain leaf in project content, stop and design that surface separately.
