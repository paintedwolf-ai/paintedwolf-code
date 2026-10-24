"""Freeze a package-only dependency baseline, then restore product absence."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path

from ..installed_environment import package_inventory, package_inventory_arguments
from ..installed_runtime import bind
from .native_workflows import PACKAGE, native_lock, prepare_install, query
from .package_native import command, current_package, operations
from .package_payload import inspect
from .records import CaseUnavailable, retain

PROCESS_OBSERVER = r'''
import json,sys,os
from pathlib import Path
if os.getuid()!=0 or os.geteuid()!=0: raise ValueError('process observer requires administrator identity')
identities=json.loads(sys.argv[1])
if not isinstance(identities,list) or not 1<=len(identities)<=128 or any(not isinstance(row,list) or len(row)!=2 or any(type(v)is not int or v<0 for v in row) for row in identities): raise ValueError('invalid selected image identities')
selected={tuple(row) for row in identities};matches=[]
processes=[p for p in Path('/proc').iterdir() if p.name.isdecimal()]
if len(processes)>65536: raise ValueError('process observer exceeds fixed bound')
for process in processes:
 try:
  with (process/'stat').open('rb') as stream: before=stream.read(8193)
  image=(process/'exe').stat()
  if (image.st_dev,image.st_ino) not in selected: continue
  with (process/'stat').open('rb') as stream: after=stream.read(8193)
  if len(before)>8192 or len(after)>8192: raise ValueError('process identity exceeds bound')
  start=before.rpartition(b') ')[2].split()[19]
  if start!=after.rpartition(b') ')[2].split()[19]: raise ValueError('selected process changed identity')
  matches.append({'pid':int(process.name),'start_time_ticks':int(start)})
 except (FileNotFoundError,ProcessLookupError): pass
print(json.dumps({'format_version':1,'matching_processes':matches},sort_keys=True))
'''


def finish(output, report):
    (output / 'bootstrap.json').write_text(json.dumps(report, sort_keys=True, indent=2) + '\n')
    return report


def bootstrap(final, source, artifact_id, packages: Path, install: Path, output: Path, execution: dict):
    from ..installed_input.protocol import binding as execution_binding
    execution_binding(execution)
    if execution['source_commit'] != source or execution['artifact_id'] != artifact_id:
        raise ValueError('bootstrap execution differs from the selected package source')
    output.mkdir(parents=True, mode=0o700, exist_ok=False)
    evidence = output / 'evidence'
    evidence.mkdir()
    inputs = {'native_packages': {'format_version': 1, 'current_package': str(packages / artifact_id),
        'installation': {'operation': 'fresh-install', 'expected_absent': True}}}
    installed = prepare_install(final, source, artifact_id, packages, install, inputs, evidence)
    report = {'format_version': 1, 'execution': execution, 'status': 'failed', 'bootstrap_only': True,
        'gui_initialized': False, 'pristine_operating_system': False, 'installation': installed,
        'snapshot': None, 'inventory': None, 'removal': None,
        'qualification_authority': False, 'publication_authority': False}
    if installed['status'] != 'passed':
        report['status'] = installed['status']
    else:
        try:
            binding = bind(final, source, artifact_id, packages, install)
        except (OSError, ValueError) as error:
            report['recovery_required'] = True
            report['preparation_error_type'] = type(error).__name__
            return finish(output, report)
        binding['case_inputs'] = inputs
        observations = {'phase': 'package-only-bootstrap-removal', 'recovery_required': True}
        streams, status = {}, 'failed'

        def run(label, arguments):
            code, stdout, stderr = command(arguments)
            observations[label] = {'arguments': arguments, 'exit_code': code}
            for name, raw in (('stdout', stdout), ('stderr', stderr)):
                remaining = max(0, min(8 << 20, (24 << 20) - sum(map(len, streams.values()))))
                streams[label + '-' + name] = raw[:remaining]
                if len(raw) > remaining:
                    raise ValueError('bootstrap removal evidence exceeds its fixed bound')
            return code, stdout.decode('utf-8', 'strict').strip()

        with native_lock():
            try:
                package = current_package(binding)
                family = binding['artifact']['installation']
                entries = inspect(package, family, binding['artifact']['identity']['sha256'])
            except (OSError, ValueError) as error:
                report['recovery_required'] = True
                report['preparation_error_type'] = type(error).__name__
                return finish(output, report)
            images = [[value['device'], value['inode']] for value in binding['images'].values()]
            owner_verified = False
            try:
                # This independent observation precedes the qualification leaf.
                owner_query = {'deb': ['dpkg-query', '--show', '--showformat=${Package}', PACKAGE],
                    'rpm': ['rpm', '--query', '--queryformat', '%{NAME}', PACKAGE],
                    'pacman': ['pacman', '--query', '--quiet', PACKAGE]}[family]
                code, owner = run('installed-owner', owner_query)
                if code or owner != PACKAGE:
                    raise ValueError('bootstrap selected native ownership changed before removal')
                owner_verified = True
                inventory = package_inventory(family)
                if inventory['exit_code'] or inventory.get('stderr'):
                    raise CaseUnavailable('bootstrap package inventory was not completely observed')
                if len(json.dumps(inventory).encode()) > 1 << 20:
                    raise ValueError('bootstrap inventory exceeds its independent observation bound')
                report['inventory'] = inventory
                report['snapshot'] = {'kind': 'package-inventory', 'value': hashlib.sha256(
                    json.dumps(inventory, sort_keys=True).encode()).hexdigest(), 'channel': None}
            except (OSError, ValueError) as error:
                observations['inventory_error_type'] = type(error).__name__
            finally:
                try:
                    if not owner_verified:
                        raise ValueError('bootstrap removal refused after native ownership changed')
                    # Revalidate sealed bytes after inventory, immediately before destructive removal.
                    bind(final, source, artifact_id, packages, install)
                    observations['selected_payload_revalidated'] = True
                    versions = {
                        'deb': (['dpkg-deb', '--field', str(package), 'Version'],
                                ['dpkg-query', '--show', '--showformat=${Version}', PACKAGE]),
                        'rpm': (['rpm', '--query', '--package', '--queryformat', '%{EPOCHNUM}:%{VERSION}-%{RELEASE}.%{ARCH}', str(package)],
                                ['rpm', '--query', '--queryformat', '%{EPOCHNUM}:%{VERSION}-%{RELEASE}.%{ARCH}', PACKAGE]),
                        'pacman': (['pacman', '--query', '--file', str(package)], ['pacman', '--query', PACKAGE])}
                    selected_query, installed_query = versions[family]
                    code, selected_version = run('selected-version', selected_query)
                    actual_code, actual_version = run('installed-version', installed_query)
                    if code or actual_code or not selected_version or actual_version != selected_version:
                        raise ValueError('bootstrap native version changed before owned removal')
                    remove, _ = operations(family, package, PACKAGE)
                    if family == 'deb':
                        remove = ['sudo', '-n', 'dpkg', '--purge', PACKAGE]
                    code, _ = run('remove', remove)
                    if code:
                        raise ValueError('bootstrap could not remove its selected owned package')
                    controls = {'deb': (['dpkg-query', '--show', '--showformat=${Package}', 'dpkg'], 'dpkg'),
                        'rpm': (['rpm', '--query', '--queryformat', '%{NAME}', 'rpm'], 'rpm'),
                        'pacman': (['pacman', '--query', '--quiet', 'pacman'], 'pacman')}
                    arguments, expected = controls[family]
                    code, observed = run('database-control', arguments)
                    if code or observed != expected:
                        raise CaseUnavailable('bootstrap package database failed its positive control')
                    code, _ = run('absence', query(family))
                    remaining = [entry.name for entry in entries.values()
                        if entry.kind != 'directory' and os.path.lexists(Path('/') / entry.name)]
                    observations['remaining_payload_paths'] = remaining[:128]
                    observations['profile_absent'] = not os.path.lexists(Path.home() / '.config/paintedwolf')
                    observations['webview_profile_absent'] = not os.path.lexists(Path.home() / '.local/share/dev.paintedwolf.code')
                    if code != 1 or remaining or not observations['profile_absent'] or not observations['webview_profile_absent']:
                        raise ValueError('bootstrap did not restore verified package, payload and profile absence')
                    code, raw = run('process-absence', ['sudo', '-n', '/usr/bin/python3', '-c', PROCESS_OBSERVER,
                        json.dumps(images, separators=(',', ':'))])
                    processes = json.loads(raw)
                    if code or processes != {'format_version': 1, 'matching_processes': []}:
                        raise ValueError('bootstrap retained a selected application or service process')
                    observations['selected_processes_absent'] = True
                    observations['recovery_required'] = False
                    status = 'passed' if report['snapshot'] is not None else 'unverified'
                except (OSError, ValueError) as error:
                    observations['error_type'] = type(error).__name__
            result = retain(binding, 'installation', 'bootstrap-removal', evidence, status, observations, streams)
            report['removal'] = result
            report['status'] = status
    return finish(output, report)


def verify(root: Path, execution: dict, artifact: dict) -> dict:
    from ..installed_execution import read
    from ..installed_evidence import object_fields, retained_file
    from .desktop import observed_journal
    report = object_fields(read(root / 'bootstrap.json'), {'format_version', 'execution', 'status',
        'bootstrap_only', 'gui_initialized', 'pristine_operating_system', 'installation', 'snapshot', 'inventory', 'removal',
        'qualification_authority', 'publication_authority'})
    if (type(report['format_version']) is not int or report['format_version'] != 1
            or report['execution'] != execution or report['status'] != 'passed'
            or report['bootstrap_only'] is not True or report['gui_initialized'] is not False
            or report['pristine_operating_system'] is not False
            or report['qualification_authority'] is not False or report['publication_authority'] is not False):
        raise ValueError('bootstrap differs from its exact package-only preparation execution')
    binding = {'source_commit': execution['source_commit'], 'artifact': artifact}
    installed = observed_journal(binding, root / 'evidence', report['installation'], 'preinstallation-transaction')
    if (installed['status'] != 'passed' or installed['observations'].get('profile_absent') is not True
            or installed['observations'].get('webview_profile_absent') is not True):
        raise ValueError('bootstrap installation did not begin with absent product profiles')
    removed = object_fields(report['removal'], {'id', 'status', 'evidence'})
    if removed['id'] != 'bootstrap-removal' or removed['status'] != 'passed' or not 1 <= len(removed['evidence']) <= 17:
        raise ValueError('bootstrap removal did not pass with bounded actual journals')
    journal = None
    for item in removed['evidence']:
        retained_file(item, root / 'evidence')
        if not item['path'].startswith('installation/bootstrap-removal/'):
            raise ValueError('bootstrap removal crosses its retained evidence boundary')
        if item['path'] == 'installation/bootstrap-removal/execution.json':
            journal = read(root / 'evidence' / item['path'])
    if not isinstance(journal, dict):
        raise ValueError('bootstrap removal omitted its actual operation journal')
    observations = journal.get('observations', {})
    expected = {'format_version': 1, 'source_commit': execution['source_commit'], 'artifact': artifact,
        'capability': 'installation', 'case': 'bootstrap-removal', 'status': 'passed', 'observations': observations}
    if (journal != expected or observations.get('recovery_required') is not False
            or any(observations.get(name) is not True for name in
                   ('profile_absent', 'webview_profile_absent', 'selected_processes_absent'))
            or observations.get('remaining_payload_paths') != []):
        raise ValueError('bootstrap did not restore absence without initializing the application')
    inventory = object_fields(report['inventory'], {'arguments', 'exit_code', 'stdout', 'stderr'})
    if (inventory['arguments'] != package_inventory_arguments(artifact['installation'])
            or type(inventory['exit_code']) is not int or inventory['exit_code'] != 0
            or not isinstance(inventory['stdout'], str) or not inventory['stdout'] or inventory['stderr'] != ''):
        raise ValueError('bootstrap inventory is not the complete independent native package observation')
    expected_snapshot = {'kind': 'package-inventory', 'value': hashlib.sha256(
        json.dumps(inventory, sort_keys=True).encode()).hexdigest(), 'channel': None}
    if report['snapshot'] != expected_snapshot:
        raise ValueError('bootstrap snapshot differs from its independently retained postinstall inventory')
    return report


def main(arguments):
    from ..installed_execution import read, current_tree
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--final-artifacts', type=Path, required=True)
    parser.add_argument('--execution-request', type=Path, required=True)
    parser.add_argument('--file-root', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args(arguments)
    execution = read(args.execution_request)
    if current_tree(Path(__file__).resolve().parents[3]) != execution['driver_tree']:
        raise ValueError('bootstrap differs from its selected immutable controller tree')
    report = bootstrap(read(args.final_artifacts), execution['source_commit'], execution['artifact_id'],
        args.file_root, Path('/opt/painted-wolf-code'), args.output, execution)
    return 0 if report['status'] == 'passed' else 2


if __name__ == '__main__':
    import sys
    raise SystemExit(main(sys.argv[1:]))
