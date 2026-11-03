"""Stop expensive planning on an already rejected commit certification."""
import json
import os
from pathlib import Path
import re
import subprocess

from ci_policy.github import api, repository


def rejection(event):
    pull = event.get('pull_request', {})
    # Bot certification follows successful CI through the trusted workflow_run route.
    if pull.get('user', {}).get('login') == 'dependabot[bot]':
        return None
    head = pull.get('head', {}).get('sha', '')
    if not isinstance(head, str) or not re.fullmatch(r'[0-9a-f]{40}', head):
        return None
    try:
        state = api(f'{repository()}/commits/{head}/status?per_page=100', timeout=15)
        if state.get('sha') != head:
            return None
        for row in state.get('statuses', []):
            if row.get('context') == 'DCO-owned':
                return {'head': head, 'state': row['state']} if row.get('state') in {'failure', 'error'} else None
    except (KeyError, TypeError, AttributeError, ValueError, OSError, subprocess.SubprocessError):
        return None
    return None


def main():
    if os.environ.get('GITHUB_EVENT_NAME') != 'pull_request':
        return 0
    with Path(os.environ['GITHUB_EVENT_PATH']).open() as source:
        failed = rejection(json.load(source))
    if failed:
        print('::error title=Commit certification::DCO-owned rejected this head; repair certification before verification.')
        print(json.dumps(failed, sort_keys=True))
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
