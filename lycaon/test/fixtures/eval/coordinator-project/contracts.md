# Required scheduling behavior

A job is ready only after all dependencies have completed in earlier stages. At each stage select at most the requested parallelism from the ready set, ordered by descending priority then ascending ID. Finish a stage before selecting the next. Include every job exactly once. Do not mutate input records.

Reject a non-list document, non-object records, absent or unknown fields, empty/non-string IDs, duplicate IDs, bool/non-integer priorities, non-list dependencies, non-string dependencies, duplicate dependencies, unknown dependencies, self dependencies, and cycles. Empty input is valid. A failure must not return a partial plan.

The CLI accepts one input JSON file and `--parallelism` (positive integer, default 2). Output JSON is exactly {"stages": [["id", ...], ...]}. Invalid input prints a useful error on stderr, exits nonzero, and prints neither a plan nor a traceback. `--help` exits successfully. Standard library only.
