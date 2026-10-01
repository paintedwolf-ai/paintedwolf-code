# Service report

A dependency-free Python service report. Run `python3 -m unittest` for tests and
`python3 app.py sample.json` for the terminal report. Keep imports standard-library only.

The application should show the highest score first, breaking equal scores by
service name. Invalid records should produce a useful error rather than a report.

## Report contract

Order scores numerically, descending, and names by case-sensitive Python string
order, ascending. Preserve the input order of records with equal scores and names.
Leave input records and their nested values unchanged.

Keep the default limit of 8 and both positional and keyword limit arguments.
Support empty input and all nonnegative limits, including zero. Preserve the CLI
table format and input validation.

Keep the supplied tests unchanged and add regression tests for score ordering and
name ties when fixing ranking. Keep the project self-contained, with regular files
and no new dependencies.
