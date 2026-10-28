"""Observe the controlled image input and actual running NixOS revision."""
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess

from .records import CaseUnavailable

MARKER = Path('/etc/paintedwolf-factory.json')
VERSION = Path('/run/current-system/sw/bin/nixos-version')
FIELDS = {'schema_version', 'channel', 'nixpkgs_revision', 'application_commit', 'application_store_path'}


def pairs(items):
    result = {}
    for key, value in items:
        if key in result:
            raise ValueError('NixOS factory marker repeats a field')
        result[key] = value
    return result


def read_marker():
    if MARKER.resolve() != MARKER:
        # NixOS /etc entries normally link to the immutable generation in /nix/store.
        target = MARKER.resolve()
        if not target.is_relative_to('/nix/store'):
            raise ValueError('NixOS factory marker must resolve into the immutable store')
    else:
        target = MARKER
    try:
        descriptor = os.open(target, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    except FileNotFoundError as error:
        raise CaseUnavailable('actual-nixos-factory-marker-unavailable') from error
    try:
        before = os.fstat(descriptor)
        if (not stat.S_ISREG(before.st_mode) or before.st_uid != 0 or before.st_mode & 0o022
                or not 0 < before.st_size <= 16384):
            raise ValueError('NixOS factory marker is not a bounded root-controlled regular file')
        raw = os.read(descriptor, 16385)
        after = os.fstat(descriptor)
        if (len(raw) != before.st_size or (before.st_dev, before.st_ino, before.st_mtime_ns, before.st_ctime_ns)
                != (after.st_dev, after.st_ino, after.st_mtime_ns, after.st_ctime_ns)):
            raise ValueError('NixOS factory marker changed during observation')
    finally:
        os.close(descriptor)
    return json.loads(raw, object_pairs_hook=pairs), hashlib.sha256(raw).hexdigest()


def revision():
    physical = VERSION.resolve()
    try:
        information = physical.stat()
    except FileNotFoundError as error:
        raise CaseUnavailable('actual-nixos-version-unavailable') from error
    if (not physical.is_relative_to('/nix/store') or not stat.S_ISREG(information.st_mode)
            or information.st_uid != 0 or information.st_mode & 0o022 or not information.st_mode & 0o111):
        raise ValueError('NixOS version observer is not an immutable root-controlled executable')
    result = subprocess.run([str(physical), '--revision'], env={'PATH': '/run/current-system/sw/bin', 'LC_ALL': 'C'},
                            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
    if result.returncode or len(result.stdout) > 128:
        raise CaseUnavailable('actual-nixos-revision-observation-failed')
    value = result.stdout.decode('ascii').strip()
    if re.fullmatch('[0-9a-f]{40}', value) is None:
        raise ValueError('NixOS version observer did not return an exact revision')
    return value


def observe(binding, snapshot):
    if (not isinstance(snapshot, dict) or set(snapshot) != {'kind', 'value', 'channel'}
            or snapshot['kind'] != 'nixos-revision' or snapshot['channel'] not in {'nixos-26.05', 'nixos-unstable'}
            or not isinstance(snapshot['value'], str) or not re.fullmatch('[0-9a-f]{40}', snapshot['value'])):
        raise ValueError('NixOS observation requires an independently selected channel and revision')
    marker, digest = read_marker()
    identity = binding['artifact']['identity']
    if (not isinstance(marker, dict) or set(marker) != FIELDS or type(marker['schema_version']) is not int
            or marker['schema_version'] != 1 or marker['channel'] != snapshot['channel']
            or marker['nixpkgs_revision'] != snapshot['value']
            or marker['application_commit'] != binding['source_commit']
            or identity.get('kind') != 'nix-closure' or marker['application_store_path'] != identity.get('store_path')):
        raise ValueError('Actual NixOS factory differs from the selected host or installed application identity')
    actual = revision()
    if actual != snapshot['value']:
        raise ValueError('Running NixOS revision differs from the independently controlled factory input')
    return {'format_version': 1, 'snapshot': snapshot, 'factory_marker_sha256': digest,
            'running_nixpkgs_revision': actual, 'application_commit': marker['application_commit'],
            'application_store_path': marker['application_store_path']}
