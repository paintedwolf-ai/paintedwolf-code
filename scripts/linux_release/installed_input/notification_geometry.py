"""Admit only an explicit single-monitor logical-to-pixel mapping for the fixed click."""
from __future__ import annotations

from ..installed_cases.records import CaseUnavailable


def sequence(value, count):
    return isinstance(value, (tuple, list)) and len(value) == count


def mapping(state):
    if not sequence(state, 4) or type(state[0]) is not int or not 0 < state[0] < 1 << 32:
        raise ValueError('notification display state has an invalid serial or shape')
    serial, physical, logical, properties = state
    if (not sequence(physical, 1) or not sequence(logical, 1) or not isinstance(properties, dict)
            or type(properties.get('layout-mode')) is not int or properties['layout-mode'] != 2):
        raise CaseUnavailable('notification-pointer-requires-one-logical-layout-monitor')
    monitor, display = physical[0], logical[0]
    if not sequence(monitor, 3) or not sequence(display, 7):
        raise ValueError('notification display state has an invalid monitor shape')
    identity, modes, _ = monitor
    if (not sequence(identity, 4) or any(not isinstance(x, str) or not x or len(x) > 128 for x in identity)
            or not isinstance(modes, (tuple, list)) or not 1 <= len(modes) <= 128):
        raise ValueError('notification physical monitor identity is malformed')
    x, y, scale, transform, primary, members, _ = display
    if (type(x) is not int or type(y) is not int or x != 0 or y != 0
            or type(scale) not in {int, float} or scale != 1 or type(transform) is not int
            or transform != 0 or primary is not True or not sequence(members, 1)
            or list(members[0]) != list(identity)):
        raise CaseUnavailable('notification-pointer-requires-one-untransformed-scale100-display')
    current = []
    for mode in modes:
        if not sequence(mode, 7) or not isinstance(mode[6], dict):
            raise ValueError('notification physical monitor mode is malformed')
        if mode[6].get('is-current') is True:
            current.append(mode)
    if len(current) != 1:
        raise CaseUnavailable('notification-pointer-current-display-mode-is-ambiguous')
    mode = current[0]
    width, height = mode[1:3]
    if (type(width) is not int or type(height) is not int or not 8 <= width <= 16384
            or not 8 <= height <= 16384 or not isinstance(mode[5], (tuple, list)) or not mode[5]
            or any(type(scale) not in {int, float} or not 1 <= scale <= 4 for scale in mode[5]) or 1 not in mode[5]):
        raise ValueError('notification current display mode has invalid pixel geometry')
    return {'serial': serial, 'logical_extent': [0, 0, width, height], 'pixel_size': [width, height],
        'scale_percent': 100, 'transform': 0, 'layout_mode': 2}
