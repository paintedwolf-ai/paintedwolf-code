"""Observe only attributes of owned visible alerts, never accessible text or field values."""
from __future__ import annotations

import time

from .app import process_identity
from .desktop_ui import retry_inventory
from .records import CaseUnavailable
from ..installed_input import accessibility, lineage


def alerts(app, ui, deadline):
    app.require_running()
    app.require_engine()
    root = dict(app.observations['launch_identity'])
    engine = dict(app.observations['engine_identity'])
    windows, result = list(ui.windows(deadline)), []
    for _, window in windows:
        for node in ui.descendants(window, deadline):
            if node.getRole() != ui.api.ROLE_ALERT or not ui.visible(node):
                continue
            if len(result) >= 8:
                raise CaseUnavailable('owned-presence-alert-inventory-exceeds-bound')
            frame, chain = accessibility.capture(node, windows)
            control = process_identity(node.get_process_id())
            surface = process_identity(frame.get_process_id())
            entry_chain, frame_chain = lineage.capture(surface, control), lineage.capture(root, surface)
            attributes = node.get_attributes()
            if (not isinstance(attributes, (list, tuple)) or len(attributes) > 64
                    or any(not isinstance(value, str) or len(value.encode()) > 2048 for value in attributes)
                    or sum(len(value.encode()) for value in attributes) > 16 << 10):
                raise CaseUnavailable('owned-presence-alert-attributes-exceed-bound')
            if (accessibility.capture(node, list(ui.windows(deadline)))[1] != chain
                    or lineage.capture(surface, control) != entry_chain
                    or lineage.capture(root, surface) != frame_chain or not ui.visible(node)):
                raise ValueError('owned presence alert changed during attribute observation')
            result.append({'accessible_chain': chain, 'entry_lineage': entry_chain,
                           'frame_lineage': frame_chain, 'attributes': list(attributes),
                           'role': int(ui.api.ROLE_ALERT), 'visible': True})
    app.require_running()
    app.require_engine()
    if (app.observations['launch_identity'] != root or app.observations['engine_identity'] != engine):
        raise ValueError('owned presence alert launch or engine identity changed')
    return result


def await_alert(app, ui):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        try:
            observed = alerts(app, ui, deadline)
        except (ValueError, CaseUnavailable):
            raise
        except Exception as error:
            if not retry_inventory(app, error):
                raise CaseUnavailable('owned-presence-alert-attributes-unavailable') from None
            observed = []
        if observed:
            if len(observed) != 1:
                raise CaseUnavailable('owned-presence-alert-is-ambiguous')
            return observed[0]
        time.sleep(0.05)
    raise CaseUnavailable('owned-presence-alert-not-observed')
