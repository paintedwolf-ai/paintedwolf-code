# Account report

The existing report sorts names using ascending case-sensitive string order, retains input order for equal names, and defaults to two records. Explicit nonnegative limits work; input records and their nested values are preserved. app.py reads a JSON array from stdin and prints the default report as JSON.

The new requirement is to omit inactive accounts before applying the limit, in both summarize and the CLI. Every input record has a string name and a boolean active field. Retain the earlier ordering, stable ties, input preservation, and limit behavior. Other input types are outside scope.

Run python3 -B -m unittest discover.

Keep the supplied tests and README unchanged. Use regular project files and the Python standard library; no dependencies or downloads.
