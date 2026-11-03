"""Reject stale generated Nix frontend inputs before starting hosted lanes."""
import hashlib
import json
from pathlib import Path


def require_current(root: Path) -> None:
    identity = json.loads((root / 'packaging/nix/bun-lock.json').read_text())
    if not isinstance(identity, dict) or set(identity) != {
            'source_sha256', 'expression_sha256', 'generator_revision'}:
        raise ValueError('Nix Bun identity has an unsupported shape')
    revision = json.loads((root / 'flake.lock').read_text())['nodes']['bun2nix']['locked']['rev']
    expected = {
        'source_sha256': hashlib.sha256((root / 'lycaon-den/bun.lock').read_bytes()).hexdigest(),
        'expression_sha256': hashlib.sha256((root / 'packaging/nix/bun.nix').read_bytes()).hexdigest(),
        'generator_revision': revision,
    }
    changed = [field for field, value in expected.items() if identity[field] != value]
    if changed:
        raise ValueError('Nix Bun inputs are stale (' + ', '.join(changed) +
                         '); run ./task codegen:dependency-inventory -- --nix-bun')
