# Catalog selection

Integrate prefix filtering and pagination. select_records(records, prefix="", offset=0, limit=None) first selects names whose casefolded string starts with the casefolded prefix, then skips offset records and takes limit records (None means all remaining). Keep input order and records unchanged. Empty inputs, empty prefix, zero limit, and offsets beyond the end are supported. Invalid offsets and limits (negative, boolean, non-integer) raise ValueError; only limit may be None. Records always contain string names; other record shapes are outside scope.

The two worker overlays branch from the same baseline. Each has passed its own feature check. The combined tests are already supplied. Preserve the user's current preferences.json exactly, even if a worker branched from an older preference.

Run python3 -B -m unittest discover.

Keep the supplied tests and README unchanged. Use regular project files and the Python standard library; no dependencies or downloads.
