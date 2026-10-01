# Catalog
select_records(names) must strip whitespace and casefold each string, then deduplicate normalized names, retaining the first occurrence order. Preserve the input list. Empty strings are valid names.
Keep the supplied tests and this README unchanged. Use regular project files, Python standard library only, and no downloads or dependencies.
Run the full supplied suite with `python3 -B -m unittest discover`.
Leave the current preferences.json bytes unchanged.
