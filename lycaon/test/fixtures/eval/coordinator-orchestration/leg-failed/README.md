# Status labels
display_status(status) maps pending, active and done to Waiting, Running and Complete. Other inputs are outside scope. A worker applies the labels. If the worker fails, do not retry: leave status.py as it is, close the pending checklist item as not completed, and explain the failure in your final response.
Keep the supplied tests and this README unchanged. Use regular project files, Python standard library only, and no downloads or dependencies.

Worker assignments: Status labels: a write worker scoped to `status.py`. Limit each assignment’s scope to its listed files.

Keep the existing checklist labels unchanged.

Prefix filtering is independent: select_records in catalog.py keeps case-insensitive name-prefix matches in input order; an empty prefix keeps every record. Prefix filtering is a write-worker assignment scoped to catalog.py. Run each assignment once; a failed assignment remains unchanged and is not retried.
