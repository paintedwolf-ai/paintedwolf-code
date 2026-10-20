"""Stage and retain real native installed interactions on a disposable hosted desktop."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import pwd
import secrets
import shutil
import stat
import subprocess

from .hosted_desktop import USER, fresh, validate_host
from .hosted_installed_plan import controller, read as selection
from .installed_artifact_bundle import Limits, retain_tree
from .installed_campaign import verify_execution_receipt
from .installed_capacity import limits
from .installed_environment import release
from .installed_evidence import artifacts
from .installed_execution import read
from .installed_results import admit
from .installed_runtime import file_identity, stable
from .installed_variants import requirements
from .support_matrix import CATALOG

ROOT = Path('/var/lib/paintedwolf-installed-rehearsal')
INSTALL = Path('/opt/painted-wolf-code')


def command(arguments: list[str], timeout=1200) -> str:
    result = subprocess.run(arguments, stdin=subprocess.DEVNULL, capture_output=True, timeout=timeout,
                            env={'PATH': '/usr/sbin:/usr/bin:/sbin:/bin', 'DEBIAN_FRONTEND': 'noninteractive', 'LC_ALL': 'C.UTF-8'})
    if result.returncode or len(result.stdout) + len(result.stderr) > 16 << 20:
        raise ValueError('fixed hosted installation operation failed: ' + arguments[0])
    return result.stdout.decode('utf-8', 'strict').strip()


def context(selected: dict, checkout: Path, facts: dict):
    import platform
    validate_host(os.environ, os.getuid(), platform.machine(), release())
    controller(checkout, selected)
    account = pwd.getpwnam(USER)
    if (facts.get('controller_commit') != selected['controller_commit'] or facts.get('workflow_commit') != os.environ['GITHUB_SHA']
            or facts.get('github_run_id') != os.environ['GITHUB_RUN_ID'] or facts.get('user') != USER
            or facts.get('uid') != account.pw_uid or facts.get('display_protocol') != 'x11'
            or facts.get('application_qualified') is not False or facts.get('physical_gpu_qualified') is not False
            or facts.get('architecture') != {'amd64': 'x86_64', 'arm64': 'aarch64'}[selected['architecture']]):
        raise ValueError('hosted runtime differs from its separately observed native desktop')
    return account


def candidate_package(selected: dict, candidate: Path):
    final, producer = read(candidate / 'final-artifacts.json'), read(candidate / 'candidate-selection.json')
    if (producer.get('qualification_authority') is not False or producer.get('publication_authority') is not False
            or producer.get('partial') is not selected['partial']
            or producer.get('source', {}).get('source_commit') != selected['source_commit']
            or producer.get('source', {}).get('source_tree') != selected['source_tree']):
        raise ValueError('hosted package inputs differ from the selected nonpublishing candidate')
    provenance = producer.get('provenance', {})
    if (provenance.get('run_id') != selected['candidate_run_id'] or provenance.get('artifact_id') != selected['candidate_artifact_id']
            or provenance.get('controller_commit') != selected['candidate_controller_commit']):
        raise ValueError('hosted packages differ from the independently selected candidate producer')
    chosen = [item for item in artifacts(final, selected['source_commit']).values()
              if item['installation'] == 'deb' and item['architecture'] == selected['architecture']]
    if len(chosen) != 1:
        raise ValueError('hosted Ubuntu rehearsal requires exactly one selected native Debian package')
    artifact = chosen[0]
    source = candidate / 'packages' / artifact['id']
    actual = file_identity(source)
    if source.is_symlink() or actual['sha256'] != artifact['identity']['sha256']:
        raise ValueError('hosted Debian package differs from the selected candidate bytes')
    return final, producer, artifact, source, actual


def prepare(selected: dict, checkout: Path, candidate: Path, facts: dict) -> dict:
    if selected.get('partial') is not False:
        raise ValueError('complete hosted preparation requires its independently selected complete candidate')
    account = context(selected, checkout, facts)
    if ROOT.exists() or ROOT.is_symlink() or INSTALL.exists() or INSTALL.is_symlink():
        raise ValueError('hosted installation must preserve any existing runtime or campaign')
    final, producer, artifact, source, actual = candidate_package(selected, candidate)
    marker = Path('/etc/paintedwolf-qualification.json')
    info = marker.lstat()
    expected_marker = {'format_version': 1, 'desktop': 'gnome', 'package': str(INSTALL)}
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022 or read(marker) != expected_marker or type(read(marker).get('format_version')) is not int):
        raise ValueError('hosted desktop ownership marker changed before package installation')
    ROOT.mkdir(mode=0o755)
    packages = ROOT / 'packages'
    packages.mkdir(mode=0o755)
    target = packages / artifact['id']
    shutil.copyfile(source, target)
    target.chmod(0o444)
    copied = file_identity(target)
    if copied['sha256'] != actual['sha256'] or copied['bytes'] != actual['bytes']:
        raise ValueError('hosted Debian package changed while entering administrator custody')
    if (command(['/usr/bin/dpkg-deb', '--field', str(target), 'Package']) != 'painted-wolf-code'
            or command(['/usr/bin/dpkg-deb', '--field', str(target), 'Architecture']) != selected['architecture']):
        raise ValueError('hosted package native metadata differs from its selected package and CPU')
    catalog = read(CATALOG)
    environment = 'ubuntu-floor-gnome-x11'
    variants = [item for item in requirements(catalog, environment, 'windowing')
                if item['release']['version_id'] == '24.04' and item['scales_percent'] == [100]]
    if len(variants) != 1:
        raise ValueError('hosted Ubuntu baseline differs from the independently declared support catalog')
    nonce = secrets.token_hex(16)
    request = {'format_version': 1, 'execution_nonce': nonce, 'source_commit': selected['source_commit'],
               'driver_tree': selected['controller_tree'], 'artifact_id': artifact['id'],
               'environment_id': environment, 'variant_id': variants[0]['id']}
    inputs = {'native_packages': {'format_version': 1, 'current_package': str(target),
                                 'installation': {'operation': 'fresh-install', 'expected_absent': True}}}
    output = Path(account.pw_dir) / 'installed-observations'
    if output.exists() or output.is_symlink():
        raise ValueError('hosted graphical account already owns an installed observation campaign')
    bootstrap = Path(account.pw_dir) / 'native-bootstrap'
    if bootstrap.exists() or bootstrap.is_symlink():
        raise ValueError('hosted graphical account already owns a bootstrap transaction')
    contract = {**request, 'snapshot': None, 'output': str(output), 'user': USER,
                'bootstrap_output': str(bootstrap), 'bootstrap_frozen': False,
                'controller_commit': selected['controller_commit'], 'candidate_selection': producer}
    for name, value in [('final-artifacts.json', final), ('case-inputs.json', inputs),
                        ('execution-request.json', request), ('contract.json', contract)]:
        fresh(ROOT / name, json.dumps(value, sort_keys=True, indent=2) + '\n', 0o444)
    return contract


def freeze(selected: dict, checkout: Path, facts: dict) -> dict:
    """Freeze preparation observations before the qualifying fresh-install leaf."""
    from .installed_cases.native_bootstrap import verify
    if selected.get('partial') is not False:
        raise ValueError('complete hosted snapshot freeze refuses partial diagnostic packages')
    account = context(selected, checkout, facts)
    contract_path = ROOT / 'contract.json'
    original_identity = (stable(contract_path.lstat()), file_identity(contract_path))
    original_bytes = contract_path.read_bytes()
    contract = read(contract_path)
    if (contract['controller_commit'] != selected['controller_commit'] or contract['bootstrap_frozen'] is not False
            or contract['snapshot'] is not None or Path(contract['bootstrap_output']) != Path(account.pw_dir) / 'native-bootstrap'):
        raise ValueError('hosted bootstrap differs from its pending preparation contract')
    original = Path(contract['bootstrap_output'])
    info = original.lstat()
    if (original.resolve() != original or not stat.S_ISDIR(info.st_mode)
            or info.st_uid != account.pw_uid or stat.S_IMODE(info.st_mode) != 0o700):
        raise ValueError('hosted bootstrap is not the fresh graphical user transaction')
    request, final = read(ROOT / 'execution-request.json'), read(ROOT / 'final-artifacts.json')
    from .installed_input.protocol import binding as execution_binding
    execution_binding(request)
    if (request['source_commit'] != selected['source_commit'] or request['driver_tree'] != selected['controller_tree']
            or any(request[key] != contract[key] for key in request)):
        raise ValueError('hosted bootstrap request differs from its selected preleaf execution')
    from .hosted_bootstrap import verify as verify_supervision
    verify_supervision(ROOT, selected, account, request)
    artifact = artifacts(final, selected['source_commit'])[contract['artifact_id']]
    retained = ROOT / 'bootstrap'
    retain_tree(original, retained, root_files={'bootstrap.json'}, root_dirs={'evidence'},
                limits=Limits(max_files=64, max_bytes=64 << 20, max_file=32 << 20))
    observed = verify(retained, request, artifact)
    contract.update(snapshot=observed['snapshot'], bootstrap_frozen=True)
    replacement = ROOT / 'frozen-contract.json'
    fresh(replacement, json.dumps(contract, sort_keys=True, indent=2) + '\n', 0o444)
    if ((stable(contract_path.lstat()), file_identity(contract_path)) != original_identity or contract_path.read_bytes() != original_bytes):
        raise ValueError('hosted bootstrap contract changed before snapshot freeze')
    os.replace(replacement, ROOT / 'contract.json')
    return contract


def retain(selected: dict, checkout: Path, facts: dict, output: Path) -> dict:
    if selected.get('partial') is not False:
        raise ValueError('complete hosted campaign retention refuses partial diagnostic packages')
    account = context(selected, checkout, facts)
    contract, final = read(ROOT / 'contract.json'), read(ROOT / 'final-artifacts.json')
    if (contract['controller_commit'] != selected['controller_commit'] or contract['source_commit'] != selected['source_commit']
            or Path(contract['output']) != Path(account.pw_dir) / 'installed-observations'
            or contract.get('bootstrap_frozen') is not True):
        raise ValueError('hosted execution contract differs from the selected controller and account')
    if output.exists() or output.is_symlink() or output.resolve().is_relative_to(checkout.resolve()):
        raise ValueError('hosted evidence requires a fresh independent retention path')
    catalog = read(CATALOG)
    bound = limits(catalog, final)
    capacity = Limits(**{key: bound[key] for key in ('max_files', 'max_bytes', 'max_file')})
    output.mkdir(mode=0o755)
    retain_tree(ROOT / 'bootstrap', output / 'bootstrap', root_files={'bootstrap.json'}, root_dirs={'evidence'},
                limits=Limits(max_files=64, max_bytes=64 << 20, max_file=32 << 20))
    for name in ('bootstrap-supervision.json', 'bootstrap-invocation.json'):
        shutil.copyfile(ROOT / name, output / name)
    target = output / 'target'
    target.mkdir(mode=0o755)
    retain_tree(Path(contract['output']), target / 'leaf',
                root_files={'campaign.json', 'binding.json', 'environment.json', 'environment-facts.json',
                            'driver-registry.json', 'variant.json', 'execution.json'},
                root_dirs={'evidence'}, limits=capacity)
    shutil.copyfile(ROOT / 'execution-request.json', target / 'execution-request.json')
    verify_execution_receipt(target, contract, selected['source_commit'], selected['controller_tree'])
    campaign = read(target / 'leaf/campaign.json')
    admission = admit(catalog, campaign, final, selected['source_commit'], target / 'leaf/evidence')
    counts = {status: sum(case['status'] == status for row in campaign['results'] for case in row['cases'])
              for status in ('passed', 'failed', 'unverified')}
    report = {'format_version': 1, 'selection': selected, 'desktop': facts, 'case_counts': counts,
              'complete_matrix_accepted': admission['accepted'], 'application_qualified': False,
              'physical_gpu_qualified': False, 'publication_authority': False, 'qualification_authority': False}
    for name, value in [('rehearsal.json', report), ('admission.json', admission), ('final-artifacts.json', final)]:
        fresh(output / name, json.dumps(value, sort_keys=True, indent=2) + '\n', 0o444)
    # Actions uploader runs as the original runner account; it receives read-only observations.
    for path in output.rglob('*'):
        path.chmod(0o755 if path.is_dir() else 0o444)
    return report


def main(arguments=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=('prepare', 'bootstrap', 'freeze', 'retain', 'prepare-diagnostic', 'retain-diagnostic'))
    parser.add_argument('--selection', type=Path, required=True)
    parser.add_argument('--checkout', type=Path, required=True)
    parser.add_argument('--facts', type=Path, required=True)
    parser.add_argument('--candidate', type=Path)
    parser.add_argument('--output', type=Path)
    args = parser.parse_args(arguments)
    try:
        selected, facts = selection(args.selection), read(args.facts)
        if args.mode in {'prepare-diagnostic', 'retain-diagnostic'}:
            from .hosted_installed_diagnostic import prepare as diagnostic_prepare, retain as diagnostic_retain
            if args.mode == 'prepare-diagnostic' and args.candidate is not None:
                diagnostic_prepare(selected, args.checkout, args.candidate, facts)
            elif args.mode == 'retain-diagnostic' and args.output is not None:
                diagnostic_retain(selected, args.checkout, facts, args.output)
            else:
                raise ValueError('diagnostic mode requires its explicit input or output path')
        elif args.mode == 'prepare' and args.candidate is not None:
            prepare(selected, args.checkout, args.candidate, facts)
        elif args.mode == 'bootstrap':
            if selected.get('partial') is not False:
                raise ValueError('complete bootstrap refuses partial diagnostic packages')
            from .hosted_bootstrap import supervise
            supervise(ROOT, selected, context(selected, args.checkout, facts))
        elif args.mode == 'freeze':
            freeze(selected, args.checkout, facts)
        elif args.mode == 'retain' and args.output is not None:
            retain(selected, args.checkout, facts, args.output)
        else:
            raise ValueError('hosted runtime mode requires its explicit input or output path')
    except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError) as error:
        parser.exit(1, 'Hosted installed rehearsal refused: ' + str(error) + '\n')
    return 0
