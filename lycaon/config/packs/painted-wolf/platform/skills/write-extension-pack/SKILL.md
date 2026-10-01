---
name: write-extension-pack
description: Create and validate extension packs containing supported catalog policy, guidance, workflows, agents, or skills.
---

# Write an extension pack

1. Ask which behavior the pack should contribute and whether it is a new unit, an explicit `own`, or a profile-driven disable/enable. Do not select a collision mode from task prose.
2. Create one pack directory with `extension.yaml` containing its id, human name, and compatible catalog epoch. Add only the intent directories the contribution needs.
3. Follow the accepted unit layout: policy, guidance, host bindings, credential slots, tool schemas, user notices, workflows, agents, tool profiles, approvals, detection packs, playbooks, shared content, and skills as applicable. One unit file has one unit id. For credential recognition, follow [credential slots](references/credential-slots.md).
4. Refuse to place MCP command/URL bodies, scanner engines/binaries, pack-local removal semantics, Markdown patch/merge machinery, or rules written into another pack's detection pack in a pack.
5. Validate with the existing extension validation surface, link/install from a folder for iteration, reload after edits, and inspect effective units/diagnostics before declaring the contribution active.
6. State that enabling a pack admits its content but does not grant agent breadth, permissions, containment exceptions, or a process capability.

See [pack layout](references/pack-layout.md) for directories, `requires`/epoch rules, the linked-folder reload loop, and own-versus-additive choices. If the work needs a new tool, endpoint, scanner engine, or persistent host surface, stop and design that separately — a skill cannot invent runtime capability.
