"""Expose application-owned rate observations without duplicating its controller."""
import json


def observations(root):
    buckets = []
    for path in sorted((root / 'provider-rate-state').glob('*.json')):
        state = json.loads(path.read_text())
        if not isinstance(state, dict) or state.get('version') != 1:
            raise ValueError(f'unsupported provider rate state: {path.name}')
        buckets.append({'bucket': path.stem, **state})
    return buckets
