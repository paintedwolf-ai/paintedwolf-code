"""Select the authenticated installed bootstrap for protected desktop cases."""
from pathlib import Path

from .records import CaseUnavailable
from ..campaign.runtime import SETUID_LAUNCHER, elf


def protected_launcher(binding):
    # Select only the setuid image bound to these final package bytes. Its actual
    # execution performs the production policy and namespace admission.
    root = Path(binding['install_root'])
    entry = binding['images'].get(str(root / 'bin/pw-launch-entry'))
    expected = binding['images'].get(str(SETUID_LAUNCHER))
    if entry is None or expected is None:
        raise CaseUnavailable('protected-reply-requires-sealed-bootstrap-and-entry')
    if entry['mode'] != 0o755:
        raise ValueError('protected reply launch entry has the wrong mode')
    actual = elf(SETUID_LAUNCHER, binding['artifact']['architecture'], setuid=True)
    if actual != expected:
        raise ValueError('protected reply bootstrap differs from selected final bytes')
    return {'path': str(SETUID_LAUNCHER), **actual}
