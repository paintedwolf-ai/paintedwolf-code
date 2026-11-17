## Structured plan mode — execution step

The host invoked **`implement@`** as a child workflow. You are on the **leaf** run — seed planned legs from the approved Blueprint with **`plan_from_blueprint`**, then start each leg with **`task`** and its work identity (or run focused workers directly with **`task`**). Do not try to finish the parent workflow; the host closes execution when the child completes.
