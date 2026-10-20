"""Select and authorize one immutable scanner build tool on a disposable Ubuntu runner."""
from __future__ import annotations

import hashlib
import os
from pathlib import Path
import platform
import re
import stat
import subprocess

PNAME = 'painted-wolf-code-scanner-build-tools'
STORE = r'/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[A-Za-z0-9+._-]+'
SYSTEMS = {'x86_64': 'x86_64-linux', 'aarch64': 'aarch64-linux'}
POLICY = Path('/proc/sys/kernel/apparmor_restrict_unprivileged_userns')
PROFILES = Path('/sys/kernel/security/apparmor/profiles')


def scanner_derivation(graph: dict, root: str, system: str) -> dict:
    if (system not in SYSTEMS.values() or not isinstance(graph, dict)
            or not 0 < len(graph) <= 100000 or root not in graph):
        raise ValueError('scanner selection requires the complete selected native derivation graph')
    if not isinstance(graph[root], dict) or graph[root].get('system') != system:
        raise ValueError('application derivation is not for the selected native system')
    pending, visited, matches = [root], set(), []
    while pending:
        path = pending.pop()
        if path in visited:
            continue
        visited.add(path)
        if not isinstance(path, str) or re.fullmatch(STORE + r'\.drv', path) is None:
            raise ValueError('derivation graph contains an invalid store identity')
        row = graph.get(path)
        if not isinstance(row, dict) or not isinstance(row.get('inputDrvs'), dict):
            raise ValueError('derivation graph is incomplete')
        pending.extend(row['inputDrvs'])
        environment = row.get('env')
        if not isinstance(environment, dict):
            raise ValueError('derivation graph omitted its typed environment')
        if environment.get('pname') != PNAME:
            continue
        output = row.get('outputs', {}).get('out', {})
        value = output.get('path')
        if (row.get('system') != system or not isinstance(value, str)
                or re.fullmatch(STORE, value) is None
                or not Path(value).name[33:].startswith(PNAME + '-')
                or environment.get('out') != value):
            raise ValueError('scanner tools do not have one fixed native output identity')
        matches.append({'derivation': path, 'output': value,
                        'executable': value + '/bin/opengrep-artifact', 'system': system})
    if len(matches) != 1:
        raise ValueError('selected application must depend on exactly one native scanner build tool')
    return matches[0]


def profile_source(executable: str) -> tuple[str, str]:
    if (not isinstance(executable, str)
            or re.fullmatch(STORE + '/bin/opengrep-artifact', executable) is None
            or not Path(executable).parts[3][33:].startswith(PNAME + '-')):
        raise ValueError('scanner user-namespace permission requires an exact build-tools store executable')
    name = 'paintedwolf-nix-scanner-' + hashlib.sha256(executable.encode()).hexdigest()
    # Ubuntu's unconfined flag preserves existing access while permitting userns
    # for this executable alone; it does not remove the scanner's own sandbox.
    source = ('abi <abi/4.0>,\ninclude <tunables/global>\n'
              f'profile {name} "{executable}" flags=(unconfined) {{\n  userns,\n}}\n')
    return name, source


def authorize_scanner(executable: str) -> dict:
    if (platform.system() != 'Linux' or platform.machine() not in SYSTEMS
            or os.environ.get('GITHUB_ACTIONS') != 'true'
            or os.environ.get('RUNNER_ENVIRONMENT') != 'github-hosted'):
        raise ValueError('scanner build permission is limited to disposable native GitHub-hosted runners')
    name, source = profile_source(executable)
    path = Path(executable)
    information = path.lstat()
    if (path.resolve() != path or not stat.S_ISREG(information.st_mode)
            or information.st_uid != 0 or information.st_mode & 0o022
            or not information.st_mode & 0o111):
        raise ValueError('scanner executable must be a root-owned immutable physical store file')
    with path.open('rb') as stream:
        digest = hashlib.file_digest(stream, 'sha256').hexdigest()
    before = POLICY.read_text().strip() if POLICY.exists() else None
    report = {'executable': executable, 'sha256': digest, 'host_userns_restriction': before,
              'policy_changed': False, 'profile_loaded': False}
    if before != '1':
        return report
    # No profile files or global sysctls are replaced. This kernel profile dies
    # with the disposable VM and is attached to one immutable executable path.
    result = subprocess.run(['sudo', '-n', 'apparmor_parser', '--replace'], input=source.encode(),
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
    if result.returncode:
        raise OSError('could not authorize the exact scanner build executable in AppArmor')
    profiles = PROFILES.read_text().splitlines()
    if (not any(line == name + ' (unconfined)' or line == name + ' (enforce)' for line in profiles)
            or POLICY.read_text().strip() != before):
        raise ValueError('scanner profile was not loaded without changing global userns policy')
    report.update({'profile_loaded': True, 'profile': name,
                   'profile_sha256': hashlib.sha256(source.encode()).hexdigest()})
    return report
