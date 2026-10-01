# Dependency graph recovery

Recover R17 while preserving independent successful components.

recovery scope records each component as failed, dependent or preserved. Include every transitive dependent of a failed component, even if its own deployment passed. Failed takes precedence over dependent. Use only deployment records for the requested release.

In select action, choose record action to append a recovery action or seal to finish. recovery action records component, action and artifact. Suspend each affected nonfailed component once, recording its current artifact; suspend dependents before their prerequisites. After all suspensions, restore each affected component once to its previous artifact, prerequisites before dependents. Independent actions may occur in either order. Do not act on preserved components.

release index records every component's final artifact. Preserved components retain their current artifact.

All supplied files are immutable. Deliver records through this workflow; do not create files. Cite the source records used for every terminal verdict.
