"""Derive raw campaign retention capacity from independently selected catalog rows."""
from __future__ import annotations

from .installed_cases.records import MAX_STREAMS
from .installed_variants import requirements
from .support_matrix import expand

MAX_BYTES = 64 << 30
MAX_FILE = 16 << 30


def limits(catalog: dict, final: dict | None = None) -> dict:
    rows = expand(catalog)
    if final is None:
        # Transport planning uses catalog combinations and grants no artifact admission.
        selections = [(installation + '-' + architecture, installation, architecture)
                      for installation, architecture in sorted({(row['installation'], row['architecture']) for row in rows})]
    else:
        artifacts = final.get('artifacts')
        if not isinstance(artifacts, list) or not artifacts or any(not isinstance(item, dict) for item in artifacts):
            raise ValueError('campaign capacity requires independently selected final artifacts')
        selections = [(item['id'], item['installation'], item['architecture']) for item in artifacts]
    targets, native_targets, cases = set(), set(), 0
    for artifact_id, installation, architecture in selections:
        for row in rows:
            if (row['installation'], row['architecture']) != (installation, architecture):
                continue
            variants = requirements(catalog, row['environment'], row['capability'])
            cases += len(variants) * len(row['cases'])
            selected_targets = {(artifact_id, row['environment'], item['id']) for item in variants}
            targets.update(selected_targets)
            if installation in {'deb', 'rpm', 'pacman'}:
                native_targets.update(selected_targets)
    if not targets:
        raise ValueError('campaign capacity selected no catalog targets')
    # Both transported leaf and normative controller evidence are retained. A case
    # can preserve its bounded partial record and a separate captured-product
    # failure record (each up to MAX_STREAMS streams), plus a generic failure.
    case_files = cases * (2 * MAX_STREAMS + 3) * 2
    # Includes per-case directory entries, per-target metadata/logs/ZIP and top-level
    # aggregate documents. Control bytes remain under their separate fixed limits.
    # A native target may also own one pre-bind installation journal, bounded by
    # the same stream contract, with both leaf and controller copies/directories.
    transaction_files = len(native_targets) * (MAX_STREAMS + 3) * 2
    maximum_files = case_files + cases * 4 + len(targets) * 32 + transaction_files + 128
    return {'max_files': maximum_files, 'max_bytes': MAX_BYTES, 'max_file': MAX_FILE,
            'targets': len(targets), 'case_observations': cases}
