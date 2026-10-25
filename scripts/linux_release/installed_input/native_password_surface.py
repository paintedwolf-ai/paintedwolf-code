"""Native GNOME surface identity, focus and password ancestry; no operation authority."""
from __future__ import annotations

from . import accessibility

FIELDS = {'window', 'password', 'ancestors', 'window_state', 'password_state'}


def validate(value, shell):
    if not isinstance(value, dict) or set(value) != FIELDS:
        raise ValueError('native password surface has an unknown shape')
    accessibility.validate_identity(value['window'])
    if value['window']['pid'] != shell['pid']:
        raise ValueError('native unlock window is not the captured GNOME shell')
    state = value['window_state']
    if (not isinstance(state, dict) or set(state) != {'active', 'focused', 'visible', 'enabled', 'sensitive', 'defunct'}
            or any(type(item) is not bool for item in state.values()) or not state['active']
            or not all(state[key] for key in ('visible', 'enabled', 'sensitive')) or state['defunct']):
        raise ValueError('native unlock window has no positive live active surface proof')
    password, chain = value['password'], value['ancestors']
    if password is None:
        if chain != [] or value['password_state'] is not None or not state['focused']:
            raise ValueError('native clock proof cannot claim a password ancestor chain')
    else:
        password_state = value['password_state']
        if (not isinstance(password_state, dict) or set(password_state) != {'focused', 'visible', 'enabled', 'sensitive', 'defunct'}
                or any(type(item) is not bool for item in password_state.values()) or password_state['defunct']
                or not all(password_state[key] for key in ('focused', 'visible', 'enabled', 'sensitive'))):
            raise ValueError('native password has no positive live focus proof')
        accessibility.validate_identity(password)
        accessibility.validate(chain, shell['pid'], shell['pid'])
        if (chain[0] != password or chain[-1] != value['window']
                or any(row['pid'] != shell['pid'] or row['bus_name'] != value['window']['bus_name'] for row in chain)):
            raise ValueError('native password does not belong to the captured shell window')
    return value
