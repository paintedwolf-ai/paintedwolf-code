"""Seal the root supervisor's actual package-bootstrap invocation and outcome."""
from __future__ import annotations

import json
import os
from pathlib import Path
import stat
import signal
import selectors
import time
import subprocess

from .hosted_desktop import fresh
from .hosted_installed_plan import controller
from .installed_execution import read
from .installed_input.protocol import binding
from .installed_runtime import file_identity, stable


def physical(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_gid != 0 or info.st_mode & 0o222:
        raise ValueError('bootstrap supervisor requires administrator-owned sealed files')
    for parent in path.parents:
        owner = parent.lstat()
        if not stat.S_ISDIR(owner.st_mode) or owner.st_uid != 0 or owner.st_gid != 0 or owner.st_mode & 0o022:
            raise ValueError('bootstrap supervisor custody crosses an unsafe ancestor')
    return stable(info), file_identity(path)


def invocation(root, account):
    return ['/usr/bin/sudo', '-H', '-u', account.pw_name, '/usr/bin/python3', '-m',
        'scripts.linux_release.installed_cases.native_bootstrap', '--final-artifacts', str(root / 'final-artifacts.json'),
        '--execution-request', str(root / 'execution-request.json'), '--file-root', str(root / 'packages'),
        '--output', str(Path(account.pw_dir) / 'native-bootstrap')]


def captured_child(arguments, checkout, root):
    logs = [root / ('bootstrap-supervision.' + name) for name in ('stdout', 'stderr')]
    handles = [os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), 'wb') for path in logs]
    child = None
    try:
        child = subprocess.Popen(arguments, cwd=checkout, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            env={'PATH': '/usr/bin:/bin', 'LC_ALL': 'C.UTF-8'}, start_new_session=True)
        deadline = time.monotonic() + 1800
        sizes = [0, 0]
        with selectors.DefaultSelector() as selected:
            for index, stream in enumerate((child.stdout, child.stderr)):
                os.set_blocking(stream.fileno(), False)
                selected.register(stream, selectors.EVENT_READ, index)
            while selected.get_map():
                if time.monotonic() >= deadline:
                    raise ValueError('bootstrap supervisor exceeded its fixed deadline')
                for key, _ in selected.select(timeout=min(1, max(0, deadline - time.monotonic()))):
                    block = os.read(key.fileobj.fileno(), 65536)
                    if not block:
                        selected.unregister(key.fileobj)
                        continue
                    index = key.data
                    remaining = 65536 - sizes[index]
                    handles[index].write(block[:remaining])
                    sizes[index] += min(len(block), remaining)
                    if len(block) > remaining:
                        raise ValueError('bootstrap supervisor output exceeded fixed bound')
        return child.wait(timeout=max(0.1, deadline - time.monotonic()))
    finally:
        if child is not None and child.poll() is None:
            # Interrupt the captured Python owner so its native transaction finally can cancel its own group.
            os.killpg(child.pid, signal.SIGINT)
            try:
                child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(child.pid, signal.SIGKILL)
                child.wait()
        if child is not None:
            child.stdout.close()
            child.stderr.close()
        for stream in handles:
            stream.close()


def supervise(root, selected, account):
    if os.getuid() != 0 or os.geteuid() != 0:
        raise ValueError('bootstrap supervisor requires administrator identity')
    checkout = Path(account.pw_dir) / 'controller'
    controller(checkout, selected)
    request_path = root / 'execution-request.json'
    identity = physical(request_path)
    execution = binding(read(request_path))
    if execution['driver_tree'] != selected['controller_tree'] or execution['source_commit'] != selected['source_commit']:
        raise ValueError('bootstrap supervisor request differs from selected controller and package')
    journal = root / 'bootstrap-supervision.json'
    if journal.exists() or journal.is_symlink():
        raise ValueError('bootstrap supervisor refuses a repeated invocation')
    arguments = invocation(root, account)
    fresh(root / 'bootstrap-invocation.json', json.dumps({'execution': execution, 'arguments': arguments}) + '\n', 0o400)
    result = captured_child(arguments, checkout, root)
    controller(checkout, selected)
    if physical(request_path) != identity or read(request_path) != execution:
        raise ValueError('bootstrap request changed during the captured invocation')
    value = {'format_version': 1, 'execution': execution, 'controller_commit': selected['controller_commit'],
        'controller_tree': selected['controller_tree'], 'arguments': arguments, 'exit_code': result,
        'observer_uid': os.getuid(), 'observer_euid': os.geteuid()}
    fresh(journal, json.dumps(value, sort_keys=True) + '\n', 0o444)
    if result != 0:
        raise ValueError('actual package bootstrap did not complete successfully')
    return value


def verify(root, selected, account, execution):
    path = root / 'bootstrap-supervision.json'
    physical(path)
    expected = {'format_version': 1, 'execution': execution, 'controller_commit': selected['controller_commit'],
        'controller_tree': selected['controller_tree'], 'arguments': invocation(root, account), 'exit_code': 0,
        'observer_uid': 0, 'observer_euid': 0}
    actual = read(path)
    if (actual != expected or any(type(actual.get(key)) is not int for key in
            ('format_version', 'exit_code', 'observer_uid', 'observer_euid'))):
        raise ValueError('bootstrap supervision differs from its actual selected successful invocation')
    return expected
