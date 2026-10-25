"""Feed a verified live group to the unchanged pinned DCO checker."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

from .queue_dispatch import context


def event(repository, ref, sha, pull_request=''):
    if pull_request:
        raise ValueError('queue certification cannot combine pull_request and queue inputs')
    if not ref or not sha:
        raise ValueError('queue certification requires both queue_ref and queue_sha')
    base = context(repository, ref, sha)
    return {'merge_group': {'base_ref': 'refs/heads/main', 'head_ref': ref,
                            'base_sha': base, 'head_sha': sha}}


def run(repository, ref, sha, checker, pull_request=''):
    payload = event(repository, ref, sha, pull_request)
    with tempfile.TemporaryDirectory(prefix='queue-certification-') as directory:
        path = Path(directory) / 'event.json'
        path.write_text(json.dumps(payload))
        environment = {**os.environ, 'GITHUB_EVENT_NAME': 'merge_group', 'GITHUB_EVENT_PATH': str(path)}
        # The pinned checker independently inventories queue members and
        # rechecks their exact heads and live group before publishing DCO-owned.
        return subprocess.call([sys.executable, checker], env=environment)


if __name__ == '__main__':
    raise SystemExit(run(os.environ['GITHUB_REPOSITORY'], os.environ.get('QUEUE_REF', ''),
                         os.environ.get('QUEUE_SHA', ''), sys.argv[1],
                         os.environ.get('PULL_REQUEST', '')))
