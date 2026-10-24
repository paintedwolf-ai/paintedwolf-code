"""Release storage facts from the selected normal Linux desktop environment."""
from __future__ import annotations

from dataclasses import dataclass
import os
from pathlib import Path
import stat

# The configured release identifier in src-tauri/tauri.conf.json. Its drift
# contract also checks Linux config cannot override the storage directories.
APPLICATION_IDENTIFIER = 'dev.paintedwolf.code'
PROFILE_OVERRIDES = frozenset({'LYCAON_CONFIG_DIR', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME'})


@dataclass(frozen=True)
class Storage:
    uid: int
    home: Path
    application_identifier: str
    config: Path
    webview: Path

    def facts(self):
        return {'uid': self.uid, 'home': str(self.home), 'application_identifier': self.application_identifier,
                'config': str(self.config), 'webview': str(self.webview)}


def resolve(environment):
    """Ambient variables are irrelevant; this mapping is the actual selected env."""
    if not isinstance(environment, dict) or not isinstance(environment.get('HOME'), str):
        raise ValueError('installed storage requires its selected launch home')
    if PROFILE_OVERRIDES.intersection(environment):
        raise ValueError('selected installed profile overrides are not supported by this launch contract')
    home, uid = Path(environment['HOME']), os.getuid()
    if (uid <= 0 or not home.is_absolute() or home != Path.home()
            or home.resolve(strict=True) != home):
        raise ValueError('installed storage differs from the current owned account home')
    info = home.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != uid or info.st_mode & 0o022:
        raise ValueError('installed storage requires a physical account-owned home')
    # Release host_config_dir ignores LYCAON_CONFIG_DIR. Tauri's Linux default
    # webview root uses app_local_data_dir; selected launch env has no XDG override.
    return Storage(uid, home, APPLICATION_IDENTIFIER, home / '.config/paintedwolf',
                   home / '.local/share' / APPLICATION_IDENTIFIER)
