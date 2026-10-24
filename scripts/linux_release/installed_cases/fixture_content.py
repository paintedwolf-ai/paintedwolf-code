"""Read one explicitly bound private content object with bounded decompression."""
from __future__ import annotations

from contextlib import ExitStack
import hashlib
import os
import re
import selectors
import stat
import subprocess
import time

from .package_payload import stable
from .profile_observer import identifier, physical, profile
from .records import CaseUnavailable

LIMIT = 64 << 10
TIMEOUT = 5


def decompress(stream, size):
    try:
        child = subprocess.Popen(['zstd', '--decompress', '--stdout', '--quiet'], stdin=stream,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=True)
    except FileNotFoundError:
        raise CaseUnavailable('fixture-zstd-observer-unavailable') from None
    try:
        deadline, output = time.monotonic() + TIMEOUT, bytearray()
        with selectors.DefaultSelector() as selector:
            selector.register(child.stdout, selectors.EVENT_READ)
            while True:
                remaining = deadline - time.monotonic()
                if remaining <= 0 or not selector.select(remaining):
                    raise CaseUnavailable('fixture-content-decompression-deadline')
                block = os.read(child.stdout.fileno(), min(4096, size + 1 - len(output)))
                if not block:
                    break
                output.extend(block)
                if len(output) > size:
                    raise ValueError('fixture content exceeds its exact byte size')
        try:
            code = child.wait(timeout=max(0, deadline - time.monotonic()))
        except subprocess.TimeoutExpired:
            raise CaseUnavailable('fixture-content-decompression-deadline') from None
        if code != 0 or len(output) != size:
            raise ValueError('fixture content failed exact bounded decompression')
        return bytes(output)
    finally:
        try:
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=1)
                except subprocess.TimeoutExpired:
                    child.kill()
                    try:
                        child.wait(timeout=1)
                    except subprocess.TimeoutExpired:
                        raise CaseUnavailable('fixture-content-child-cleanup-deadline') from None
        finally:
            child.stdout.close()


def read_object(app, project_id, digest, byte_size, stored_size, *, maximum):
    identifier(project_id)
    if (not isinstance(digest, str) or re.fullmatch('[0-9a-f]{64}', digest) is None
            or type(maximum) is not int or not 0 < maximum <= LIMIT
            or type(byte_size) is not int or not 0 < byte_size <= maximum
            or type(stored_size) is not int or not 0 < stored_size <= 2 * LIMIT):
        raise ValueError('fixture object metadata exceeds its declared bound')
    with profile(app) as directory, ExitStack() as custody:
        parent = directory
        held = []
        for segment in ('projects', project_id, 'model-content', digest[:2]):
            parent /= segment
            physical(parent, True)
            descriptor = os.open(parent, os.O_RDONLY | os.O_NOFOLLOW | os.O_DIRECTORY)
            custody.callback(os.close, descriptor)
            before = os.fstat(descriptor)
            if (not stat.S_ISDIR(before.st_mode) or before.st_uid != os.getuid()
                    or before.st_mode & 0o077 or stable(before) != stable(parent.lstat())):
                raise ValueError('fixture object parent changed during open')
            held.append((parent, descriptor, before))
        path = parent / digest[2:]
        physical(path)
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor, 'rb') as stream:
            before = os.fstat(stream.fileno())
            if (not stat.S_ISREG(before.st_mode) or before.st_uid != os.getuid()
                    or before.st_mode & 0o077 or before.st_nlink != 1
                    or before.st_size != stored_size or stable(before) != stable(path.lstat())):
                raise ValueError('fixture object size or identity differs from metadata')
            raw = decompress(stream, byte_size)
            if hashlib.sha256(raw).hexdigest() != digest:
                raise ValueError('fixture object digest differs from its bound content')
            if stable(before) != stable(os.fstat(stream.fileno())) or stable(before) != stable(path.lstat()):
                raise ValueError('fixture object changed during decompression')
        for path, descriptor, before in held:
            physical(path, True)
            if stable(before) != stable(os.fstat(descriptor)) or stable(before) != stable(path.lstat()):
                raise ValueError('fixture object parent changed during observation')
        return raw
