"""Select a nonpublishing hosted installed rehearsal from dispatch inputs.

The controller is the checkout the workflow runs; its commit and tree are
observed, never taken from a checked-in pin."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import subprocess

FIELDS = {'format_version', 'controller_commit', 'controller_tree', 'architecture',
          'candidate_run_id', 'candidate_artifact_id', 'candidate_controller_commit',
          'source_commit', 'source_tree', 'partial'}


def selection(value: object) -> dict:
    if not isinstance(value, dict) or set(value) != FIELDS or type(value['format_version']) is not int or value['format_version'] != 1:
        raise ValueError('hosted installed rehearsal requires an exact selection document')
    for name in ('controller_commit', 'controller_tree', 'candidate_controller_commit', 'source_commit', 'source_tree'):
        if not isinstance(value[name], str) or not re.fullmatch('[a-f0-9]{40}', value[name]):
            raise ValueError('hosted installed rehearsal requires immutable commit and tree identities')
    if (value['architecture'] not in {'amd64', 'arm64'} or type(value['partial']) is not bool
            or any(type(value[name]) is not int or value[name] < 1 for name in ('candidate_run_id', 'candidate_artifact_id'))):
        raise ValueError('hosted installed candidate selection has an invalid CPU, artifact or run identity')
    return value


def read(path: Path) -> dict:
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 16384:
        raise ValueError('hosted rehearsal selection is not a bounded regular file')
    return selection(json.loads(path.read_text()))


def observed_controller(checkout: Path) -> tuple[str, str]:
    """The checkout's commit and tree; refuses controller changes outside Git."""
    def git(*arguments):
        result = subprocess.run(['git', '-c', 'safe.directory=' + str(checkout), '-C', str(checkout), *arguments],
                                capture_output=True, text=True, check=True, timeout=15)
        return result.stdout.strip()
    if git('status', '--porcelain', '--untracked-files=all', '--', 'scripts/linux_release', 'packaging/linux',
           'Taskfile.yml', 'scripts/verification-plan.json', '.github/workflows'):
        raise ValueError('hosted controller contains changes outside its immutable Git tree')
    return git('rev-parse', 'HEAD'), git('rev-parse', 'HEAD^{tree}')


def controller(checkout: Path, selected: dict):
    if observed_controller(checkout) != (selected['controller_commit'], selected['controller_tree']):
        raise ValueError('hosted controller differs from the selected commit and tree')


def main(arguments=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--checkout', type=Path, required=True)
    parser.add_argument('--architecture', required=True)
    parser.add_argument('--candidate-run-id', type=int, required=True)
    parser.add_argument('--candidate-artifact-id', type=int, required=True)
    parser.add_argument('--candidate-controller-commit', required=True)
    parser.add_argument('--source-commit', required=True)
    parser.add_argument('--source-tree', required=True)
    parser.add_argument('--partial', action='store_true')
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args(arguments)
    try:
        commit, tree = observed_controller(args.checkout)
        value = selection({
            'format_version': 1, 'controller_commit': commit, 'controller_tree': tree,
            'architecture': args.architecture, 'candidate_run_id': args.candidate_run_id,
            'candidate_artifact_id': args.candidate_artifact_id,
            'candidate_controller_commit': args.candidate_controller_commit,
            'source_commit': args.source_commit, 'source_tree': args.source_tree, 'partial': args.partial,
        })
        with args.output.open('x', encoding='utf-8') as stream:
            json.dump(value, stream, sort_keys=True)
    except (OSError, ValueError, TypeError, subprocess.SubprocessError) as error:
        parser.exit(1, 'Hosted installed selection refused: ' + str(error) + '\n')
    return 0
