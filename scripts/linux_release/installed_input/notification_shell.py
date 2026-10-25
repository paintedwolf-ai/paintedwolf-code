"""Observe the real shell notification and display mapping immediately before a fixed click."""
from __future__ import annotations

import hashlib
import os
import time

from . import accessibility, notification_geometry, notification_wire
from ..installed_cases.app import process_identity
from ..installed_cases.desktop_ui import load_api
from ..installed_cases.records import CaseFailed, CaseUnavailable
from ..installed_cases.logind_context import Reader
from .. import installed_gnome
from .. import scripts_module

BUS = 'org.freedesktop.DBus'
BUS_PATH = '/org/freedesktop/DBus'
MUTTER = 'org.gnome.Mutter.DisplayConfig'
MUTTER_PATH = '/org/gnome/Mutter/DisplayConfig'
STATE_SIGNATURE = '(ua((ssss)a(siiddada{sv})a{sv})a(iiduba(ssss)a{sv})a{sv})'


def ancestry(node, terminal):
    nodes, identities = [], []
    selected = accessibility.identity(terminal)
    for _ in range(64):
        identity = accessibility.identity(node)
        if identity in identities:
            raise ValueError('notification accessible parent chain cycles')
        identities.append(identity)
        nodes.append(node)
        if identity == selected:
            for index, current in enumerate(nodes):
                if accessibility.identity(current) != identities[index] or index + 1 < len(nodes) and (
                        accessibility.identity(current.get_parent()) != identities[index + 1]):
                    raise accessibility.InventoryChanged()
            return identities, nodes
        node = node.get_parent()
    raise CaseUnavailable('notification-accessible-parent-chain-exceeds-bound')


def extent(node, api):
    return list(node.queryComponent().getExtents(api.DESKTOP_COORDS))


def follow(app, expected, deadline):
    node = app
    if accessibility.identity(app) != expected[-1]:
        raise CaseFailed('notification shell accessible application was replaced')
    for selected in reversed(expected[:-1]):
        if time.monotonic() >= deadline:
            raise CaseUnavailable('notification-shell-predispatch-deadline')
        count = node.childCount
        if type(count) is not int or not 0 <= count <= 1024:
            raise accessibility.InventoryChanged()
        matches = []
        for index in range(count):
            if time.monotonic() >= deadline:
                raise CaseUnavailable('notification-shell-predispatch-deadline')
            child = node[index]
            if accessibility.identity(child) == selected:
                matches.append(child)
        if len(matches) != 1 or node.childCount != count:
            raise CaseUnavailable('notification-shell-pinned-ancestor-was-replaced')
        node = matches[0]
    return node


def capture_target(api, shell_process, delivery, geometry, deadline, expected=None):
    desktop = api.Registry.getDesktop(0)
    count = desktop.childCount
    if type(count) is not int or not 0 <= count <= 256:
        raise ValueError('notification accessible application count exceeds its bound')
    apps = [desktop[index] for index in range(count) if desktop[index].get_process_id() == shell_process['pid']]
    if desktop.childCount != count or len(apps) != 1:
        raise CaseUnavailable('captured-shell-accessible-application-is-missing-or-ambiguous')
    app, matches = apps[0], []
    nodes = [follow(app, expected['ancestry'], deadline)] if expected is not None else accessibility.descendants(app, deadline)
    for node in nodes:
        if node.getRole() != api.ROLE_NOTIFICATION:
            continue
        state = node.getState()
        if state.contains(api.STATE_DEFUNCT) or not state.contains(api.STATE_SHOWING):
            continue
        candidates = {key: [] for key in ('title_sha256', 'body_sha256')}
        for label in accessibility.descendants(node, deadline):
            if label.getRole() != api.ROLE_LABEL:
                continue
            digest = hashlib.sha256(label.name.encode()).hexdigest()
            for key in candidates:
                if digest == delivery[key]:
                    candidates[key].append(label)
        if any(len(items) != 1 for items in candidates.values()):
            continue
        parents, parent_nodes = ancestry(node, app)
        labels = []
        for key in candidates:
            label = candidates[key][0]
            chain, _ = ancestry(label, node)
            labels.append({'identity': accessibility.identity(label), 'ancestry': chain,
                'role': label.getRole(), 'name_sha256': hashlib.sha256(label.name.encode()).hexdigest()})
        screen = geometry['logical_extent']
        observed = []
        for parent in parent_nodes:
            try:
                observed.append(extent(parent, api))
            except NotImplementedError:
                pass
        if screen not in observed:
            raise CaseUnavailable('notification-shell-ancestor-does-not-match-observed-display-geometry')
        matches.append({'identity': accessibility.identity(node), 'ancestry': parents,
            'role': node.getRole(), 'extent': extent(node, api), 'visible': bool(state.contains(api.STATE_VISIBLE)),
            'showing': bool(state.contains(api.STATE_SHOWING)), 'enabled': bool(state.contains(api.STATE_ENABLED)),
            'sensitive': bool(state.contains(api.STATE_SENSITIVE)), 'labels': labels})
    if len(matches) != 1:
        raise CaseUnavailable('delivered-shell-notification-is-missing-or-ambiguous')
    return matches[0]


def observe(surface, service, shell, delivery, deadline=None, expected=None):
    """Capture shell authority separately from the original product runtime context."""
    deadline = time.monotonic() + 15 if deadline is None else deadline
    runtime = '/run/user/' + str(os.getuid())
    environment = {'DBUS_SESSION_BUS_ADDRESS': 'unix:path=' + runtime + '/bus', 'XDG_RUNTIME_DIR': runtime}
    with Reader(user_only=True, deadline=deadline) as reader:
        def query(name, value):
            signature = '(s)' if name == 'GetNameOwner' else '(u)'
            return reader.call(BUS_PATH, BUS, name, ('(s)', (value,)), signature,
                user=True, destination=BUS).unpack()[0]

        def require_owners():
            native = installed_gnome.owner(reader)
            if (native['unique_owner'] != shell['bus_name'] or native['pid'] != shell['process']['pid']
                    or native['start_time_ticks'] != shell['process']['start_time_ticks']):
                raise CaseFailed('notification native shell authority changed before physical dispatch')
            for name, expected in (('org.freedesktop.Notifications', service), (MUTTER, shell)):
                if (query('GetNameOwner', name) != expected['bus_name']
                        or query('GetConnectionUnixUser', expected['bus_name']) != os.getuid()
                        or query('GetConnectionUnixProcessID', expected['bus_name']) != expected['process']['pid']
                        or process_identity(expected['process']['pid']) != expected['process']):
                    raise CaseFailed('notification shell or service owner changed before physical dispatch')
            for expected in (surface['launch'], surface['engine'], surface['main']['process']):
                if process_identity(expected['pid']) != expected:
                    raise CaseFailed('notification product runtime changed before physical dispatch')

        require_owners()
        result = reader.call(MUTTER_PATH, MUTTER, 'GetCurrentState', None, STATE_SIGNATURE,
            user=True, destination=shell['bus_name'])
        if result.get_type_string() != STATE_SIGNATURE:
            raise ValueError('notification compositor returned an unexpected typed display state')
        geometry = notification_geometry.mapping(result.unpack())
        geometry.update(display_owner=shell, shell_extent=list(geometry['logical_extent']))
        # The target's actual ancestor must independently report this rectangle.
        with scripts_module('harness.atspi_client').session_bus(environment):
            api = load_api()
            target = capture_target(api, shell['process'], delivery, geometry, deadline, expected)
        require_owners()
        if time.monotonic() >= deadline:
            raise CaseUnavailable('notification-shell-observation-deadline')
        return notification_wire.before({'surface': surface, 'service': service, 'shell': shell,
            'delivery': delivery, 'geometry': geometry, 'target': target})


def refresh(value):
    """Fresh pre-dispatch shell proof; product roots remain pinned, not re-selected."""
    notification_wire.before(value)
    fresh = observe(*(value[key] for key in ('surface', 'service', 'shell', 'delivery')),
        deadline=time.monotonic() + 4, expected=value['target'])
    notification_wire.same_target(value, fresh)
    return fresh
