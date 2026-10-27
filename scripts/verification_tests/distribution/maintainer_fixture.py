"""Bind shipped maintainer programs to fixed state beneath their scratch root."""
import re
from pathlib import Path

GENERATIONS = '/opt/painted-wolf-code/bin/pw-generations'
PACKAGE_STATE = '/var/lib/painted-wolf-code'
APPARMOR_ROOT = '/sys/kernel/security/apparmor'
FIXED_ROOTS = ('/opt/painted-wolf-code', '/etc/paintedwolf', '/run/paintedwolf', PACKAGE_STATE, APPARMOR_ROOT)


def program(source: str, root: Path, generations=None) -> str:
    generations = generations or root / 'pw-generations'
    for path in (root, generations):
        if path == Path('/') or '..' in path.parts or not re.fullmatch(r'/[A-Za-z0-9_./-]+', str(path)):
            raise ValueError('maintainer fixture requires an absolute shell-safe scratch path')
    if not generations.is_relative_to(root):
        raise ValueError('maintainer fixture executable must belong to its scratch root')
    assignment = 'PW_GENERATIONS=' + GENERATIONS
    if source.count(assignment) != int('PW_GENERATIONS' in source):
        raise ValueError('unbound host generation executable in fixture')
    source = source.replace(assignment, 'PW_GENERATIONS=' + str(generations))
    if GENERATIONS in source:
        raise ValueError('unbound host generation executable in fixture')
    for fixed in FIXED_ROOTS:
        source = source.replace(fixed, str(root / fixed.removeprefix('/')))
    unbound = source
    for fixed in FIXED_ROOTS:
        unbound = unbound.replace(str(root / fixed.removeprefix('/')), '<scratch>')
    if any(fixed in unbound for fixed in FIXED_ROOTS):
        raise ValueError('unbound host package state in fixture')
    return source
