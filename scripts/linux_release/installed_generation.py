"""Bind protected runtime inodes to the administrator's exact final generation."""
from __future__ import annotations

from dataclasses import replace
import hashlib
import json
from pathlib import Path
import stat

from .installed_runtime import SETUID_LAUNCHER, file_identity
from .installed_evidence import SHA256
from .installed_cases.package_payload import verify

POLICY = Path('/etc/paintedwolf/launch.json')


def controlled(path: Path, *, directory=False) -> None:
    current = path
    first = True
    while True:
        info = current.lstat()
        kind = stat.S_ISDIR if directory or not first else stat.S_ISREG
        if not kind(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
            raise ValueError('selected generation requires physical root-controlled paths')
        if current.parent == current:
            break
        current, first = current.parent, False


def identity(path: Path) -> dict:
    controlled(path)
    record = file_identity(path)
    record.pop('header')
    return record


def bind(root: Path, package_images: dict, package_entries: dict, *, policy_path=POLICY) -> dict | None:
    entry = str(root / 'bin/pw-launch-entry')
    bootstrap = str(SETUID_LAUNCHER)
    if entry not in package_images and bootstrap not in package_images:
        return None
    if entry not in package_images or bootstrap not in package_images:
        raise ValueError('protected package omits its complete bootstrap and entry contract')
    before = identity(policy_path)
    if before['bytes'] > 1 << 20:
        raise ValueError('selected generation policy exceeds its bound')
    raw_policy = policy_path.read_bytes()
    if hashlib.sha256(raw_policy).hexdigest() != before['sha256']:
        raise ValueError('selected generation policy changed before parsing')
    policy = json.loads(raw_policy)
    if (not isinstance(policy, dict) or type(policy.get('version')) is not int or policy['version'] != 1
            or policy.get('logical_install_root') != str(root)
            or not isinstance(policy.get('manifest_sha256'), str) or not SHA256.fullmatch(policy['manifest_sha256'])):
        raise ValueError('selected generation policy has no exact native package selection')
    digest = policy['manifest_sha256']
    generation = root / 'generations' / digest
    if policy.get('generation_root') != str(generation):
        raise ValueError('selected generation is outside its exact native store')
    controlled(generation, directory=True)
    package_manifest = package_entries[str((root / 'manifest.json').relative_to('/'))]
    seal = identity(generation / 'manifest.json')
    staged = identity(root / 'manifest.json')
    if (seal['sha256'] != digest or staged['sha256'] != digest
            or (staged['sha256'], staged['bytes'], staged['mode']) !=
               (package_manifest.sha256, package_manifest.size, package_manifest.mode)):
        raise ValueError('selected generation manifest differs from the exact final artifact')
    prefix = str(root.relative_to('/')) + '/'
    closure = {}
    for name, entry in package_entries.items():
        if not name.startswith(prefix):
            continue
        relative = name[len(prefix):]
        if entry.kind == 'hardlink':
            target = package_entries.get(entry.target)
            if target is None or target.kind != 'file' or not entry.target.startswith(prefix):
                raise ValueError('selected generation has no internal final payload link target')
            entry = replace(entry, kind='file', sha256=target.sha256, size=target.size, target='')
        closure[relative] = replace(entry, name=relative)
    verify(closure, generation)
    images = {}
    for logical, expected in package_images.items():
        path = Path(logical)
        selected = generation / path.relative_to(root) if path.is_relative_to(root) else path
        actual = identity(selected)
        if any(actual[field] != expected[field] for field in ('sha256', 'bytes', 'mode')):
            raise ValueError('selected generation image differs from the exact final artifact')
        images[logical] = actual
    if identity(policy_path) != before or identity(generation / 'manifest.json') != seal:
        raise ValueError('selected generation changed during binding')
    return {'root': str(generation), 'policy_path': str(policy_path), 'policy_identity': before,
            'manifest_identity': seal, 'images': images}


def require_current(binding: dict) -> None:
    selected = binding.get('selected_generation')
    if selected is None:
        return
    if (identity(Path(selected['policy_path'])) != selected['policy_identity']
            or identity(Path(selected['root']) / 'manifest.json') != selected['manifest_identity']):
        raise ValueError('selected protected generation changed since final artifact binding')


def image(binding: dict, relative: str) -> tuple[Path, dict]:
    require_current(binding)
    logical = Path(binding['install_root']) / relative
    selected = binding.get('selected_generation')
    path = Path(selected['root']) / relative if selected is not None else logical
    expected = binding['images'][str(logical)]
    if identity(path) != expected:
        raise ValueError('selected runtime image changed since final artifact binding')
    return path, expected
