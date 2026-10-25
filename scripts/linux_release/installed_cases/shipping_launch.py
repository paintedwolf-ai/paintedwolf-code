"""Select genuine installed launch routes without synthesizing application scopes."""
import configparser
from pathlib import Path
import shlex

from ..installed_runtime import SETUID_LAUNCHER, file_identity


def launch_command(mode, launcher, storage, arguments, binding):
    if launcher != SETUID_LAUNCHER:
        raise ValueError('shipping launch requires the final protected bootstrap')
    if mode == 'unscoped-direct':
        return [str(launcher), *arguments], {'desktop_entry_launch_proven': False}
    if mode == 'pw-open-cold-start':
        if len(arguments) != 1 or not Path(arguments[0]).is_dir():
            raise ValueError('cold CLI launch requires one existing project directory')
        cli = Path(binding['install_root']) / 'bin/pw'
        actual = file_identity(cli)
        actual.pop('header')
        if actual != binding['images'].get(str(cli)):
            raise ValueError('cold CLI differs from the selected installed image')
        return [str(cli), 'open', *arguments], {'desktop_entry_launch_proven': False, 'cli_image': actual}
    if mode != 'desktop-entry':
        raise ValueError('unsupported shipping launch route')
    desktop = Path('/usr/share/applications') / (storage.application_identifier + '.desktop')
    actual = file_identity(desktop)
    actual.pop('header')
    info = desktop.lstat()
    if info.st_uid != 0 or actual['mode'] & 0o022:
        raise ValueError('installed desktop entry is not installer controlled')
    parser = configparser.ConfigParser(interpolation=None, strict=True)
    parser.optionxform = str
    parser.read_string(desktop.read_text(encoding='utf-8'))
    entry = parser['Desktop Entry']
    if (entry.get('Type') != 'Application' or entry.get('DBusActivatable', 'false') != 'false'
            or shlex.split(entry.get('Exec', '')) not in [[str(launcher), '%U'], [str(launcher), '%u'], [str(launcher)]]
            or entry.get('StartupWMClass') != storage.application_identifier):
        raise ValueError('installed desktop entry does not execute the bound protected launcher')
    return ['/usr/bin/gio', 'launch', str(desktop), *arguments], {
        'desktop_entry_launch_proven': False, 'desktop_entry_invoked': True, 'desktop_entry_path': str(desktop), 'desktop_entry_identity': actual}
