"""Desktop appearance reaches the installed app while its shell runs under the protected launch.

The Settings portal names its caller from /proc/<pid>/root, which the protected
namespace hides, so the shell starts its pw-desktop-settings observer through
the launch broker. The case checks both halves:

- Route: the running observer is in the session's own user namespace and is not
  the shell's child, so the broker started it.
- Effect: changing one setting the observer reads changes the colours Den paints,
  through AT-SPI text attributes, and restoring it brings them back.
"""
from __future__ import annotations

import os
from pathlib import Path
import subprocess
import time

from .app import InstalledApp, session_environment
from .desktop_ui import DesktopUI
from .records import CaseFailed, CaseUnavailable, retain

CASE = ('accessibility', 'protected-desktop-appearance')
OBSERVER = 'pw-desktop-settings'
SETTLE_SECONDS = 15
TEXT_NODES = 512


def toggle_for(desktop: str) -> dict | None:
    """The one setting this desktop's observer path reads and the case may flip, as fixed argv."""
    names = {part.casefold() for part in desktop.split(':')}
    if names & {'gnome', 'ubuntu'}:
        return {'kind': 'gnome-high-contrast',
                'read': ['gsettings', 'get', 'org.gnome.desktop.a11y.interface', 'high-contrast'],
                'set': lambda value: ['gsettings', 'set', 'org.gnome.desktop.a11y.interface', 'high-contrast', value],
                'changed': lambda original: 'false' if original.strip() == 'true' else 'true'}
    if 'kde' in names:
        return {'kind': 'kde-accent',
                'read': ['kreadconfig6', '--file', 'kdeglobals', '--group', 'General', '--key', 'AccentColor'],
                'set': lambda value: (['kwriteconfig6', '--file', 'kdeglobals', '--group', 'General', '--key',
                                       'AccentColor', '--notify', '--delete'] if value == '' else
                                      ['kwriteconfig6', '--file', 'kdeglobals', '--group', 'General', '--key',
                                       'AccentColor', '--notify', value]),
                'changed': lambda original: '214,42,128' if original.strip() != '214,42,128' else '38,120,200'}
    return None


def setting(arguments: list[str], environment: dict) -> str:
    result = subprocess.run(arguments, stdin=subprocess.DEVNULL, capture_output=True, timeout=30,
                            env={**environment, 'PATH': '/run/current-system/sw/bin:/usr/bin:/bin'})
    if result.returncode != 0 or len(result.stdout) > 4096:
        error = CaseUnavailable('desktop-setting-tool-unavailable')
        error.observations = {'setting_tool': arguments[0], 'exit_code': result.returncode}
        raise error
    return result.stdout.decode('utf-8', 'replace').strip()


def process_row(pid: int) -> dict:
    with open(f'/proc/{pid}/stat') as stream:
        fields = stream.read().rsplit(')', 1)[1].split()
    return {'pid': pid, 'parent_pid': int(fields[1]), 'start_time_ticks': int(fields[19]),
            'user_namespace': os.readlink(f'/proc/{pid}/ns/user'),
            'executable': os.path.basename(os.readlink(f'/proc/{pid}/exe'))}


def observers() -> list[dict]:
    rows = []
    for entry in Path('/proc').iterdir():
        if not entry.name.isdigit():
            continue
        try:
            if entry.stat().st_uid != os.getuid():
                continue
            row = process_row(int(entry.name))
        except (OSError, ValueError, IndexError):
            continue
        if row['executable'] == OBSERVER:
            rows.append(row)
    return rows


def route(rows: list[dict], shell_pid: int, session_namespace: str) -> dict:
    """Whether the observer the shell started is outside the protected namespace through the broker."""
    if len(rows) != 1:
        return {'observers': len(rows), 'brokered': False, 'session_namespace': False}
    observer = rows[0]
    return {'observers': 1, 'brokered': observer['parent_pid'] != shell_pid,
            'session_namespace': observer['user_namespace'] == session_namespace}


def attributes(raw) -> dict:
    if isinstance(raw, dict):
        return dict(raw)
    items = raw if isinstance(raw, (list, tuple)) else str(raw).split(';')
    result = {}
    for item in items:
        key, separator, value = str(item).strip().partition(':')
        if separator:
            result[key.strip()] = value.strip()
    return result


def painted_colours(ui, frame) -> frozenset:
    """The foreground and background colours of the frame's text, as WebKit reports them."""
    colours, seen = set(), 0
    for node in ui.descendants(frame):
        if seen >= TEXT_NODES:
            break
        try:
            text = node.queryText()
        except NotImplementedError:
            continue
        except Exception:
            continue
        seen += 1
        try:
            run = attributes(text.getAttributeRun(0, True)[0])
        except Exception:
            continue
        colours.update(f'{key}={run[key]}' for key in ('fg-color', 'bg-color') if key in run)
    return frozenset(colours)


def follows(ui, frame, before: frozenset, *, changed: bool) -> tuple[bool, frozenset]:
    deadline = time.monotonic() + SETTLE_SECONDS
    while True:
        now = painted_colours(ui, frame)
        if (now != before) == changed and now:
            return True, now
        if time.monotonic() >= deadline:
            return False, now
        time.sleep(0.25)


def execute(binding: dict, capability: str, case: str, root: Path) -> dict:
    if (capability, case) != CASE:
        return retain(binding, capability, case, root, 'unverified', {'reason': 'native-desktop-interaction-driver-required'})
    observations, status = {}, 'failed'
    app = InstalledApp(binding)
    restore = None
    try:
        with app:
            session = app.observations['session']
            toggle = toggle_for(session.get('Desktop', ''))
            if toggle is None:
                raise CaseUnavailable('desktop-appearance-toggle-unsupported')
            environment = session_environment(session)
            with DesktopUI(app) as ui:
                identity, frame = next(ui.windows())
                shell_pid = identity['pid']
                deadline = time.monotonic() + SETTLE_SECONDS
                while not (rows := observers()) and time.monotonic() < deadline:
                    time.sleep(0.25)
                observations['route'] = route(rows, shell_pid, os.readlink('/proc/self/ns/user'))
                observations['observer_processes'] = rows
                original = setting(toggle['read'], environment)
                observations['toggle'] = {'kind': toggle['kind'], 'original': original}
                baseline = painted_colours(ui, frame)
                if not baseline:
                    raise CaseUnavailable('painted-text-colours-unavailable')
                restore = lambda: setting(toggle['set'](original), environment)
                setting(toggle['set'](toggle['changed'](original)), environment)
                changed, during = follows(ui, frame, baseline, changed=True)
                restore()
                restore = None
                returned, after = follows(ui, frame, during, changed=True)
                observations['appearance'] = {'baseline': sorted(baseline), 'changed': changed, 'during': sorted(during),
                                              'returned': returned and after == baseline, 'after': sorted(after)}
                route_row = observations['route']
                if not (changed and observations['appearance']['returned']):
                    raise CaseFailed('installed app appearance did not follow the desktop setting')
                if not route_row['brokered'] or not route_row['session_namespace']:
                    raise CaseUnavailable('protected-launch-not-active')
                status = 'passed'
    except CaseUnavailable as error:
        observations.update(getattr(error, 'observations', {}), reason=str(error))
        status = 'unverified'
    except CaseFailed as error:
        observations.update(getattr(error, 'observations', {}), reason=str(error))
        status = 'failed'
    finally:
        if restore is not None:
            try:
                restore()
            except (CaseUnavailable, OSError, subprocess.SubprocessError):
                observations['restore_failed'] = True
        app.stop()
        observations['desktop'] = app.observations
    return retain(binding, capability, case, root, status, observations, app.streams())
