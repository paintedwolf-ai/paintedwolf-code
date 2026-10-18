"""Publish scheduled evidence updates as reviewable, signed-off pull requests."""
import hashlib
import os
from pathlib import Path
import subprocess

from .github import api, pages, repository

PATHS = ['docs/operations/dependency-inventory.md', 'dependencies/upstream.json', '.github/dependabot.yml',
         'lycaon/vulndb', 'lycaon/vulndb-provenance.json']


def publish():
    subprocess.run(['git', 'add', '--', *PATHS], check=True)
    diff = subprocess.check_output(['git', 'diff', '--cached', '--binary'])
    if not diff:
        return
    branch = 'automation/dependency-evidence-' + hashlib.sha256(diff).hexdigest()[:16]
    owner = os.environ['GITHUB_REPOSITORY'].split('/')[0]
    existing = list(pages(f'{repository()}/pulls?state=all&head={owner}:{branch}'))
    if existing:
        print(existing[0]['html_url'])
        return
    subprocess.run(['git', 'switch', '-c', branch], check=True)
    subprocess.run(['git', '-c', 'user.name=github-actions[bot]', '-c',
                    'user.email=41898282+github-actions[bot]@users.noreply.github.com',
                    'commit', '-s', '-m', 'Refresh pinned dependency evidence'], check=True)
    subprocess.run(['git', 'push', 'origin', 'HEAD:refs/heads/' + branch], check=True)
    pull = api(f'{repository()}/pulls', 'POST', {'title': 'Refresh pinned dependency evidence',
               'head': branch, 'base': 'main', 'body': 'Scheduled complete advisory snapshot and informational dependency inventory. '
               'Review new advisories independently of feature changes. This PR uses the ordinary merge queue.'})
    # A token-created PR fires no events of its own; dispatch each workflow on the branch.
    api(f'{repository()}/actions/workflows/ci.yml/dispatches', 'POST', {'ref': branch, 'inputs': {'profile': 'fast'}})
    api(f'{repository()}/actions/workflows/dco.yml/dispatches', 'POST', {'ref': branch, 'inputs': {'pull_request': str(pull['number'])}})
    print(pull['html_url'])


if __name__ == '__main__':
    publish()
