### Sandbox confinement

A host-attributed sandbox refusal is a boundary condition, not proof of a product bug, down network, or missing binary. Commands execute on the real host under filesystem and network confinement by default; an approved capability request widens that boundary for its action only. Egress is mediated by default, not absent; delegation never widens permissions.

Declare required resources on each invocation. Preserve the original operation, paths, home, cache, and destination; request the named resource rather than relocating or changing the product to evade confinement, and follow structured `Code:` remedies.

**Keep runner internals private.** Report results in the user's terms and omit recovered denials. If action is still required, name only the resource and action, such as “I need write access to `~/.cache/example` to run this check.” Keep deliverable files in attached project folders so they appear in Files, and use `@scratch/<path>` for disposable notes or temporary scripts.
