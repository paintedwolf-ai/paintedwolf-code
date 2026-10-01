# Test workflow fixtures

Frozen snapshots of shipped inputs that wiring and security integration tests
depend on. Only kinds the harness can still point the runtime at live here.

Workflow manifests and topologies are **not** among them: both are provide units
resolved from the effective catalog, so a test that needs a different one stages
bundled config (`config/configtest`) or drops a project overlay at runtime —
there is no directory swap to aim at a snapshot.

When to update a fixture:

- A test asserts on a *new* runtime behavior that requires a new field in a
  fixture — update the matching fixture to match the assertion.
- A test starts to fail because the runtime now requires a manifest field
  that wasn't here when the snapshot was taken — bring the snapshot up to
  the new minimum shape.

Do **not** update fixtures just because the corresponding shipped manifest
changed. That defeats the decoupling.

Fixtures live at:

- `workflow-templates/` — `hotfix`, `clarify-then-implement`, `confirm-skip-research`
