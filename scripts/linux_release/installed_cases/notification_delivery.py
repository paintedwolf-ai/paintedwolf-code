"""Correlate one real notification delivery and activation on a pinned desktop bus."""
from __future__ import annotations

import copy
import hashlib
import re

from .records import CaseFailed, CaseUnavailable
from .. import notification_display

SERVICE = 'org.freedesktop.Notifications'
PATH = '/org/freedesktop/Notifications'
_UNIQUE = re.compile(r':[0-9]+\.[0-9]+')


def unique(value):
    if not isinstance(value, str) or not _UNIQUE.fullmatch(value):
        raise ValueError('notification observation requires a unique bus owner')
    return value


def uint(value):
    if type(value) is not int or not 0 < value < 1 << 32:
        raise ValueError('notification observation requires a positive uint32 identity')
    return value


class DeliveryTrace:
    """Only an owner-pinned Notify reply can establish an actionable notification ID."""
    def __init__(self, sender, owner, desktop_entry):
        self.sender, self.owner = unique(sender), unique(owner)
        if sender == owner or not isinstance(desktop_entry, str) or not desktop_entry:
            raise ValueError('notification sender and service must have distinct captured identities')
        self.desktop_entry = desktop_entry
        self.call, self.delivery, self.activation = None, None, None
        self.capability_call, self.capabilities = None, None
        self.armed, self.token, self.sequence = False, None, 0
        self.events = []

    def observe(self, row):
        if (not isinstance(row, dict) or set(row) != {'type', 'sender', 'destination', 'serial',
                'reply_serial', 'path', 'interface', 'member', 'signature', 'body'}):
            raise ValueError('notification bus observation has an unknown envelope')
        self.sequence += 1
        if self.sequence > 64:
            raise CaseUnavailable('notification-bus-observation-limit')
        if row['type'] == 'call':
            self._capabilities(row) if row['member'] == 'GetCapabilities' else self._notify(row)
        elif row['type'] == 'return':
            self._delivered(row)
        elif row['type'] == 'signal':
            self._signal(row)
        else:
            raise ValueError('notification observation requires a declared message type')

    def _capabilities(self, row):
        if (row['sender'] != self.sender or row['destination'] not in {self.owner, SERVICE}
                or row['path'] != PATH or row['interface'] != SERVICE):
            raise CaseFailed('native capability request changed its captured bus authority')
        if self.capability_call is not None or self.call is not None:
            raise CaseUnavailable('native-notification-capability-exchange-is-ambiguous')
        if row['signature'] != '()' or row['body'] not in ((), []):
            raise ValueError('native capability request has an invalid typed body')
        self.capability_call = {'request_serial': uint(row['serial']), 'request_sequence': self.sequence}

    def _notify(self, row):
        if (row['sender'] != self.sender or row['destination'] not in {self.owner, SERVICE}
                or row['path'] != PATH or row['interface'] != SERVICE or row['member'] != 'Notify'):
            raise CaseFailed('notification request changed its captured bus authority')
        if self.call is not None:
            raise CaseUnavailable('notification-request-is-not-unique')
        body = row['body']
        if row['signature'] != '(susssasa{sv}i)' or not isinstance(body, (tuple, list)) or len(body) != 8:
            raise ValueError('notification request has an invalid typed body')
        app, replacement, icon, title, content, actions, hints, timeout = body
        if (app != 'Painted Wolf Code' or type(replacement) is not int or replacement != 0
                or not isinstance(icon, str) or not isinstance(title, str) or len(title.encode()) > 4096
                or not isinstance(content, str) or len(content.encode()) > 32 << 10
                or not isinstance(actions, (list, tuple)) or list(actions) != ['default', 'Open chat'] or not isinstance(hints, dict)
                or hints.get('desktop-entry') != self.desktop_entry or type(timeout) is not int):
            raise CaseUnavailable('notification-request-is-not-the-declared-actionable-delivery')
        self.call = {'serial': uint(row['serial']), 'sender': self.sender, 'owner': self.owner,
            'desktop_entry': self.desktop_entry, 'wire_title_sha256': hashlib.sha256(title.encode()).hexdigest(),
            'wire_body_sha256': hashlib.sha256(content.encode()).hexdigest(), 'sequence': self.sequence}
        if self.capabilities is None:
            raise CaseUnavailable('native-notification-capabilities-were-not-captured-before-Notify')
        if self.capabilities['request_serial'] == self.call['serial']:
            raise CaseFailed('native notification reused its capability request serial')
        self.call.update(notification_display.digests(title, content, self.capabilities))
        self.events.append({'kind': 'notify', **self.call})

    def _delivered(self, row):
        if self.capability_call is not None and row['reply_serial'] == self.capability_call['request_serial']:
            if row['sender'] != self.owner or row['destination'] != self.sender:
                raise CaseFailed('native capability reply changed its captured bus authority')
            if self.capabilities is not None or self.call is not None:
                raise CaseUnavailable('native-notification-capability-reply-is-ambiguous-or-late')
            if row['signature'] != '(as)' or not isinstance(row['body'], (list, tuple)) or len(row['body']) != 1:
                raise ValueError('native capability reply has an invalid typed body')
            self.capabilities = {**self.capability_call, 'reply_sequence': self.sequence,
                **notification_display.capabilities(row['body'][0])}
            notification_display.capability_proof(self.capabilities)
            return
        if self.call is None or row['reply_serial'] != self.call['serial']:
            return
        if row['sender'] != self.owner or row['destination'] != self.sender:
            raise CaseFailed('notification reply changed its captured bus authority')
        if self.delivery is not None:
            raise CaseFailed('notification delivery reply was duplicated')
        if row['signature'] != '(u)' or not isinstance(row['body'], (tuple, list)) or len(row['body']) != 1:
            raise ValueError('notification service returned an invalid typed identity')
        self.delivery = {'notification_id': uint(row['body'][0]), 'sequence': self.sequence}
        self.events.append({'kind': 'delivered', **self.delivery})

    def arm(self):
        if self.delivery is None or self.armed or self.activation is not None:
            raise CaseUnavailable('notification-physical-activation-has-no-unique-delivery')
        self.armed = True
        return self.snapshot()

    def _signal(self, row):
        if row['sender'] != self.owner or row['path'] != PATH or row['interface'] != SERVICE:
            raise CaseFailed('notification signal changed its captured service owner')
        if row['member'] not in {'ActivationToken', 'ActionInvoked', 'NotificationClosed'}:
            return
        body = row['body']
        if not isinstance(body, (tuple, list)) or len(body) != 2:
            raise ValueError('notification signal has an invalid typed body')
        uint(body[0])
        if self.delivery is None or body[0] != self.delivery['notification_id']:
            return
        if row['member'] == 'NotificationClosed':
            if row['signature'] != '(uu)':
                raise ValueError('notification close signal has an invalid signature')
            if self.activation is None:
                raise CaseUnavailable('delivered-notification-closed-before-physical-activation')
            return
        if row['signature'] != '(us)' or not self.armed:
            raise CaseFailed('notification activation was not bound to its physical input transaction')
        if row['member'] == 'ActivationToken':
            token = body[1]
            if (self.token is not None or not isinstance(token, str) or not token
                    or len(token.encode()) > 4096 or '\0' in token):
                raise CaseFailed('notification activation token is missing or ambiguous')
            self.token = {'sha256': hashlib.sha256(token.encode()).hexdigest(), 'sequence': self.sequence}
            self.events.append({'kind': 'token', **self.token})
        else:
            if body[1] != 'default' or self.token is None or self.activation is not None:
                raise CaseFailed('notification default action has no unique preceding token')
            self.activation = {'notification_id': self.delivery['notification_id'],
                'action': 'default', 'sequence': self.sequence, 'token': dict(self.token)}
            self.events.append({'kind': 'activated', **self.activation})

    def snapshot(self):
        return copy.deepcopy({'sender': self.sender, 'owner': self.owner, 'request': self.call,
            'capabilities': self.capabilities, 'delivery': self.delivery, 'activation': self.activation, 'events': self.events})
