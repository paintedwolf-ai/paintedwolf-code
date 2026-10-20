"""Admit a separately pinned NixOS image factory without changing application inputs."""

import hashlib
import json
from pathlib import Path
import re
import subprocess

CHANNELS = ('nixos-26.05', 'nixos-unstable')
VM_COMPONENTS = ('nixos-gnome-vm', 'nixos-plasma-vm')


def committed(root, commit, path):
    return subprocess.check_output(['git', '-c', 'core.fsmonitor=false', 'show', commit + ':' + path], cwd=root)


def graph(lock, start):
    """Compare resolved input graphs independently of lock-node naming."""
    nodes = lock['nodes']
    def resolve(value, seen):
        if isinstance(value, list):
            marker = tuple(value)
            if marker in seen or not value or any(not isinstance(field, str) for field in value):
                raise ValueError('Cyclic or invalid factory follows input')
            node = lock['root']
            for field in value:
                node = resolve(nodes[node]['inputs'][field], seen + [marker])
            return node
        if not isinstance(value, str) or value not in nodes:
            raise ValueError('Invalid factory lock input')
        return value
    def visit(name, ancestors):
        name = resolve(name, ancestors)
        if name in ancestors:
            raise ValueError('Cyclic factory lock input')
        node = nodes[name]
        return {key: node[key] for key in ('locked', 'original') if key in node} | {
            'inputs': {key: visit(value, ancestors + [name]) for key, value in node.get('inputs', {}).items()}}
    return visit(start, [])


def validate_lock(root, descriptor, raw):
    lock = json.loads(raw)
    nodes = lock['nodes']
    inputs = nodes[lock['root']]['inputs']
    application = nodes[inputs['application']]
    pin = application['locked']
    if pin.get('rev') != descriptor['application_commit'] or pin.get('type') != 'git' or \
            pin.get('url') != 'https://github.com/paintedwolf-ai/paintedwolf-code':
        raise ValueError('Factory application source is not the selected immutable repository commit')
    host = nodes[inputs['nixpkgs']]
    if host['original'] != {'owner': 'NixOS', 'repo': 'nixpkgs', 'ref': descriptor['channel'], 'type': 'github'} or \
            not re.fullmatch('[0-9a-f]{40}', host['locked'].get('rev', '')):
        raise ValueError('Factory host channel/revision is not independently pinned')
    app_lock = json.loads(committed(root, descriptor['application_commit'], 'flake.lock'))
    expected = graph(app_lock, app_lock['root'])['inputs']
    if graph(lock, inputs['application'])['inputs'] != expected:
        raise ValueError('Factory changes the application dependency graph')
    return {'nixpkgs_revision': host['locked']['rev'], 'lock_sha256': hashlib.sha256(raw).hexdigest()}


def select(root: Path, commit: str, channel: str, component: str, *, prepare=False):
    if channel not in CHANNELS or component not in VM_COMPONENTS:
        raise ValueError('Factory input is restricted to declared NixOS image components/channels')
    if not root.is_absolute() or root.is_symlink() or root.resolve() != root:
        raise ValueError('Factory source must be a physical absolute Git root')
    if not re.fullmatch('[0-9a-f]{40}', commit):
        raise ValueError('Factory source requires an immutable full commit')
    actual = subprocess.check_output(['git', 'rev-parse', '--show-toplevel'], cwd=root, text=True).strip()
    if actual != str(root):
        raise ValueError('Factory source must name the Git checkout root')
    subdir = 'packaging/nix/qualification-factories/' + channel
    descriptor = json.loads(committed(root, commit, subdir + '/factory-inputs.json'))
    if set(descriptor) != {'schema_version', 'application_commit', 'channel'} or \
            type(descriptor['schema_version']) is not int or descriptor['schema_version'] != 1 or \
            descriptor['channel'] != channel or not re.fullmatch('[0-9a-f]{40}', descriptor['application_commit']):
        raise ValueError('Invalid committed factory descriptor')
    result = {'source_commit': commit, 'source_tree': subprocess.check_output(
        ['git', 'rev-parse', commit + '^{tree}'], cwd=root, text=True).strip(),
        'subdirectory': subdir, **descriptor}
    if not prepare:
        result.update(validate_lock(root, descriptor, committed(root, commit, subdir + '/flake.lock')))
    return result
