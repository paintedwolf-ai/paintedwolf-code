"""Isolate active runtime copies and share identical files after shutdown."""
import argparse
import ctypes
import fcntl
import hashlib
import os
import pathlib
import shutil
import sys
import tempfile
from snapshot import RUNTIME_METADATA

PRIVATE_CONFIG_FILES = ('credential-vault.age', '.credential-vault-development-identity',
                        'providers.local.yaml', 'model-policy.yaml', 'api.token')


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
    clone_file(engine, capture / 'engine')
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
