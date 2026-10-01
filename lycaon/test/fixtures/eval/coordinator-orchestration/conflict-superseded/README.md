# Catalog selection
select_records(records, prefix="", offset=0, limit=None) first keeps records whose casefolded name starts with the casefolded prefix, then skips offset records and takes limit records; None means all remaining. Input order and records are unchanged. Offset and limit reject negative, boolean and noninteger values with ValueError; only limit may be None. Two worker results are attached: prefix filtering and pagination. The pagination change is already applied in the working tree. Preserve preferences.json.
Keep the supplied tests and this README unchanged. Use regular project files, Python standard library only, and no downloads or dependencies.
Run the full supplied suite with `python3 -B -m unittest discover`.

The worker pagination assignment is canceled because the current tree already contains it; the prefix-filtering assignment remains active.

Keep the existing checklist labels unchanged.
