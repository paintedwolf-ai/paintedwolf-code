"""Settle one submitted host verification job by its private completion custody.

No wrapper imports, guest operations or source/argument heuristics select a live
scheduler entry. Canonical job metadata and its inherited lock bind each effect.
"""
import fcntl
import json
import importlib
from importlib.machinery import PathFinder
import math
import os
from pathlib import Path
import re
import stat
import sys


def _require(value, message):
    if not value:
        raise ValueError(message)


class JobSettlement:

    def __init__(self, *, checkout, arguments, save, command):
        self.checkout = Path(checkout)
        self.arguments = list(arguments)
        self.save = save
        self.command = command

    def directory(self, job):
        self.validate_checkout()
        from scripts.artifact_paths import artifact_root
        _require(isinstance(job, dict) and set(job) == {'job_id', 'pid', 'state', 'wait', 'log'}, 'submitted job shape differs')
        _require(
            isinstance(job['job_id'], str) and re.fullmatch('[a-f0-9]{32}', job['job_id'])
            and type(job['pid']) is int and job['pid'] > 1
            and job['state'] == 'submitted' and job['wait'] == './task --wait ' + job['job_id'],
            'submitted job identity differs')
        directory = artifact_root(self.checkout) / 'jobs' / job['job_id']
        _require(job['log'] == str(directory / 'output.log'), 'submitted job log differs from canonical job directory')
        info = directory.lstat()
        _require(
            stat.S_ISDIR(info.st_mode) and info.st_uid == os.getuid()
            and not info.st_mode & 0o077 and directory.resolve() == directory,
            'job directory is not physical and private')
        metadata = self.read(directory / 'job.json')
        _require(
            isinstance(metadata, dict)
            and set(metadata) == {'job_id', 'arguments', 'submitted_at', 'on_complete'}
            and metadata['job_id'] == job['job_id'] and metadata['arguments'] == self.arguments
            and metadata['on_complete'] is None,
            'submitted job metadata differs from exact invocation')
        return directory

    def read(self, path):
        info = path.lstat()
        _require(
            stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid()
            and info.st_nlink == 1 and info.st_size <= 65536 and path.resolve() == path,
            'job record authority differs')
        with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), 'rb') as stream:
            opened = os.fstat(stream.fileno())
            raw = stream.read(65537)
            after = os.fstat(stream.fileno())
        stable = lambda row: (row.st_dev, row.st_ino, row.st_mode, row.st_uid, row.st_gid, row.st_size, row.st_mtime_ns, row.st_ctime_ns)
        _require(stable(info) == stable(opened) == stable(after) == stable(path.lstat()), 'job record changed while reading')
        return json.loads(raw)

    def completed(self, job):
        directory = self.directory(job)
        lock = directory / 'completion.lock'
        fd = os.open(lock, os.O_RDWR | os.O_NOFOLLOW)
        try:
            info = os.fstat(fd)
            _require(
                stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid()
                and info.st_nlink == 1 and lock.resolve() == lock,
                'job completion lock authority differs')
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                return None
            if not (directory / 'result.json').exists():
                return None
            result = self.read(directory / 'result.json')
            _require(
                isinstance(result, dict) and result.get('job_id') == job['job_id']
                and result.get('state') in {'completed', 'unverified'}
                and type(result.get('exit_code')) is int and 0 <= result['exit_code'] <= 255
                and type(result.get('finished_at')) in {int, float}
                and math.isfinite(result['finished_at']) and result['finished_at'] > 0
                and result.get('log') == job['log']
                and (result['state'] != 'unverified' or result['exit_code'] != 0),
                'own terminal job result differs')
            return result
        finally:
            os.close(fd)

    def validate_checkout(self):
        _require(self.checkout.is_absolute() and self.checkout.resolve(strict=True) == self.checkout,
                 'process observation requires the physical reviewed checkout')
        expected = {
            'scripts.artifact_paths': 'scripts/artifact_paths.py',
            'scripts.linux_release': 'scripts/linux_release/__init__.py',
            'scripts.linux_release.installed_cases': 'scripts/linux_release/installed_cases/__init__.py',
            'scripts.linux_release.installed_cases.records': 'scripts/linux_release/installed_cases/records.py',
            'scripts.linux_release.installed_process': 'scripts/linux_release/installed_process.py',
            'verification_health': 'scripts/verification_health.py',
        }
        namespace = self.checkout / 'scripts'
        namespace_search = [str(namespace), str(self.checkout),
                  *[entry for entry in sys.path if entry not in {str(namespace), str(self.checkout)}]]

        def require_namespace(spec, locations):
            paths = [Path(location) for location in locations] if locations is not None else []
            _require(spec is not None and spec.origin is None and paths
                     and all(path == namespace and path.resolve(strict=True) == namespace for path in paths),
                     'scripts namespace differs from the reviewed checkout')

        # Namespace packages have no __file__; identical search roots add no authority.
        candidate = PathFinder.find_spec('scripts', namespace_search)
        require_namespace(candidate, candidate.submodule_search_locations if candidate else None)
        # Reject a cached dependency from another checkout before importing any observer.
        for name, loaded in list(sys.modules.items()):
            if name == 'scripts' or name.startswith('scripts.') or name.startswith('verification_'):
                source = getattr(loaded, '__file__', None)
                if name == 'scripts':
                    _require(source is None, 'scripts must remain the reviewed namespace package')
                    require_namespace(getattr(loaded, '__spec__', None), getattr(loaded, '__path__', None))
                    continue
                _require(source is not None and Path(source).resolve().is_relative_to(self.checkout),
                         'cached observation dependency is outside the reviewed checkout')
                if name in expected:
                    _require(Path(source).resolve() == (self.checkout / expected[name]).resolve(strict=True),
                             'cached observation dependency has the wrong concrete origin')
        for name, relative in expected.items():
            source = self.checkout / relative
            search = source.parent.parent if source.name == '__init__.py' else source.parent
            spec = PathFinder.find_spec(name, [str(search)])
            _require(spec is not None and spec.origin is not None
                     and Path(spec.origin).resolve() == source.resolve(strict=True),
                     'observation dependency differs from the reviewed checkout')
        sys.path[:] = namespace_search

    def _observers(self):
        self.validate_checkout()
        process = importlib.import_module('scripts.linux_release.installed_process')
        health = importlib.import_module('verification_health')
        return process.process_identity, health.identity

    def invocation(self, job, status):
        process_identity, identity = self._observers()
        directory = self.directory(job)
        lock_path = directory / 'completion.lock'
        lock_fd = os.open(lock_path, os.O_RDONLY | os.O_NOFOLLOW)
        try:
            lock = os.fstat(lock_fd)
        finally:
            os.close(lock_fd)
        _require(
            stat.S_ISREG(lock.st_mode) and lock.st_uid == os.getuid()
            and lock.st_nlink == 1 and lock_path.resolve() == lock_path,
            'job completion lock identity differs')
        matches = []
        for row in status.get('runs', []):
            if row.get('kind') not in {'invocation', 'request'}:
                continue
            pid = row.get('pid')
            if type(pid) is not int or pid <= 1:
                continue
            try:
                before = process_identity(pid)
                if Path('/proc/' + str(pid)).stat().st_uid != os.getuid():
                    continue
                descriptors = list(Path('/proc/' + str(pid) + '/fd').iterdir())
                _require(len(descriptors) <= 1024, 'scheduler process descriptor inventory exceeds bound')
                inherited = False
                for descriptor in descriptors:
                    try:
                        info = descriptor.stat()
                    except FileNotFoundError:
                        continue
                    if (info.st_dev, info.st_ino) == (lock.st_dev, lock.st_ino):
                        inherited = True
                if not inherited:
                    continue
                observed_identity = identity(pid)
                if observed_identity is None:
                    continue
                _require(
                    row.get('owner_identity') is not None
                    and observed_identity == row['owner_identity'] and process_identity(pid) == before,
                    'own invocation changed process identity')
                _require(isinstance(row.get('ticket'), str) and re.fullmatch('[0-9]+-[a-f0-9]{32}', row['ticket']), 'own invocation ticket differs')
                matches.append({'entry': row, 'process': before, 'completion_lock': {'device': lock.st_dev, 'inode': lock.st_ino}})
            except (FileNotFoundError, ProcessLookupError):
                continue
        _require(len(matches) <= 1, 'multiple scheduler invocations hold own job completion lock')
        return matches[0] if matches else None

    def settle(self, job):
        terminal = self.completed(job)
        if terminal is not None:
            self.save('owned-cleanup-completion-race.json', terminal)
            return True
        p = self.command(['./task', 'test:status'], 'owned-cleanup-status', 15)
        _require(p.returncode == 0, 'cannot identify own unsettled invocation')
        selected = self.invocation(job, json.loads(p.stdout))
        if selected is None:
            terminal = self.completed(job)
            _require(terminal is not None, 'own job has neither provable invocation nor settled completion')
            self.save('owned-cleanup-completion-race.json', terminal)
            return True
        self.save('owned-cleanup-invocation.json', selected)
        ticket = selected['entry']['ticket']
        for attempt in range(2):
            p = self.command(
                ['./task', 'test:cancel', '--', ticket, '--reason',
                 'owned verification job ended before caller cleanup', '--force'],
                'owned-cancel-' + str(attempt), 30)
            if p.returncode != 0:
                terminal = self.completed(job)
                if terminal is not None:
                    self.save('owned-cleanup-completion-race.json', terminal)
                    return True
                _require(False, 'exact invocation cancellation failed without terminal completion')
            result = json.loads(p.stdout)
            _require(result.get('ticket') == ticket, 'cancellation acknowledgment differs from exact invocation')
            if p.returncode == 0 and result.get('released') is True:
                return True
            terminal = self.completed(job)
            if terminal is not None:
                self.save('owned-cleanup-completion-race.json', terminal)
                return True
        _require(False, 'scheduler did not release exact own invocation')
