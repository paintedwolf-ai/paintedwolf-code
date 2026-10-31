"""Isolate active runtime copies, checkpoint closed stores, and share identical files after shutdown."""
import argparse
from contextlib import closing, contextmanager
import ctypes
import fcntl
import hashlib
import os
import pathlib
import shutil
import sqlite3
import sys
import tempfile
from snapshot import RUNTIME_METADATA

RUNTIME_EXECUTABLES = ('pw-logs', 'pw-document-core', 'bialy')

PRIVATE_CONFIG_FILES = ('credential-vault.age', '.credential-vault-development-identity',
                        'providers.local.yaml', 'model-policy.yaml', 'api.token')


@contextmanager
def closed_ledger(capture):
    database = capture / 'store.db'
    wal = database.with_name(database.name + '-wal')
    if wal.exists() and wal.stat().st_size:
        raise ValueError('worker ledger has uncheckpointed writes; wait for application shutdown')
    with closing(sqlite3.connect(database.resolve().as_uri() + '?mode=ro&immutable=1', uri=True)) as db:
        db.row_factory = sqlite3.Row
        yield db


def checkpoint_closed_store(database, process, timeout=30):
    if process.poll() is None:
        raise ValueError('application must exit before checkpointing its ledger')
    if database.is_symlink() or not database.is_file():
        raise ValueError('application ledger is not a regular file')
    with closing(sqlite3.connect(database.resolve().as_uri() + '?mode=rw', uri=True, timeout=timeout)) as db:
        busy, frames, copied = db.execute('PRAGMA wal_checkpoint(TRUNCATE)').fetchone()
        if busy or frames != copied:
            raise ValueError('application ledger checkpoint remains blocked')
    wal = database.with_name(database.name + '-wal')
    if wal.exists() and wal.stat().st_size:
        raise ValueError('application ledger changed after checkpointing')
    return {'busy': busy, 'log_frames': frames, 'checkpointed_frames': copied}


def cleanup_private_config(config, exit_record):
    if exit_record.get('kind') not in {'exited', 'interrupted', 'worker_failed'}:
        return
    if config.is_symlink():
        raise ValueError('private configuration directory is a symlink')
    if not config.exists():
        return
    if not config.is_dir():
        raise ValueError('private configuration path is not a directory')
    for name in PRIVATE_CONFIG_FILES:
        (config / name).unlink(missing_ok=True)


def clone_file(source, destination):
    source, destination = pathlib.Path(source), pathlib.Path(destination)
    if destination.exists() or destination.is_symlink():
        raise FileExistsError(destination)
    if sys.platform == 'darwin':
        clone = ctypes.CDLL(None, use_errno=True).clonefile
        clone.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_int]
        clone.restype = ctypes.c_int
        if clone(os.fsencode(source), os.fsencode(destination), 0) == 0:
            return str(destination)
    elif sys.platform.startswith('linux'):
        try:
            with source.open('rb') as original, destination.open('xb') as captured:
                fcntl.ioctl(captured.fileno(), 0x40049409, original.fileno())  # FICLONE
            shutil.copystat(source, destination)
            return str(destination)
        except OSError:
            destination.unlink(missing_ok=True)
    return shutil.copy2(source, destination)


def stage_runtime(engine, catalog, capture):
    executables = [engine, *[engine.parent / name for name in RUNTIME_EXECUTABLES]]
    for executable in executables:
        if executable.is_symlink() or not executable.is_file() or not os.access(executable, os.X_OK):
            raise ValueError('required runtime executable is unavailable: ' + str(executable))
    clone_file(engine, capture / 'engine')
    for executable in executables[1:]:
        clone_file(executable, capture / executable.name)
    shutil.copytree(catalog, capture / 'module/config', copy_function=clone_file,
                    symlinks=True, ignore=shutil.ignore_patterns(*RUNTIME_METADATA))


def digest(path):
    result = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            result.update(chunk)
    return result.digest()


def share_file(original, captured):
    if not original.is_file() or not captured.is_file() or original.is_symlink() or captured.is_symlink():
        return
    if original.samefile(captured):
        return
    left, right = original.stat(), captured.stat()
    if left.st_dev != right.st_dev or left.st_size != right.st_size or digest(original) != digest(captured):
        return
    fd, name = tempfile.mkstemp(prefix='.runtime-', dir=captured.parent)
    os.close(fd)
    temporary = pathlib.Path(name)
    try:
        temporary.unlink()
        os.link(original, temporary)
        temporary.replace(captured)
    finally:
        temporary.unlink(missing_ok=True)


def deduplicate_runtime(capture, source):
    share_file(source / '.bin/lycaon-dev', capture / 'engine')
    for name in RUNTIME_EXECUTABLES:
        share_file(source / '.bin' / name, capture / name)
    catalog = source / 'lycaon/config'
    for original in catalog.rglob('*'):
        if original.is_file():
            share_file(original, capture / 'module/config' / original.relative_to(catalog))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--engine', required=True, type=pathlib.Path)
    parser.add_argument('--catalog', required=True, type=pathlib.Path)
    parser.add_argument('--capture', required=True, type=pathlib.Path)
    args = parser.parse_args()
    stage_runtime(args.engine, args.catalog, args.capture)
