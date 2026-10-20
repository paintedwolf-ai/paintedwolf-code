"""Run the fixed extracted-package diagnostic with an owned transient userns profile."""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import selectors
import signal
import time
import stat
import subprocess
import tempfile

POLICY = Path('/proc/sys/kernel/apparmor_restrict_unprivileged_userns')
MARKER = Path('/etc/paintedwolf-qualification.json')
INSTALL = 'opt/painted-wolf-code'


def physical(path, *, owner, executable=False, limit=None):
    if not path.is_absolute() or path.resolve(strict=True) != path:
        raise ValueError('package probe inputs must be absolute physical paths')
    before = path.lstat()
    if (not stat.S_ISREG(before.st_mode) or before.st_uid not in (owner if isinstance(owner, tuple) else (owner,)) or before.st_mode & 0o6022
            or executable and not before.st_mode & 0o111 or limit is not None and before.st_size > limit):
        raise ValueError('package probe input ownership, mode or size is invalid')
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b''):
            digest.update(chunk)
    after = path.lstat()
    identity = lambda value: (value.st_dev, value.st_ino, value.st_mode, value.st_size, value.st_mtime_ns, value.st_ctime_ns)
    if identity(before) != identity(after):
        raise ValueError('package probe input changed during capture')
    return {'path': str(path), 'sha256': digest.hexdigest(), 'bytes': after.st_size,
            'uid': after.st_uid, 'mode': oct(after.st_mode & 0o7777)}


def profile_source(executable):
    # Reject every AppArmor pattern/escape token, rather than accepting a path glob.
    if not isinstance(executable, str) or not re.fullmatch(r'/[A-Za-z0-9._ /+-]+/opt/painted-wolf-code/bin/pw', executable):
        raise ValueError('package probe profile requires a literal extracted engine path')
    name = 'paintedwolf-package-probe-' + hashlib.sha256(executable.encode()).hexdigest()
    source = ('abi <abi/4.0>,\ninclude <tunables/global>\n'
              f'profile {name} "{executable}" flags=(unconfined) {{\n  userns,\n}}\n')
    return name, source.encode()


def selected(tree):
    uid = os.geteuid()
    if uid == 0 or platform.system() != 'Linux' or platform.machine() not in {'x86_64', 'aarch64'}:
        raise ValueError('package diagnostics require a nonroot native Linux caller')
    if not tree.is_absolute() or tree.resolve(strict=True) != tree or tree == Path('/'):
        raise ValueError('select an absolute physical extracted package root')
    ancestor = tree.stat()
    if not stat.S_ISDIR(ancestor.st_mode) or ancestor.st_uid not in {0, uid} or ancestor.st_mode & 0o022:
        raise ValueError('extracted package root must be private to its probe owner')
    manifest_path = tree / INSTALL / 'manifest.json'
    manifest_identity = physical(manifest_path, owner=(0, uid), limit=2 << 20)
    manifest = json.loads(manifest_path.read_bytes())
    if manifest.get('format_version') != 1 or manifest.get('install_root') != '/' + INSTALL:
        raise ValueError('extracted package manifest has an unsupported identity')
    result = {'tree': str(tree), 'manifest': manifest_identity}
    for name in ('pw', 'pw-document-core'):
        path = tree / INSTALL / 'bin' / name
        measured = physical(path, owner=(0, uid), executable=True)
        matches = [row for row in manifest['executables'] if row.get('path') == '/' + INSTALL + '/bin/' + name]
        if len(matches) != 1 or matches[0] != {'path': '/' + INSTALL + '/bin/' + name,
                                             'sha256': measured['sha256'], 'size': measured['bytes']}:
            raise ValueError('probe executable differs from its selected package manifest')
        with path.open('rb') as stream:
            header = stream.read(20)
        machine = 62 if platform.machine() == 'x86_64' else 183
        if len(header) != 20 or header[:6] != b'\x7fELF\x02\x01' or int.from_bytes(header[18:20], 'little') != machine:
            raise ValueError('package diagnostic executable is not native ELF64')
        result[name] = measured
    profile_source(result['pw']['path'])
    return result


def authority(binding, lease):
    hosted = os.environ.get('GITHUB_ACTIONS') == 'true' and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted'
    if hosted:
        return {'kind': 'disposable-github-hosted-runner'}
    if lease is None or re.fullmatch(r'/run/paintedwolf-package-stage-[a-f0-9]{32}\.json', str(lease)) is None:
        raise ValueError('owned guest package probing requires its exact root-owned operation lease')
    marker_identity = physical(MARKER, owner=0, limit=4096)
    marker = json.loads(MARKER.read_bytes())
    if (set(marker) != {'format_version', 'desktop', 'package'} or type(marker['format_version']) is not int
            or marker['format_version'] != 1 or marker['desktop'] not in {'gnome', 'plasma', 'xfce', 'cinnamon', 'mate', 'lxqt'}
            or marker['package'] != '/' + INSTALL):
        raise ValueError('existing disposable guest marker does not authorize this native package')
    lease_identity = physical(lease, owner=0, limit=4096)
    expected = {'format_version': 1, 'uid': os.geteuid(), 'tree': binding['tree'],
                'engine_path': binding['pw']['path'], 'engine_sha256': binding['pw']['sha256'],
                'marker_sha256': marker_identity['sha256']}
    actual = json.loads(lease.read_bytes())
    if set(actual) != set(expected) or any(type(actual[key]) is not type(value) or actual[key] != value for key, value in expected.items()):
        raise ValueError('root operation lease differs from the selected package and existing guest marker')
    return {'kind': 'owned-disposable-guest', 'marker': marker_identity, 'lease': lease_identity}


def profiles():
    result = subprocess.run(['sudo', '-n', 'cat', '/sys/kernel/security/apparmor/profiles'], capture_output=True, timeout=30)
    if result.returncode or len(result.stdout) > 8 << 20:
        raise ValueError('bounded kernel profile observation failed')
    return result.stdout.decode().splitlines()


def diagnostic(arguments, env):
    """Bound output and stop only the process group created for this diagnostic."""
    streams = [bytearray(), bytearray()]
    process = subprocess.Popen(arguments, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    deadline = time.monotonic() + 60
    try:
        with selectors.DefaultSelector() as selected_streams:
            for index, stream in enumerate((process.stdout, process.stderr)):
                selected_streams.register(stream, selectors.EVENT_READ, index)
            while selected_streams.get_map():
                if time.monotonic() >= deadline:
                    raise ValueError('fixed package diagnostic exceeded its deadline')
                for key, _ in selected_streams.select(timeout=0.5):
                    chunk = os.read(key.fileobj.fileno(), 65536)
                    if not chunk:
                        selected_streams.unregister(key.fileobj)
                        continue
                    if sum(map(len, streams)) + len(chunk) > 1 << 20:
                        raise ValueError('fixed package diagnostic output exceeded its bound')
                    streams[key.data].extend(chunk)
        code = process.wait(timeout=max(0.1, deadline - time.monotonic()))
        return subprocess.CompletedProcess(arguments, code, bytes(streams[0]), bytes(streams[1]))
    finally:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=10)
        process.stdout.close(); process.stderr.close()


def fixed_diagnostic(binding):
    with tempfile.TemporaryDirectory(prefix='paintedwolf-package-diagnostic-') as temporary:
        state = Path(temporary)
        env = {**os.environ, 'HOME': str(state), 'XDG_CONFIG_HOME': str(state / 'config'),
               'XDG_DATA_HOME': str(state / 'data'), 'XDG_CACHE_HOME': str(state / 'cache'),
               'LYCAON_DOCUMENT_CORE_BINARY': binding['pw-document-core']['path'],
               'LYCAON_ENGINE_ROOT': str(Path(binding['tree']) / INSTALL / 'engine-root')}
        result = diagnostic([binding['pw']['path'], 'diagnostics', 'document-core'], env)
        return result


def run(tree, *, lease=None, output=None, require_baseline_refusal=False):
    binding = selected(tree)
    proof = authority(binding, lease)
    name, source = profile_source(binding['pw']['path'])
    before = POLICY.read_text().strip() if POLICY.exists() else None
    report = {'format_version': 1, 'binding': binding, 'authority': proof, 'global_policy_before': before,
              'global_policy_changed': False, 'profile': name, 'profile_loaded': False, 'profile_removed': False,
              'profile_sha256': hashlib.sha256(source).hexdigest(), 'status': 'started'}
    if output is not None:
        if (not output.is_absolute() or output.exists() or output.is_symlink() or not output.parent.is_dir()
                or output.resolve().is_relative_to(tree) or output.resolve() in {MARKER, lease}):
            raise ValueError('package probe journal requires a fresh independent absolute file')
        with output.open('x') as stream:
            json.dump(report, stream, sort_keys=True); stream.write('\n')
        output.chmod(0o600)
    try:
        if require_baseline_refusal:
            if before != '1':
                raise ValueError('controlled baseline comparison requires the active host restriction')
            baseline = fixed_diagnostic(binding)
            report['baseline'] = {'exit_code': baseline.returncode, 'stdout': baseline.stdout.decode(errors='replace'),
                                  'stderr': baseline.stderr.decode(errors='replace')}
            if baseline.returncode == 0:
                raise ValueError('unprofiled baseline already succeeds; no differential qualification claimed')
        if before == '1':
            # --add refuses another owner's existing profile; never replace it.
            subprocess.run(['sudo', '-n', 'apparmor_parser', '--add'], input=source, check=True, capture_output=True, timeout=30)
            report['profile_loaded'] = True
            if not any(line in {name + ' (unconfined)', name + ' (enforce)'} for line in profiles()):
                raise ValueError('exact package probe profile was not observed in the kernel')
        if selected(tree) != binding or authority(binding, lease) != proof:
            raise ValueError('selected package or operation lease changed before execution')
        result = fixed_diagnostic(binding)
        report.update({'diagnostic_exit_code': result.returncode, 'stdout': result.stdout.decode(errors='replace'),
                       'stderr': result.stderr.decode(errors='replace'), 'status': 'passed' if result.returncode == 0 else 'failed'})
        if selected(tree) != binding or authority(binding, lease) != proof:
            raise ValueError('selected package or operation lease changed during execution')
    except Exception as error:
        report.update({'status': 'failed', 'error': {'type': type(error).__name__, 'message': str(error)}})
    finally:
        if report['profile_loaded']:
            try:
                subprocess.run(['sudo', '-n', 'apparmor_parser', '--remove'], input=source, check=True, capture_output=True, timeout=30)
                report['profile_removed'] = not any(line.startswith(name + ' (') for line in profiles())
                if not report['profile_removed']:
                    raise ValueError('owned transient package profile remained loaded')
            except Exception as error:
                report.update({'status': 'failed', 'cleanup_error': str(error)})
        after = POLICY.read_text().strip() if POLICY.exists() else None
        report['global_policy_changed'] = after != before
        if after != before:
            report.update({'status': 'failed', 'policy_error': 'global userns restriction changed'})
    if output is not None:
        output.write_text(json.dumps(report, sort_keys=True) + '\n')
    return report


def main(arguments=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--tree', type=Path, required=True)
    parser.add_argument('--lease', type=Path)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--require-baseline-refusal', action='store_true')
    args = parser.parse_args(arguments)
    try:
        report = run(args.tree, lease=args.lease, output=args.output, require_baseline_refusal=args.require_baseline_refusal)
        print(json.dumps(report, sort_keys=True))
        return 0 if report['status'] == 'passed' else 1
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        parser.exit(1, 'Extracted package diagnostic refused: ' + str(error) + '\n')


if __name__ == '__main__':
    raise SystemExit(main())
