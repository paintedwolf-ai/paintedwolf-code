"""Native confirmation observations stay bound and never stand for a host release."""
import json
from types import SimpleNamespace
import unittest
from unittest.mock import Mock

from linux_release.installed_cases import presence_confirmation as confirmation

CHALLENGE = '00000000-0000-4000-8000-000000000001'
ROW = {'format_version': 1, 'challenge_id': CHALLENGE, 'purpose': 'reveal', 'window_label': 'main',
       'generation': 7, 'status': 'refused', 'authenticator': None, 'code': 'presence_unavailable', 'resolution': None}


def stream(row):
    return (confirmation.PREFIX + json.dumps(row) + '\n').encode()


class NativeConfirmationObservationTests(unittest.TestCase):
    def app(self, raw):
        return SimpleNamespace(observations={'launch_identity': {'pid': 1}, 'engine_identity': {'pid': 2}},
                               require_running=Mock(), require_engine=Mock(),
                               streams=Mock(return_value={'app-stderr': raw}))

    def test_complete_typed_record_and_partial_next_line_are_separate(self):
        app = self.app(stream(ROW) + confirmation.PREFIX.encode() + b'{"challenge_id":')
        self.assertEqual(confirmation.confirmation_rows(app), [ROW])
        self.assertEqual(confirmation.await_confirmation(app, CHALLENGE, 7, after_count=0), ROW)
        self.assertEqual(app.require_running.call_count, 4)

    def test_foreign_challenge_generation_and_duplicate_outcomes_cannot_satisfy_selected_operation(self):
        duplicate = self.app(stream(ROW) * 2)
        with self.assertRaisesRegex(ValueError, 'duplicate outcomes'):
            confirmation.await_confirmation(duplicate, CHALLENGE, 7, after_count=0)
        with self.assertRaisesRegex(ValueError, 'selected reveal'):
            confirmation.await_confirmation(self.app(stream(ROW)), CHALLENGE, 8, after_count=0)
        other = {**ROW, 'purpose': 'unlock'}
        with self.assertRaisesRegex(ValueError, 'selected reveal'):
            confirmation.await_confirmation(self.app(stream(other)), CHALLENGE, 7, after_count=0)

    def test_malformed_or_contradictory_outcomes_do_not_become_confirmation(self):
        changes = ({'format_version': True}, {'generation': True}, {'purpose': []},
                   {'authenticator': 'linux_user_presence'}, {'code': []},
                   {'status': 'confirmed', 'authenticator': 'linux_app_password'})
        for change in changes:
            with self.subTest(change=change), self.assertRaises(ValueError):
                confirmation.confirmation_rows(self.app(stream({**ROW, **change})))
        raw = stream(ROW).replace(b'"format_version": 1', b'"format_version": 1, "format_version": 1')
        with self.assertRaisesRegex(ValueError, 'repeats a field'):
            confirmation.confirmation_rows(self.app(raw))

    def test_observer_refuses_engine_replacement_and_does_not_publish_partial_rows(self):
        app = self.app(stream(ROW))
        def changed():
            app.observations['engine_identity'] = {'pid': 3}
            return {'app-stderr': stream(ROW)}
        app.streams.side_effect = changed
        with self.assertRaisesRegex(ValueError, 'runtime changed'):
            confirmation.confirmation_rows(app)

    def test_native_password_confirmation_has_no_host_completion_claim(self):
        row = {**ROW, 'status': 'confirmed', 'authenticator': 'linux_app_password', 'code': None}
        self.assertEqual(confirmation.confirmation_rows(self.app(stream(row))), [row])
        self.assertNotIn('host_completed', row)

    def test_new_reveal_boundary_excludes_old_outcomes_but_rejects_two_new_operations(self):
        row = {**ROW, 'challenge_id': '00000000-0000-4000-8000-000000000002'}
        self.assertEqual(confirmation.await_confirmation(self.app(stream(ROW) + stream(row)), after_count=1), row)
        with self.assertRaisesRegex(ValueError, 'duplicate outcomes'):
            confirmation.await_confirmation(self.app(stream(ROW) + stream(row)), after_count=0)
