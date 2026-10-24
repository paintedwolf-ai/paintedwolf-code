"""Keep partial hosted startup observations separate from complete campaigns."""
from __future__ import annotations

import json
from pathlib import Path
import secrets
import shutil
import stat

from . import hosted_installed as runtime
from .hosted_desktop import fresh
from .installed_artifact_bundle import Limits, retain_tree
from .installed_execution import read
from .installed_runtime import file_identity


def require_partial(selected):
    if selected.get('partial') is not True:
        raise ValueError('partial hosted diagnostic requires an explicitly partial producer')


def prepare(selected: dict, checkout: Path, candidate: Path, facts: dict) -> dict:
    require_partial(selected)
    account = runtime.context(selected, checkout, facts)
    root = runtime.ROOT
    if root.exists() or root.is_symlink() or runtime.INSTALL.exists() or runtime.INSTALL.is_symlink():
        raise ValueError('partial diagnostic must preserve existing installations and campaigns')
    final, producer, artifact, source, identity = runtime.candidate_package(selected, candidate)
    marker = Path('/etc/paintedwolf-qualification.json')
    info = marker.lstat()
    declared = read(marker)
    if (type(declared.get('format_version')) is not int or not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022
            or declared != {'format_version': 1, 'desktop': 'gnome', 'package': str(runtime.INSTALL)}):
        raise ValueError('partial diagnostic requires the measured desktop ownership marker')
    output = Path(account.pw_dir) / 'installed-observations'
    if output.exists() or output.is_symlink():
        raise ValueError('partial diagnostic requires a fresh graphical observation path')
    root.mkdir(mode=0o755)
    packages = root / 'packages'
    packages.mkdir(mode=0o755)
    target = packages / artifact['id']
    shutil.copyfile(source, target)
    target.chmod(0o444)
    copied = file_identity(target)
    if any(copied[key] != identity[key] for key in ('sha256', 'bytes')):
        raise ValueError('partial package changed while entering administrator custody')
    contract = {'format_version': 1, 'scope': 'partial-native-first-launch-diagnostic',
                'controller_commit': selected['controller_commit'], 'driver_tree': selected['controller_tree'],
                'source_commit': selected['source_commit'], 'source_tree': selected['source_tree'],
                'artifact_id': artifact['id'], 'execution_nonce': secrets.token_hex(16),
                'output': str(output), 'user': runtime.USER, 'candidate_provenance': producer['provenance']}
    for name, value in [('final-artifacts.json', final), ('candidate-selection.json', producer), ('contract.json', contract)]:
        fresh(root / name, json.dumps(value, sort_keys=True, indent=2) + '\n', 0o444)
    return contract


def retain(selected: dict, checkout: Path, facts: dict, output: Path) -> dict:
    require_partial(selected)
    account = runtime.context(selected, checkout, facts)
    contract = read(runtime.ROOT / 'contract.json')
    leaf = Path(account.pw_dir) / 'installed-observations'
    report = read(leaf / 'diagnostic.json')
    expected = {'format_version': 1, 'scope': 'partial-native-first-launch-diagnostic', 'source_commit': selected['source_commit'],
                'source_tree': selected['source_tree'], 'driver_tree': selected['controller_tree'],
                'candidate_provenance': contract['candidate_provenance'], 'complete_candidate': False,
                'application_qualified': False, 'qualification_authority': False, 'publication_authority': False}
    if (contract['controller_commit'] != selected['controller_commit'] or contract['output'] != str(leaf)
            or any(report.get(key) != value or type(report.get(key)) is not type(value) for key, value in expected.items())
            or report.get('status') not in {'passed', 'failed', 'unverified'}):
        raise ValueError('partial diagnostic differs from its frozen source, controller or authority')
    first = report.get('first_launch')
    if ((first is not None and (not isinstance(first, dict) or first.get('execution_nonce') != contract['execution_nonce']))
            or (report['status'] == 'passed' and first is None)):
        raise ValueError('partial first launch differs from its fresh execution nonce')
    if output.exists() or output.is_symlink() or output.resolve().is_relative_to(checkout.resolve()):
        raise ValueError('partial observations require a fresh independent retention path')
    output.mkdir(mode=0o755)
    retain_tree(leaf, output / 'diagnostic', root_files={'diagnostic.json'}, root_dirs={'evidence'},
                limits=Limits(max_files=4096, max_bytes=256 << 20, max_file=16 << 20))
    for name in ('contract.json', 'candidate-selection.json', 'final-artifacts.json'):
        shutil.copyfile(runtime.ROOT / name, output / name)
    fresh(output / 'rehearsal.json', json.dumps({'format_version': 1, 'selection': selected, 'desktop': facts,
          'scope': expected['scope'], 'application_qualified': False, 'physical_gpu_qualified': False,
          'qualification_authority': False, 'publication_authority': False}, sort_keys=True) + '\n', 0o444)
    for path in output.rglob('*'):
        path.chmod(0o755 if path.is_dir() else 0o444)
    return report
