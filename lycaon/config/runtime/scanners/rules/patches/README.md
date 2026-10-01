# Vendored rule patches

Local fixes to vendored upstream rules live here as `<id>/NNNN-*.patch`, applied
in name order to a catalog's staged tree by `scripts/vendor-scan-rules.sh`.

A vendored rule file is never edited in place. An edit there is invisible to a
later `--bump`, which re-materializes the catalog from upstream and drops it; a
patch is re-applied by every sync, and one that no longer applies fails the sync
instead of being skipped.

`rules-provenance.yaml` records both sides: `tree_sha256` is the pinned commit's
bytes before any patch, `vendored_sha256` is what ships after them, and
`patch_sha256` is the patch set itself. `--check` verifies the last two offline.

Patch paths are relative to the catalog root — `rules/generic/x.yaml`, not
`lycaon/config/runtime/scanners/rules/vendor/<id>/rules/generic/x.yaml`.

## What belongs here

Making a rule loadable by the engine this product ships: a construct OpenGrep
does not support, or an upstream rule whose schema is invalid.

## What does not

Changing what a rule detects. A rule that matches something different is a
different rule, and shipping one under upstream's id and message reports
findings upstream's rule did not make. Those belong in `rules/lycaon/` under a
first-party id, where divergence is the expectation rather than a surprise.
