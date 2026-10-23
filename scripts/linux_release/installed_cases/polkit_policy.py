"""Hold the guest administrator's fixed policy lease through private process pipes."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import pwd
import select
import subprocess
import time

from . import polkit_rule
from .products import require_owned_guest
from .records import CaseFailed, CaseUnavailable
from ..installed_input.protocol import binding as input_binding


class PresencePolicy:
    def __init__(self, binding):
        require_owned_guest(binding)
        native = binding.get('native_input')
        if not isinstance(native, dict):
            raise CaseUnavailable('presence-policy-requires-fixed-physical-input')
        self.execution = input_binding(native['binding'])
        self.process, self.pending = None, b''
        self.observations = {'installed': False, 'restored': False}

    def _read(self):
        deadline = time.monotonic() + 15
        while True:
            if b'\n' in self.pending:
                row, self.pending = self.pending.split(b'\n', 1)
                if len(row) > 1 << 20:
                    raise ValueError('presence policy receipt exceeds its fixed bound')
                return json.loads(row)
            if len(self.pending) > 1 << 20:
                raise ValueError('presence policy receipt exceeds its fixed bound')
            remaining = deadline - time.monotonic()
            if remaining <= 0 or not select.select([self.process.stdout], [], [], remaining)[0]:
                raise CaseUnavailable('presence-policy-custodian-receipt-unavailable')
            raw = os.read(self.process.stdout.fileno(), 8192)
            if not raw:
                raise CaseUnavailable('presence-policy-custodian-closed-an-incomplete-receipt')
            self.pending += raw

    def _installed(self, value):
        if (not isinstance(value, dict) or set(value) != {'phase', 'pid', 'rule'}
                or value['phase'] != 'installed' or type(value['pid']) is not int or value['pid'] <= 1):
            raise ValueError('presence policy custodian omitted its installation receipt')
        selected = value['rule']
        expected = '00-paintedwolf-qualification-' + self.execution['execution_nonce'] + '.rules'
        if (not isinstance(selected, dict) or set(selected) != {'name', 'action_id', 'object', 'before'}
                or selected['name'] != expected or selected['action_id'] != polkit_rule.ACTION
                or not isinstance(selected['before'], dict) or len(selected['before']) > 256):
            raise ValueError('presence policy receipt differs from the fixed selected rule')
        # Construct the same public fixture bytes without opening the privileged rule directory.
        username = pwd.getpwuid(os.getuid()).pw_name
        body = polkit_rule.rule_bytes(username)
        facts = selected['object']
        integers = {'device', 'inode', 'uid', 'gid', 'mode', 'links', 'bytes', 'mtime_ns', 'ctime_ns'}
        if (not isinstance(facts, dict) or set(facts) != integers | {'sha256'}
                or any(type(facts[key]) is not int or facts[key] < 0 for key in integers)
                or facts['inode'] <= 0 or facts['uid'] != 0 or facts['mode'] != 0o100644
                or facts.get('links') != 1 or facts.get('bytes') != len(body)
                or facts.get('sha256') != hashlib.sha256(body).hexdigest()):
            raise ValueError('presence policy rule is not the exact root-controlled fixed action and user')
        return selected

    def __enter__(self):
        helper = Path(polkit_rule.__file__).resolve(strict=True)
        self.process = subprocess.Popen(['sudo', '-n', '--', '/usr/bin/python3', '-I', str(helper),
            self.execution['execution_nonce']], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.PIPE, start_new_session=True)
        try:
            selected = self._installed(self._read())
            self.observations.update({'installed': True, 'rule': selected})
            return self
        except BaseException as error:
            self.__exit__(type(error), error, error.__traceback__)
            raise

    def __exit__(self, kind, error, traceback):
        restoration = None
        try:
            try:
                self.process.stdin.write(b'release\n')
                self.process.stdin.flush()
            except BrokenPipeError:
                pass
            finally:
                self.process.stdin.close()
            restoration = self._read()
            code = self.process.wait(timeout=15)
            expected = self.observations.get('rule', {}).get('before')
            restored = restoration.get('rule') if isinstance(restoration, dict) else None
            if (code != 0 or not isinstance(restoration, dict)
                    or set(restoration) != {'phase', 'rule', 'shared_policy_unchanged'}
                    or restoration['phase'] != 'restored' or restoration['shared_policy_unchanged'] is not True
                    or not isinstance(restored, dict) or set(restored) != {'created', 'removed', 'restored', 'after'}
                    or any(restored[key] is not True for key in ('created', 'removed', 'restored'))
                    or restored['after'] != expected):
                raise ValueError('presence policy did not establish exact restoration')
            self.observations['restored'] = True
        except BaseException as cleanup:
            self.observations.update({'recovery_required': True, 'cleanup_type': type(cleanup).__name__,
                                      'restoration': restoration, 'custodian_pid': self.process.pid})
            if self.process.poll() is None:
                # Only the sudo process started here is signaled; it forwards termination
                # to the custodian, whose handler removes its exact owned rule.
                self.process.terminate()
                try:
                    self.process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    pass
            failure = CaseFailed('owned-presence-policy-restoration-unverified')
            failure.observations = {'presence_policy': self.observations}
            raise failure from None
        finally:
            self.process.stdout.close()
            self.process.stderr.close()
            if error is not None:
                error.observations = {**getattr(error, 'observations', {}),
                                      'presence_policy': self.observations}
