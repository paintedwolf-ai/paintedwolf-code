"""Causal state of one observed system-agent transaction; no input or host release authority.

Only the privileged fixed bus observer supplies envelopes. Cookies stay private in this
object and never enter its public witness or exception messages.
"""
from __future__ import annotations

from copy import deepcopy

AUTHORITY = 'org.freedesktop.PolicyKit1.Authority'
AGENT = 'org.freedesktop.PolicyKit1.AuthenticationAgent'
from .polkit_wire import ACTION, validate
CANCELLED = 'org.freedesktop.PolicyKit1.Error.Cancelled'


class Transaction:
    def __init__(self, attempt, app, shell, peer):
        # Native attempt parsing and kernel/session acquisition belong to their
        # existing owners; this state machine cannot promote supplied JSON to proof.
        validate(attempt)
        if attempt['phase'] != 'interactive' or attempt['transport'] != 'pending':
            raise ValueError('Polkit transaction requires its fresh interactive call boundary')
        if (attempt['subject'] != {key: app[key] for key in ('pid', 'start_time_ticks', 'uid')}
                or shell['uid'] != app['uid']):
            raise ValueError('Polkit transaction subject differs from its independently selected account')
        self.attempt, self.app, self.shell, self.peer = deepcopy(attempt), deepcopy(app), deepcopy(shell), peer
        self.check = self.begin = self.response = self.agent = None
        self.other_checks = set()
        self.cookie = None
        self.response_accepted = self.agent_returned = self.finished = False
        self.cancelled = self.failed = False
        self.cancel_requested = False
        self.count = 0

    def _reject(self):
        self.failed = True
        self.cookie = None
        raise ValueError('Polkit transaction lost its unique bound authority or causal sequence')

    def _same_subject(self, subject):
        expected = self.attempt['subject']
        return (isinstance(subject, (list, tuple)) and len(subject) == 2
                and subject[0] == 'unix-process' and subject[1] == {
                    'pid': expected['pid'], 'start-time': expected['start_time_ticks'], 'uid': expected['uid']})

    def _same_account(self, identity):
        return (isinstance(identity, (list, tuple)) and len(identity) == 2
                and identity[0] == 'unix-user' and identity[1] == {'uid': self.app['uid']})

    def observe(self, row):
        if self.failed or self.finished:
            self._reject()
        self.count += 1
        if self.count > 128:
            self._reject()
        authority = self.attempt['authority']
        if row['kind'] == 'owner-changed':
            if row['name'] in {authority, 'org.freedesktop.PolicyKit1',
                               self.attempt['sender'], self.agent['unique'] if self.agent else ''}:
                self._reject()
            return
        if row['kind'] == 'call':
            interface, member, body = row['interface'], row['member'], row['body']
            if interface == AUTHORITY and member == 'CheckAuthorization':
                if len(body) != 5 or body[1] != ACTION:
                    return
                key = (row['sender'], row['serial'])
                selected = (row['sender'] == self.attempt['sender'] and body[3] == 1
                            and body[4] == self.attempt['request_id'])
                if not selected:
                    if self.check is not None or key in self.other_checks:
                        self._reject()
                    self.other_checks.add(key)
                    return
                if (self.check is not None or self.other_checks or row['destination'] != authority
                        or not self._same_subject(body[0]) or body[2] != {}
                        or type(body[3]) is not int or body[3] != 1
                        or self.peer(row['sender']) != self.app):
                    self._reject()
                self.check = {'sender': row['sender'], 'serial': row['serial']}
            elif interface == AGENT and member == 'BeginAuthentication':
                if len(body) != 6 or body[0] != ACTION:
                    return
                if (self.check is None or self.begin is not None or row['sender'] != authority
                        or not isinstance(body[3], dict)
                        or body[3].get('polkit.caller-pid') != str(self.app['pid'])
                        or body[3].get('polkit.subject-pid') != str(self.app['pid'])
                        or not isinstance(body[4], str) or not 0 < len(body[4]) <= 4096
                        or not isinstance(body[5], (list, tuple)) or len(body[5]) != 1
                        or not self._same_account(body[5][0])):
                    self._reject()
                owner = self.peer(row['destination'])
                if owner != self.shell:
                    self._reject()
                self.agent = {'unique': row['destination'], 'process': owner, 'path': row['path']}
                self.begin = {'sender': row['sender'], 'serial': row['serial']}
                self.cookie = body[4]
            elif interface == AUTHORITY and member == 'AuthenticationAgentResponse2':
                if self.cookie is None or len(body) != 3 or body[1] != self.cookie:
                    return
                if (self.response is not None or self.agent_returned or row['destination'] != authority
                        or type(body[0]) is not int or body[0] != self.shell['uid']
                        or not self._same_account(body[2])):
                    self._reject()
                self.response = {'sender': row['sender'], 'serial': row['serial']}
            elif interface == AGENT and member == 'CancelAuthentication':
                if len(body) == 1 and body[0] == self.cookie:
                    if row['sender'] != authority or row['destination'] != self.agent['unique']:
                        self._reject()
                    self.cancel_requested = True
            return
        if row['kind'] not in {'return', 'error'}:
            self._reject()
        key = {'sender': row['destination'], 'serial': row['reply_serial']}
        other = (key['sender'], key['serial'])
        if other in self.other_checks and row['sender'] == authority:
            self.other_checks.remove(other)
            return
        if self.response is not None and key == self.response:
            if row['sender'] != authority or row['kind'] != 'return' or row['body'] not in ([], ()):
                self._reject()
            self.response_accepted = True
        elif self.begin is not None and key == self.begin:
            if row['sender'] != self.agent['unique'] or self.agent_returned:
                self._reject()
            if row['kind'] == 'error' and row['error_name'] == CANCELLED and not self.response_accepted:
                self.cancelled = True
            elif row['kind'] != 'return' or row['body'] not in ([], ()) or not self.response_accepted:
                self._reject()
            self.agent_returned = True
        elif self.check is not None and key == self.check:
            if row['sender'] != authority or row['kind'] != 'return' or not self.agent_returned:
                self._reject()
            body = row['body']
            if (not isinstance(body, (list, tuple)) or len(body) != 1
                    or not isinstance(body[0], (list, tuple)) or len(body[0]) != 3
                    or type(body[0][0]) is not bool or type(body[0][1]) is not bool
                    or not isinstance(body[0][2], dict)
                    or any(not isinstance(key, str) or not isinstance(value, str)
                           for key, value in body[0][2].items())
                    or body[0][0] is not (not self.cancelled) or body[0][1] is not False):
                self._reject()
            self.finished, self.cookie = True, None

    def pending(self):
        if (self.failed or self.finished or self.cancel_requested or self.begin is None or self.agent_returned
                or self.response is not None or self.peer(self.attempt['sender']) != self.app
                or self.peer(self.agent['unique']) != self.shell):
            self._reject()
        return {'attempt': deepcopy(self.attempt), 'agent': deepcopy(self.agent),
                'check_serial': self.check['serial'], 'begin_serial': self.begin['serial']}

    def witness(self):
        if self.failed or not self.finished:
            self._reject()
        return {'attempt': deepcopy(self.attempt), 'agent': deepcopy(self.agent),
                'check_serial': self.check['serial'], 'begin_serial': self.begin['serial'],
                'system_authenticated': self.response_accepted and not self.cancelled,
                'agent_cancelled': self.cancelled, 'host_release_verified': False}

    def close(self):
        self.cookie = None
        self.failed = True
