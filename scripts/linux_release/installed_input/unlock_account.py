"""Disposable 058 QA account validation, private chpasswd preparation and teardown."""
from __future__ import annotations

import argparse
import grp
import os
from pathlib import Path
import pwd
import re
import stat
import sys

from . import protocol, unlock_wire
from .mailbox import decode
from ..installed_cases import native_command
from ..installed_cases.records import CaseUnavailable

ACCOUNT_NAME = re.compile(r'pwqual058[a-z0-9]{0,12}')
FORBIDDEN_NAMES = {'root', 'pwtest'}


def validate_account_shape(account: dict) -> dict:
    if (not isinstance(account, dict) or set(account) != {'name', 'uid', 'gid', 'home'}
            or not isinstance(account['name'], str) or account['name'] in FORBIDDEN_NAMES
            or account['name'].startswith('pwqual055') or not ACCOUNT_NAME.fullmatch(account['name'])
            or type(account['uid']) is not int or not 30000 <= account['uid'] < 1 << 31
            or type(account['gid']) is not int or account['gid'] != account['uid']
            or account['home'] != '/home/' + account['name']):
        raise ValueError('OS input has no exact dedicated QA account identity')
    return account


def _verify_marker(vm_id: str, marker_path: Path = Path('/etc/paintedwolf-input-qualification.json')):
    if not isinstance(vm_id, str) or not protocol.IDENTIFIER.fullmatch(vm_id):
        raise ValueError('native input requires its selected VM identity')
    descriptor = os.open(marker_path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        before = os.fstat(descriptor)
        if (not stat.S_ISREG(before.st_mode) or before.st_uid != 0 or before.st_gid != 0
                or before.st_mode & 0o222 or before.st_size > protocol.MAX_DOCUMENT
                or marker_path.resolve() != marker_path):
            raise ValueError('native input requires its root-owned readonly VM marker')
        raw = os.read(descriptor, protocol.MAX_DOCUMENT + 1)
        after = os.fstat(descriptor)
        identity = lambda value: (value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns, value.st_ctime_ns)
        if identity(before) != identity(after) or identity(after) != identity(marker_path.lstat()):
            raise ValueError('native input VM marker changed during observation')
        document = decode(raw)
        if type(document.get('format_version')) is not int or document != {'format_version': 1, 'vm_id': vm_id}:
            raise ValueError('native input guest differs from the selected VM identity')
    finally:
        os.close(descriptor)


def _verify_directory(path: Path, *, uid: int, gid: int, mode: int = 0o755):
    info = path.lstat()
    if (not stat.S_ISDIR(info.st_mode) or info.st_uid != uid or info.st_gid != gid
            or stat.S_IMODE(info.st_mode) != mode or path.resolve() != path):
        raise ValueError(f'dedicated QA account directory failed physical verification: {path}')


def _verify_root_binary(path: Path):
    info = path.lstat()
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_gid != 0
            or info.st_mode & 0o022 or not info.st_mode & 0o111 or path.resolve() != path):
        raise ValueError(f'dedicated QA account helper is not a root-owned executable: {path}')


def validate_account(account: dict, *, vm_id: str | None = None,
                     marker_path: Path = Path('/etc/paintedwolf-input-qualification.json')) -> dict:
    validate_account_shape(account)
    if os.geteuid() != 0:
        raise ValueError('dedicated QA account custody requires root execution')
    if vm_id is not None:
        _verify_marker(vm_id, marker_path=marker_path)
    by_name = pwd.getpwnam(account['name'])
    by_uid = pwd.getpwuid(account['uid'])
    group = grp.getgrgid(account['gid'])
    if (by_name.pw_name != account['name'] or by_name.pw_uid != account['uid']
            or by_name.pw_gid != account['gid'] or by_name.pw_dir != account['home']
            or by_uid.pw_name != account['name'] or group.gr_name != account['name']
            or group.gr_gid != account['gid']):
        raise ValueError('dedicated QA account passwd/group records do not match')
    home = Path(account['home'])
    _verify_directory(home.parent, uid=0, gid=0, mode=0o755)
    _verify_directory(home, uid=account['uid'], gid=account['gid'], mode=0o755)
    _verify_directory(home / '.config', uid=account['uid'], gid=account['gid'], mode=0o755)
    return account


def shadow_state(username: str, *, shadow_path: Path = Path('/etc/shadow')) -> str:
    if (not isinstance(username, str) or username in FORBIDDEN_NAMES
            or username.startswith('pwqual055') or not ACCOUNT_NAME.fullmatch(username)):
        raise ValueError('OS input has no exact dedicated QA account identity')
    descriptor = os.open(shadow_path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        info = os.fstat(descriptor)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0
                or info.st_mode & 0o022 or shadow_path.resolve() != shadow_path):
            raise ValueError('shadow database lost its root-owned regular file identity')
        raw = bytearray()
        while chunk := os.read(descriptor, 65536):
            if len(raw) + len(chunk) > 1 << 20:
                raise ValueError('shadow database exceeds its bounded size')
            raw.extend(chunk)
    finally:
        os.close(descriptor)
    try:
        matches = []
        prefix = username.encode('ascii') + b':'
        for line in raw.splitlines():
            if line.startswith(prefix):
                fields = line.split(b':')
                if len(fields) < 2:
                    return 'invalid'
                matches.append(bytes(fields[1]))
        if len(matches) != 1:
            raise ValueError('dedicated QA account has no unique shadow entry')
        field = matches[0]
        if field.startswith((b'!', b'*')):
            return 'locked'
        if field.startswith(b'$6$'):
            parts = field.split(b'$')
            if len(parts) >= 4 and parts[2] and parts[3] and not field.startswith((b'!', b'*')):
                return 'active'
        return 'invalid'
    finally:
        raw[:] = b'\0' * len(raw)


def prepare_account(account: dict, *, vm_id: str | None = None, descriptor=0,
                    shadow_path: Path = Path('/etc/shadow'),
                    chpasswd_path: Path = Path('/usr/sbin/chpasswd'),
                    marker_path: Path = Path('/etc/paintedwolf-input-qualification.json')) -> dict:
    validate_account(account, vm_id=vm_id, marker_path=marker_path)
    _verify_root_binary(chpasswd_path)
    secret_buf = unlock_wire.read_secret_frame(descriptor)
    chpasswd_input = bytearray()
    try:
        chpasswd_input.extend(account['name'].encode('ascii'))
        chpasswd_input.append(ord(':'))
        chpasswd_input.extend(secret_buf)
        chpasswd_input.append(ord('\n'))
        returncode, _, _ = native_command.command(
            [str(chpasswd_path), '-c', 'SHA512'],
            timeout=10, limit=4096, input_bytes=chpasswd_input)
    finally:
        secret_buf[:] = b'\0' * len(secret_buf)
        chpasswd_input[:] = b'\0' * len(chpasswd_input)
    if returncode != 0:
        raise CaseUnavailable('native-unlock-account-prepare-failed')
    if shadow_state(account['name'], shadow_path=shadow_path) != 'active':
        raise CaseUnavailable('native-unlock-account-shadow-not-active')
    return {'prepared': True, 'account': dict(account), 'credential_state': 'active'}


def disable_account(account: dict, *, vm_id: str | None = None, terminate_sessions: bool = False,
                    shadow_path: Path = Path('/etc/shadow'),
                    passwd_path: Path = Path('/usr/bin/passwd'),
                    usermod_path: Path = Path('/usr/sbin/usermod'),
                    loginctl_path: Path = Path('/usr/bin/loginctl'),
                    marker_path: Path = Path('/etc/paintedwolf-input-qualification.json')) -> dict:
    validate_account_shape(account)
    if os.geteuid() != 0:
        raise ValueError('dedicated QA account custody requires root execution')
    if vm_id is not None:
        _verify_marker(vm_id, marker_path=marker_path)
    _verify_root_binary(passwd_path)
    _verify_root_binary(usermod_path)
    passwd_rc, _, _ = native_command.command([str(passwd_path), '-l', account['name']], timeout=10, limit=4096)
    usermod_rc, _, _ = native_command.command([str(usermod_path), '-L', account['name']], timeout=10, limit=4096)
    if passwd_rc != 0 or usermod_rc != 0:
        raise CaseUnavailable('native-unlock-account-disable-failed')
    if terminate_sessions:
        _verify_root_binary(loginctl_path)
        native_command.command([str(loginctl_path), 'terminate-user', account['name']], timeout=10, limit=4096)
    if shadow_state(account['name'], shadow_path=shadow_path) != 'locked':
        raise CaseUnavailable('native-unlock-account-shadow-not-locked')
    return {'disabled': True, 'account': dict(account), 'shadow_locked': True,
            'credential_state': 'locked', 'sessions_terminated': bool(terminate_sessions)}


def _verified_checkout_root() -> Path:
    module_path = Path(__file__).resolve()
    checkout = module_path.parents[3]
    info = checkout.lstat()
    if (not stat.S_ISDIR(info.st_mode) or info.st_uid not in {0, os.getuid()}
            or info.st_mode & 0o022 or checkout.resolve() != checkout
            or (checkout / 'scripts/linux_release/installed_input/unlock_account.py').resolve() != module_path):
        raise ValueError('dedicated QA account helper checkout root failed physical verification')
    return checkout


def helper_python_arguments(action: str, account: dict, *, vm_id: str | None = None,
                            terminate_sessions: bool = False) -> list[str]:
    if action not in {'prepare', 'disable'}:
        raise ValueError('dedicated QA account helper requires a closed action')
    validate_account_shape(account)
    checkout = _verified_checkout_root()
    python_exe = Path(sys.executable).resolve()
    arguments = [
        str(python_exe), '-I', '-c',
        'import sys;sys.path.insert(0,sys.argv.pop(1));'
        'from scripts.linux_release.installed_input.unlock_account import main;main()',
        str(checkout), action, '--account', protocol.canonical(account).decode('ascii').strip()]
    if vm_id is not None:
        arguments.extend(['--vm-id', vm_id])
    if terminate_sessions:
        arguments.append('--terminate-sessions')
    return arguments


def run_privileged_prepare(account: dict, secret_raw: bytes | bytearray, *, vm_id: str | None = None) -> dict:
    validate_account_shape(account)
    unlock_wire.secret(secret_raw)
    frame = bytearray(unlock_wire.secret_frame(secret_raw))
    try:
        arguments = ['/usr/bin/sudo', '-n', *helper_python_arguments('prepare', account, vm_id=vm_id)]
        returncode, stdout, _ = native_command.command(arguments, timeout=15, limit=4096, input_bytes=frame)
    finally:
        frame[:] = b'\0' * len(frame)
    if returncode != 0:
        raise CaseUnavailable('native-unlock-account-prepare-failed')
    result = decode(stdout)
    if result != {'prepared': True, 'account': dict(account), 'credential_state': 'active'}:
        raise ValueError('dedicated QA account preparation returned an invalid acknowledgment')
    return result


def run_privileged_disable(account: dict, *, vm_id: str | None = None,
                           terminate_sessions: bool = False) -> dict:
    arguments = ['/usr/bin/sudo', '-n', *helper_python_arguments(
        'disable', account, vm_id=vm_id, terminate_sessions=terminate_sessions)]
    returncode, stdout, _ = native_command.command(arguments, timeout=15, limit=4096)
    if returncode != 0:
        raise CaseUnavailable('native-unlock-account-disable-failed')
    result = decode(stdout)
    if result != {'disabled': True, 'account': dict(account), 'shadow_locked': True,
                  'credential_state': 'locked', 'sessions_terminated': bool(terminate_sessions)}:
        raise ValueError('dedicated QA account disable returned an invalid acknowledgment')
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('action', choices=('prepare', 'disable'))
    parser.add_argument('--account', required=True)
    parser.add_argument('--vm-id', default=None)
    parser.add_argument('--terminate-sessions', action='store_true')
    args = parser.parse_args()
    account = decode(args.account.encode('ascii'))
    if args.action == 'prepare':
        result = prepare_account(account, vm_id=args.vm_id, descriptor=0)
    else:
        result = disable_account(account, vm_id=args.vm_id, terminate_sessions=args.terminate_sessions)
    sys.stdout.buffer.write(protocol.canonical(result))
    sys.stdout.buffer.flush()


if __name__ == '__main__':
    main()
