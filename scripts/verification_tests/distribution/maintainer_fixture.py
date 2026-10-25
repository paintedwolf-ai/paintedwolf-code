"""Bind shipped maintainer programs to their two fixed scratch authorities."""
import re
from pathlib import Path

GENERATIONS = '/opt/painted-wolf-code/bin/pw-generations'
PACKAGE_STATE = '/var/lib/painted-wolf-code'


def program(source: str, generations: Path, package_state: Path) -> str:
    for path in (generations, package_state):
        if not re.fullmatch(r'/[A-Za-z0-9_./-]+', str(path)):
            raise ValueError('maintainer fixture requires an absolute shell-safe scratch path')
    assignment = 'PW_GENERATIONS=' + GENERATIONS
    if source.count(assignment) != int('PW_GENERATIONS' in source):
        raise ValueError('unbound host generation executable in fixture')
    source = source.replace(assignment, 'PW_GENERATIONS=' + str(generations))
    if GENERATIONS in source:
        raise ValueError('unbound host generation executable in fixture')
    source = source.replace(PACKAGE_STATE, str(package_state))
    if PACKAGE_STATE in source:
        raise ValueError('unbound host package state in fixture')
    return source
