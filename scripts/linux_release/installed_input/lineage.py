"""Bind an actual native UI process to its separately captured launch process."""
from __future__ import annotations

from ..installed_cases.app import process_identity

MAX_ANCESTRY = 32


def identity(value: dict) -> dict:
    if (not isinstance(value, dict) or set(value) != {'pid', 'parent_pid', 'start_time_ticks'}
            or any(type(number) is not int for number in value.values())
            or value['pid'] <= 1 or value['parent_pid'] < 1 or value['start_time_ticks'] <= 0):
        raise ValueError('native input process identity is invalid')
    return value


def validate(chain: list, root: dict, candidate: dict) -> list:
    identity(root)
    identity(candidate)
    if not isinstance(chain, list) or not 1 <= len(chain) <= MAX_ANCESTRY:
        raise ValueError('native input lineage exceeds its fixed bound')
    for row in chain:
        identity(row)
    if (chain[0] != candidate or chain[-1] != root
            or len({row['pid'] for row in chain}) != len(chain)
            or any(child['parent_pid'] != parent['pid'] for child, parent in zip(chain, chain[1:]))):
        raise ValueError('native input lineage does not bind the captured processes')
    return chain


def capture(root: dict, candidate: dict) -> list:
    identity(root)
    identity(candidate)
    chain, pid, seen = [], candidate['pid'], set()
    while pid > 1 and pid not in seen and len(chain) < MAX_ANCESTRY:
        seen.add(pid)
        row = identity(process_identity(pid))
        if row['pid'] != pid:
            raise ValueError('native input process observer changed its selected PID')
        chain.append(row)
        if pid == root['pid']:
            break
        pid = row['parent_pid']
    validate(chain, root, candidate)
    # Every parent and birth fact must survive the whole capture, including both endpoints.
    if any(process_identity(row['pid']) != row for row in reversed(chain)):
        raise ValueError('native input lineage changed during kernel observation')
    return chain
