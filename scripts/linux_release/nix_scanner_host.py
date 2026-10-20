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
    observed = subprocess.run(['sudo', '-n', 'cat', '/sys/kernel/security/apparmor/profiles'],
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
    if observed.returncode or len(observed.stdout) > 8 << 20:
        raise ValueError('scanner profile readback could not complete within its bound')
    try:
        profiles = observed.stdout.decode('utf-8').splitlines()
    except UnicodeDecodeError:
        raise ValueError('scanner profile readback is not UTF-8') from None
    if (not any(line == name + ' (unconfined)' or line == name + ' (enforce)' for line in profiles)
            or POLICY.read_text().strip() != before):
        raise ValueError('scanner profile was not loaded without changing global userns policy')
    report.update({'profile_loaded': True, 'profile': name,
                   'profile_sha256': hashlib.sha256(source.encode()).hexdigest()})
    return report
