"""Resolve explicit application targets and export immutable Git trees."""
import json
import os
import pathlib
import re
import subprocess
import tempfile

CONTRACT = 'lycaon/internal/harnessfixture/contract.json'


def git(root, *args):
    return subprocess.check_output(['git', *args], cwd=root)


def resolve(root, ref):
    if not ref or ref.startswith('-'):
        raise ValueError('select an application tag or full commit identity')
    tag = 'refs/tags/' + ref.removeprefix('refs/tags/')
    tagged = subprocess.run(['git', 'show-ref', '--verify', '--quiet', tag], cwd=root).returncode
    if tagged not in (0, 1):
        raise ValueError('could not inspect the selected tag')
    if tagged and not re.fullmatch(r'[0-9a-f]{40}|[0-9a-f]{64}', ref):
        raise ValueError('application revisions must be tags or full commit identities; branches are mutable')
    selected = tag if not tagged else ref
    commit = git(root, 'rev-parse', '--verify', selected + '^{commit}').decode().strip()
    tree = git(root, 'rev-parse', commit + '^{tree}').decode().strip()
    return {'ref': ref, 'revision': commit, 'tree': tree, 'dirty': False, 'kind': 'tag' if not tagged else 'commit'}


def working_tree(root):
    commit = subprocess.check_output(['bash', 'scripts/repo-snapshot-lock.sh', 'read', '--',
                                     'bash', 'scripts/test-source-snapshot.sh', 'capture'], cwd=root, text=True).strip()
    parent = git(root, 'rev-parse', commit + '^').decode().strip()
    tree = git(root, 'rev-parse', commit + '^{tree}').decode().strip()
    clean = git(root, 'rev-parse', parent + '^{tree}').decode().strip() == tree
    return {'ref': None, 'revision': parent, 'snapshot': commit, 'tree': tree,
            'dirty': not clean, 'kind': 'worktree'}


def read_file(root, revision, path):
    return git(root, 'show', revision['tree'] + ':' + path)


def contract(root, revision):
    value = json.loads(read_file(root, revision, CONTRACT))
    if (set(value) != {'revision', 'benchmark_paths', 'harness_paths'}
            or type(value['revision']) is not int or value['revision'] < 1):
        raise ValueError('unsupported application evaluation contract')
    for group in ('benchmark_paths', 'harness_paths'):
        if not isinstance(value[group], list) or not value[group]:
            raise ValueError('evaluation contract requires explicit source boundaries')
        for name in value[group]:
            if not isinstance(name, str):
                raise ValueError('evaluation source boundary requires a relative path')
            path = pathlib.PurePosixPath(name)
            if path.is_absolute() or '..' in path.parts or name in {'.', ''}:
                raise ValueError('evaluation source boundary escapes the application')
    return value


def export(root, revision, destination):
    names = git(root, 'ls-tree', '-r', '--name-only', '-z', revision['tree'], '--',
                'lycaon', 'scripts', 'schemas', 'task', 'Taskfile.yml', 'VERSION', 'RELEASE_BUILD')
    destination.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory() as temporary:
        environment = {**os.environ, 'GIT_INDEX_FILE': str(pathlib.Path(temporary) / 'index')}
        subprocess.run(['git', 'read-tree', revision['tree']], cwd=root, env=environment, check=True)
        subprocess.run(['git', 'checkout-index', '--stdin', '-z', '--prefix=' + str(destination) + '/'],
                       cwd=root, env=environment, input=names, check=True)
    return names.decode().rstrip('\0').split('\0')
