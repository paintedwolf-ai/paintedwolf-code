"""Prepare a disposable GitHub-hosted Ubuntu desktop for native installed rehearsals."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import platform
import pwd
import secrets
import stat
import subprocess
import time

from .installed_environment import release

USER = 'paintedwolf-test'
PACKAGES = ['gdm3', 'gnome-session', 'gnome-shell', 'gnome-settings-daemon', 'gnome-keyring',
            'at-spi2-core', 'python3-pyatspi', 'dbus-x11', 'xserver-xorg-core',
            'xserver-xorg-video-dummy', 'xserver-xorg-input-libinput', 'x11-xserver-utils',
            'libgl1-mesa-dri', 'mesa-utils', 'fonts-dejavu-core', 'libglib2.0-bin', 'xauth']
XORG = '''Section "Device"
    Identifier "QualificationDummy"
    Driver "dummy"
    VideoRam 256000
EndSection
Section "Monitor"
    Identifier "QualificationMonitor"
    HorizSync 28.0-80.0
    VertRefresh 48.0-75.0
    Modeline "1920x1080" 148.5 1920 2008 2052 2200 1080 1084 1089 1125 +HSync +VSync
EndSection
Section "Screen"
    Identifier "QualificationScreen"
    Device "QualificationDummy"
    Monitor "QualificationMonitor"
    DefaultDepth 24
    SubSection "Display"
        Depth 24
        Modes "1920x1080"
    EndSubSection
EndSection
'''


def validate_host(environment: dict, uid: int, machine: str, distribution: dict):
    if (uid != 0 or environment.get('GITHUB_ACTIONS') != 'true'
            or environment.get('RUNNER_ENVIRONMENT') != 'github-hosted'
            or not environment.get('GITHUB_RUN_ID', '').isdigit()
            or environment.get('RUNNER_ARCH') not in {'ARM64', 'X64'}):
        raise ValueError('desktop preparation requires an explicitly selected disposable hosted runner')
    expected = {'ARM64': 'aarch64', 'X64': 'x86_64'}[environment['RUNNER_ARCH']]
    if machine != expected or distribution.get('ID') != 'ubuntu' or distribution.get('VERSION_ID') != '24.04':
        raise ValueError('desktop preparation requires the selected native Ubuntu 24.04 runner')
    source = environment.get('GITHUB_SHA', '')
    if len(source) != 40 or any(value not in '0123456789abcdef' for value in source):
        raise ValueError('hosted desktop preparation requires its exact controller commit')


def run(arguments: list[str], *, input: bytes | None = None, timeout=1200, cwd=None) -> bytes:
    result = subprocess.run(arguments, input=input, capture_output=True, timeout=timeout, cwd=cwd,
                            env={**os.environ, 'DEBIAN_FRONTEND': 'noninteractive', 'LC_ALL': 'C.UTF-8'})
    if result.returncode or len(result.stdout) + len(result.stderr) > 16 << 20:
        raise OSError('hosted desktop preparation command failed or exceeded its output bound: ' + arguments[0])
    return result.stdout


def fresh(path: Path, text: str, mode=0o644):
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    with os.fdopen(descriptor, 'w') as stream:
        stream.write(text)
    path.chmod(mode)


def prepare(checkout: Path, output: Path) -> dict:
    validate_host(os.environ, os.getuid(), platform.machine(), release())
    checkout = checkout.resolve(strict=True)
    if not (checkout / 'scripts/linux_release/installed_environment.py').is_file() or output.exists() or output.is_symlink():
        raise ValueError('desktop preparation requires the checked-out controller and a fresh result path')
    revision = run(['git', '-c', 'safe.directory=' + str(checkout), '-C', str(checkout), 'rev-parse', 'HEAD'], timeout=10).decode().strip()
    if revision != os.environ['GITHUB_SHA']:
        raise ValueError('desktop checkout differs from its selected controller commit')
    marker = Path('/etc/paintedwolf-qualification.json')
    if marker.exists() or marker.is_symlink():
        raise ValueError('hosted runner already belongs to a qualification campaign')
    try:
        pwd.getpwnam(USER)
    except KeyError:
        pass
    else:
        raise ValueError('hosted desktop account already exists; preserving its owner')
    run(['apt-get', 'update'])
    run(['apt-get', 'install', '-y', '--no-install-recommends', *PACKAGES])
    run(['useradd', '--create-home', '--shell', '/bin/bash', USER])
    # Autologin uses a disposable account; no reusable OS or app secret is retained.
    run(['chpasswd'], input=(USER + ':' + secrets.token_urlsafe(48) + '\n').encode())
    account = pwd.getpwnam(USER)
    home = Path(account.pw_dir)
    controller = home / 'controller'
    run(['git', '-c', 'safe.directory=' + str(checkout), 'clone', '--no-local',
         str(checkout), str(controller)], timeout=120)
    run(['chown', '-R', str(account.pw_uid) + ':' + str(account.pw_gid), str(controller)])
    (home / '.ssh').mkdir(mode=0o700)
    os.chown(home / '.ssh', account.pw_uid, account.pw_gid)
    fresh(Path('/etc/sudoers.d/paintedwolf-qualification'), USER + ' ALL=(ALL) NOPASSWD: ALL\n', 0o440)
    fresh(Path('/etc/X11/xorg.conf.d/20-paintedwolf-qualification.conf'), XORG)
    sessions = Path('/usr/share/xsessions')
    selected = next((name for name in ('gnome-xorg', 'gnome') if (sessions / (name + '.desktop')).is_file()), None)
    if selected is None:
        raise ValueError('installed GNOME package has no declared X11 session entry')
    entry = sessions / (selected + '.desktop')
    if entry.is_symlink() or entry.stat().st_uid != 0 or entry.stat().st_mode & 0o022:
        raise ValueError('GNOME session entry is not owned by the package administrator')
    gdm = Path('/etc/gdm3/custom.conf')
    if gdm.is_symlink() or not gdm.is_file() or gdm.stat().st_uid != 0 or gdm.stat().st_mode & 0o022:
        raise ValueError('hosted GDM configuration is not an administrator-owned regular file')
    # This file is confined to the newly allocated ephemeral hosted machine.
    gdm.write_text('[daemon]\nWaylandEnable=false\nAutomaticLoginEnable=true\nAutomaticLogin=' + USER + '\nDefaultSession=' + selected + '.desktop\n')
    fresh(Path('/var/lib/AccountsService/users') / USER,
          '[User]\nSession=' + selected + '\nXSession=' + selected + '\nSystemAccount=false\n')
    fresh(home / '.dmrc', '[Desktop]\nSession=' + selected + '\n')
    os.chown(home / '.dmrc', account.pw_uid, account.pw_gid)
    environment_file = Path('/etc/environment')
    if environment_file.is_symlink() or not environment_file.is_file() or environment_file.stat().st_uid != 0:
        raise ValueError('hosted environment file is not administrator-owned')
    with environment_file.open('a') as stream:
        stream.write('\nLIBGL_ALWAYS_SOFTWARE=1\n')
    run(['systemctl', 'restart', 'gdm3'])
    probe = ('import json; from linux_release.installed_environment import graphical_session; '
             'print(json.dumps(graphical_session()))')
    deadline, session = time.monotonic() + 180, None
    while time.monotonic() < deadline:
        try:
            raw = run(['runuser', '-u', USER, '--', 'env',
                       'HOME=' + str(home), 'PYTHONPATH=' + str(controller / 'scripts'), '/usr/bin/python3', '-c', probe], timeout=15, cwd=home)
            session = json.loads(raw)
            if session.get('Type') == 'x11' and session.get('Active') == 'yes':
                break
        except (OSError, subprocess.TimeoutExpired, ValueError):
            pass
        time.sleep(1)
    else:
        raise OSError('hosted GNOME did not establish a measured active X11 session')
    authority = Path('/run/user') / str(account.pw_uid) / 'gdm/Xauthority'
    info = authority.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != account.pw_uid or stat.S_IMODE(info.st_mode) & 0o077:
        raise ValueError('hosted GDM did not supply a private display authority for its account')
    (home / '.Xauthority').symlink_to(authority)
    os.lchown(home / '.Xauthority', account.pw_uid, account.pw_gid)
    fresh(marker, json.dumps({'format_version': 1, 'desktop': 'gnome', 'package': 'deb'}) + '\n')
    result = {'format_version': 1, 'controller_commit': os.environ['GITHUB_SHA'],
              'github_run_id': os.environ['GITHUB_RUN_ID'], 'architecture': platform.machine(),
              'controller_checkout': str(controller),
              'session': session, 'user': USER, 'uid': account.pw_uid, 'desktop': 'gnome',
              'display_protocol': 'x11', 'graphics': 'software',
              'application_qualified': False, 'physical_gpu_qualified': False}
    fresh(output, json.dumps(result, indent=2, sort_keys=True) + '\n')
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--checkout', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    arguments = parser.parse_args()
    prepare(arguments.checkout, arguments.output)


if __name__ == '__main__':
    main()
