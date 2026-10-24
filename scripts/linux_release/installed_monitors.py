"""Configure declared monitor scales in an owned disposable GNOME session."""
from __future__ import annotations

import os

from .installed_cases.products import require_owned_guest
from .installed_cases.records import CaseUnavailable
from .installed_environment import query
from .installed_variants import gnome_monitors


def configuration(state: dict, percentages: list[int]) -> list[str]:
    physical = state['data'][1]
    if not physical or len(physical) > 16 or len(percentages) > len(physical):
        raise CaseUnavailable('required-physical-monitor-layout-unavailable')
    for monitor in physical:
        if (not isinstance(monitor, list) or len(monitor) != 3 or not isinstance(monitor[0], list)
                or len(monitor[0]) != 4 or any(not isinstance(item, str) or not 0 < len(item) <= 128 for item in monitor[0])
                or not isinstance(monitor[1], list) or not 1 <= len(monitor[1]) <= 128):
            raise ValueError('compositor physical monitor identity is malformed')
        for mode in monitor[1]:
            if (not isinstance(mode, list) or len(mode) != 7 or not isinstance(mode[0], str) or not 0 < len(mode[0]) <= 128
                    or any(type(mode[item]) is not int or not 1 <= mode[item] <= 16384 for item in (1, 2))
                    or not isinstance(mode[5], list) or not mode[5]
                    or any(type(item) not in {int, float} or not 1 <= item <= 4 for item in mode[5])):
                raise ValueError('compositor monitor mode is malformed')
    selected = sorted(physical, key=lambda item: item[0])
    scales = percentages if len(percentages) > 1 else percentages * len(selected)
    if len(scales) != len(selected):
        raise CaseUnavailable('mixed-monitor-layout-requires-exact-connected-monitor-count')
    result, x = [str(state['data'][0]), '1', str(len(selected))], 0
    for index, (monitor, percent) in enumerate(zip(selected, scales)):
        if type(percent) is not int or not 100 <= percent <= 400:
            raise ValueError('monitor preparation requires declared integer scale percentages')
        scale = percent / 100
        modes = [mode for mode in monitor[1] if len(mode) == 7 and scale in mode[5]
                 and mode[1] / scale >= 900 and mode[2] / scale >= 600
                 and mode[1] / scale == int(mode[1] / scale) and mode[2] / scale == int(mode[2] / scale)]
        if not modes:
            raise CaseUnavailable('connected-monitor-has-no-usable-mode-for-required-scale')
        mode = min(modes, key=lambda item: (abs(item[1] * item[2] / scale ** 2 - 1280 * 960), item[0]))
        if x + int(mode[1] / scale) > 16384:
            raise ValueError('declared monitor layout exceeds the bounded desktop width')
        result.extend([str(x), '0', str(scale), '0', 'true' if index == 0 else 'false',
                       '1', monitor[0][0], mode[0], '0'])
        x += int(mode[1] / scale)
    result.append('0')
    return result


def prepare(binding: dict, session: dict, percentages: list[int]) -> dict:
    require_owned_guest(binding)
    before = gnome_monitors(session)
    if before['scales_percent'] == sorted(percentages):
        return {'changed': False, 'before': before, 'after': before}
    arguments = configuration(before['state'], percentages)
    response = query(['busctl', '--address=unix:path=/run/user/' + str(os.getuid()) + '/bus', '--json=short', 'call',
                      'org.gnome.Mutter.DisplayConfig', '/org/gnome/Mutter/DisplayConfig',
                      'org.gnome.Mutter.DisplayConfig', 'ApplyMonitorsConfig',
                      'uua(iiduba(ssa{sv}))a{sv}', *arguments])
    if response['exit_code'] != 0:
        raise CaseUnavailable('compositor-refused-declared-temporary-monitor-configuration')
    after = gnome_monitors(session)
    if after['scales_percent'] != sorted(percentages):
        raise ValueError('compositor did not establish the selected monitor scales')
    return {'changed': True, 'before': before, 'after': after}
