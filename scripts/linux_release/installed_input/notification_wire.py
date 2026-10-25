"""One closed notification click, derived from pinned shell geometry rather than caller coordinates."""
from __future__ import annotations

import hashlib
import json
import re

from . import accessibility, lineage, surface_wire

OPERATION = 'notification-click-v1'
_HASH = re.compile(r'[a-f0-9]{64}')
_UNIQUE = re.compile(r':[0-9]+\.[0-9]+')


def fields(value, names):
    if not isinstance(value, dict) or set(value) != set(names):
        raise ValueError('notification physical proof has an unknown shape')
    return value


def owner(value):
    fields(value, ('bus_name', 'process'))
    if not isinstance(value['bus_name'], str) or not _UNIQUE.fullmatch(value['bus_name']):
        raise ValueError('notification proof requires a unique session bus owner')
    lineage.identity(value['process'])
    return value


def positive(value, maximum):
    if type(value) is not int or not 0 < value <= maximum:
        raise ValueError('notification physical identity or geometry exceeds its bound')
    return value


def rectangle(value):
    if (not isinstance(value, list) or len(value) != 4 or any(type(x) is not int for x in value)
            or not 0 <= value[0] < 16384 or not 0 <= value[1] < 16384):
        raise ValueError('notification target has invalid desktop geometry')
    positive(value[2], 16384)
    positive(value[3], 16384)
    return value


def chain(value, first, pid):
    accessibility.validate(value, pid, pid)
    if value[0] != first or any(row['pid'] != pid or row['bus_name'] != first['bus_name'] for row in value):
        raise ValueError('notification accessible ancestry escaped its captured shell')
    return value


def before(value):
    fields(value, ('surface', 'service', 'shell', 'delivery', 'geometry', 'target'))
    surface_wire.proof(value['surface'])
    service, shell = owner(value['service']), owner(value['shell'])
    delivery = fields(value['delivery'], ('notification_id', 'request_serial', 'sender', 'title_sha256', 'body_sha256'))
    positive(delivery['notification_id'], (1 << 32) - 1)
    positive(delivery['request_serial'], (1 << 32) - 1)
    if (not isinstance(delivery['sender'], str) or not _UNIQUE.fullmatch(delivery['sender'])
            or any(not isinstance(delivery[key], str) or not _HASH.fullmatch(delivery[key])
                   for key in ('title_sha256', 'body_sha256'))):
        raise ValueError('notification click has no captured typed delivery identity')
    geometry = fields(value['geometry'], ('display_owner', 'serial', 'logical_extent', 'pixel_size',
        'scale_percent', 'transform', 'layout_mode', 'shell_extent'))
    if owner(geometry['display_owner']) != shell:
        raise ValueError('notification display geometry changed its captured shell owner')
    positive(geometry['serial'], (1 << 32) - 1)
    screen = rectangle(geometry['logical_extent'])
    if (screen[:2] != [0, 0] or geometry['pixel_size'] != screen[2:]
            or geometry['shell_extent'] != screen or type(geometry['scale_percent']) is not int
            or geometry['scale_percent'] != 100 or type(geometry['transform']) is not int
            or geometry['transform'] != 0 or type(geometry['layout_mode']) is not int
            or geometry['layout_mode'] != 2):
        raise ValueError('notification click requires one untransformed scale100 logical-to-pixel display')
    target = fields(value['target'], ('identity', 'ancestry', 'role', 'extent',
        'showing', 'visible', 'sensitive', 'enabled', 'labels'))
    identity = accessibility.validate_identity(target['identity'])
    chain(target['ancestry'], identity, shell['process']['pid'])
    if (type(target['role']) is not int or target['role'] != 101
            or any(target[key] is not True for key in ('showing', 'visible', 'sensitive', 'enabled'))):
        raise ValueError('notification click requires its actual visible actionable notification role')
    extent = rectangle(target['extent'])
    if extent[2] < 8 or extent[3] < 8 or extent[0] + extent[2] > screen[2] or extent[1] + extent[3] > screen[3]:
        raise ValueError('notification click target is outside its pinned display')
    labels = target['labels']
    if not isinstance(labels, list) or len(labels) != 2:
        raise ValueError('notification click requires unique delivered title and body labels')
    for label, key in zip(labels, ('title_sha256', 'body_sha256')):
        fields(label, ('identity', 'ancestry', 'role', 'name_sha256'))
        accessibility.validate_identity(label['identity'])
        ancestry = chain(label['ancestry'], label['identity'], shell['process']['pid'])
        if (ancestry[-1] != identity or label['identity'] == identity or type(label['role']) is not int
                or label['role'] != 29 or label['name_sha256'] != delivery[key]):
            raise ValueError('notification delivered labels escaped their observed notification ancestor')
    if labels[0]['identity'] == labels[1]['identity']:
        raise ValueError('notification delivered labels are not distinct objects')
    if service['process']['pid'] == value['surface']['main']['process']['pid'] or shell['process']['pid'] == value['surface']['main']['process']['pid']:
        raise ValueError('notification shell authority cannot be the product UI process')
    return value


def same_target(previous, current):
    before(previous)
    before(current)
    surface_wire.same_runtime(previous['surface'], current['surface'])
    if any(previous[key] != current[key] for key in ('service', 'shell', 'delivery', 'geometry', 'target')):
        raise ValueError('notification owner, ancestry, delivery or geometry changed before dispatch')


def qmp_events(value):
    before(value)
    x, y, width, height = value['target']['extent']
    screen_width, screen_height = value['geometry']['pixel_size']
    # QEMU's absolute axes span 0..32767. Only the host derives this center.
    px = ((2 * x + width - 1) * 32767) // (2 * (screen_width - 1))
    py = ((2 * y + height - 1) * 32767) // (2 * (screen_height - 1))
    return [{'type': 'abs', 'data': {'axis': 'x', 'value': px}},
        {'type': 'abs', 'data': {'axis': 'y', 'value': py}},
        {'type': 'btn', 'data': {'down': True, 'button': 'left'}},
        {'type': 'btn', 'data': {'down': False, 'button': 'left'}}]


def event_sha256(value):
    return hashlib.sha256(json.dumps(qmp_events(value), sort_keys=True, separators=(',', ':')).encode()).hexdigest()
