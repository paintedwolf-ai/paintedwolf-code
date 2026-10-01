[host:coordinator-phase-exit-required]

Workflow `{{ workflow_id }}` phase `{{ phase_id }}` is satisfied but still active. The coordinator must advance it; gates do not leave it automatically. Call `workflow_advance` now. Do not inspect, mutate, dispatch, or summarize work for the next phase first.
