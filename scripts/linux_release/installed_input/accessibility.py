"""Pin an embedded control to its actual enumerated native window, across AT-SPI services."""
from __future__ import annotations
import re
import time

from ..installed_cases.records import CaseUnavailable

MAX_PARENTS = 64


def identity(node) -> dict:
    value = {'bus_name': node.app.bus_name, 'path': node.path, 'pid': node.get_process_id()}
    validate_identity(value)
    return value


def validate_identity(value):
    if (not isinstance(value, dict) or set(value) != {'bus_name', 'path', 'pid'}
            or not isinstance(value['bus_name'], str)
            or not re.fullmatch(r':[0-9]+\.[0-9]+', value['bus_name'])
            or len(value['bus_name']) > 255 or not isinstance(value['path'], str)
            or len(value['path']) > 512 or not re.fullmatch(r'/[A-Za-z0-9_/]*', value['path'])
            or type(value['pid']) is not int or value['pid'] <= 1):
        raise ValueError('native accessible object identity is invalid')
    return value


def validate(chain, entry_pid, frame_pid):
    if not isinstance(chain, list) or not 1 <= len(chain) <= MAX_PARENTS:
        raise ValueError('native accessible ancestor chain exceeds its bound')
    for row in chain:
        validate_identity(row)
    keys = [(row['bus_name'], row['path']) for row in chain]
    if len(set(keys)) != len(keys) or chain[0]['pid'] != entry_pid or chain[-1]['pid'] != frame_pid:
        raise ValueError('native accessible ancestor endpoints or object identities differ')
    return chain


def capture(node, windows):
    """Select only a real native inventory window reached through actual parent links."""
    selected = {tuple(identity(window).items()): window for _, window in windows}
    nodes, chain = [], []
    for _ in range(MAX_PARENTS):
        if node is None:
            break
        row = identity(node)
        if row in chain:
            raise ValueError('native accessible parent chain cycles')
        nodes.append(node)
        chain.append(row)
        if tuple(row.items()) in selected:
            for index, current in enumerate(nodes):
                if identity(current) != chain[index]:
                    raise ValueError('native accessible object changed during ancestor observation')
                if index + 1 < len(nodes) and identity(current.get_parent()) != chain[index + 1]:
                    raise ValueError('native accessible parent link changed during observation')
            return selected[tuple(row.items())], validate(chain, chain[0]['pid'], row['pid'])
        node = node.get_parent()
    raise ValueError('focused entry has no bounded native inventory window ancestor')


class InventoryChanged(Exception):
    """The native service invalidated an unfinished inventory."""


def descendants(window, deadline=None):
    deadline = time.monotonic() + 15 if deadline is None else deadline
    pending, visited = [(window, 0)], 0
    while pending:
        if time.monotonic() >= deadline:
            raise CaseUnavailable('normal-native-control-observation-deadline')
        node, depth = pending.pop()
        visited += 1
        if node is None:
            raise InventoryChanged()
        count = node.childCount
        if type(count) is int and count == -1:
            raise InventoryChanged()
        if type(count) is not int or visited > 4096 or depth > 64 or not 0 <= count <= 1024:
            error = CaseUnavailable('installed accessible window exceeds its fixed observer traversal bound')
            error.observations = {'desktop_inventory_bound': {'visited': visited, 'depth': depth,
                'child_count': count if type(count) is int else None,
                'limits': {'visited': 4096, 'depth': 64, 'child_count': 1024}}}
            raise error
        yield node
        pending.extend((node[index], depth + 1) for index in range(count))
