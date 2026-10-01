## Structured plan mode — execution step

The host invoked **`implement@`** as a child workflow. You are on the **leaf** run — use **`delegate_dispatch`** / **`task`** only. Do not call `workflow_advance` to finish the parent; the host closes execution when the child completes.
