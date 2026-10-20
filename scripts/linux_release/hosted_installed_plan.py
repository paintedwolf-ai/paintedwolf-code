"""Validate an immutable, nonpublishing hosted installed rehearsal selection."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import subprocess

COMPONENTS = {'native_candidates', 'support_variants', 'installed_execution'}
FIELDS = {'format_version', 'controller_commit', 'controller_tree', 'components', 'architecture',
          'candidate_run_id', 'candidate_artifact_id', 'candidate_controller_commit',
          'source_commit', 'source_tree', 'partial'}


def selection(value: object) -> dict:
    if not isinstance(value, dict) or set(value) != FIELDS or type(value['format_version']) is not int or value['format_version'] != 1:
        raise ValueError('hosted installed rehearsal requires an exact selection document')
    for name in ('controller_commit', 'controller_tree', 'candidate_controller_commit', 'source_commit', 'source_tree'):
        if not isinstance(value[name], str) or not re.fullmatch('[a-f0-9]{40}', value[name]):
            raise ValueError('hosted installed rehearsal requires immutable commit and tree identities')
    components = value['components']
    if (not isinstance(components, dict) or set(components) != COMPONENTS
            or any(not isinstance(item, str) or not re.fullmatch('[a-f0-9]{40}', item) for item in components.values())):
        raise ValueError('hosted installed controller requires every independently frozen prerequisite')
    if (value['architecture'] not in {'amd64', 'arm64'} or type(value['partial']) is not bool
            or any(type(value[name]) is not int or value[name] < 1 for name in ('candidate_run_id', 'candidate_artifact_id'))):
        raise ValueError('hosted installed candidate selection has an invalid CPU, artifact or run identity')
    return value


def read(path: Path) -> dict:
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 16384:
        raise ValueError('hosted rehearsal selection is not a bounded regular file')
    return selection(json.loads(path.read_text()))


def controller(checkout: Path, selected: dict):
    def git(*arguments):
        result = subprocess.run(['git', '-c', 'safe.directory=' + str(checkout), '-C', str(checkout), *arguments],
                                capture_output=True, text=True, check=True, timeout=15)
        return result.stdout.strip()
    if git('rev-parse', 'HEAD') != selected['controller_commit'] or git('rev-parse', 'HEAD^{tree}') != selected['controller_tree']:
        raise ValueError('hosted controller differs from the independently frozen commit and tree')
    if git('status', '--porcelain', '--untracked-files=all', '--', 'scripts/linux_release', 'packaging/linux',
           'Taskfile.yml', 'scripts/verification-plan.json', '.github/workflows'):
        raise ValueError('hosted controller contains changes outside its immutable Git tree')
    for component in selected['components'].values():
        git('merge-base', '--is-ancestor', component, selected['controller_commit'])


def main(arguments=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--selection', type=Path, required=True)
    parser.add_argument('--github-output', type=Path, required=True)
    args = parser.parse_args(arguments)
    try:
        value = read(args.selection)
        # Only fixed typed values become workflow outputs; arbitrary text never enters shell authority.
        with args.github_output.open('a') as stream:
            for name in ('controller_commit', 'controller_tree', 'architecture', 'source_commit', 'source_tree',
                         'candidate_controller_commit', 'candidate_run_id', 'candidate_artifact_id'):
                stream.write(name + '=' + str(value[name]) + '\n')
            stream.write('partial=' + ('true' if value['partial'] else 'false') + '\n')
    except (OSError, ValueError, TypeError) as error:
        parser.exit(1, 'Hosted installed selection refused: ' + str(error) + '\n')
    return 0
