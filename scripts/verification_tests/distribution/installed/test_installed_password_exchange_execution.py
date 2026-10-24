"""Private host exchange rejects changed focus and ambiguous completion without retry."""
import copy
import json
import time
import tempfile
from pathlib import Path
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_input import password_actor, password_exchange, password_wire
from verification_tests.distribution.installed import test_installed_password_actor_execution as actor_tests


class PasswordExchangeTests(unittest.TestCase):
    setUp = actor_tests.PrivateActorTests.setUp

    def pipe(self):
        pipe = Mock()
        pipe.deadline = time.monotonic() + 10
        pipe.remaining.return_value = 10
        pipe.receive_frame.return_value = self.secret.encode()
        responses = [password_exchange.observed(ordinal, self.pending)
                     for ordinal in range(41) for _ in range(2)]
        responses.append({'operation': 'accepted', 'transaction_id': self.pending['transaction_id']})
        pipe.receive_metadata.side_effect = responses
        return pipe, responses

    def test_complete_exchange_retains_only_aggregate_facts_and_same_deadline(self):
        pipe, _ = self.pipe()
        original = self.actor._send_keys
        deadlines = []
        def deliver(keys):
            deadlines.append(self.actor.deadline)
            return original(keys)
        self.actor._send_keys = deliver
        result = password_exchange.exchange(self.actor, self.pending, pipe)
        self.assertTrue(result['guest_confirmed'])
        self.assertEqual(result['delivery']['acknowledged_count'], 41)
        self.assertEqual(deadlines, [pipe.deadline] * 41)
        self.assertNotIn(self.secret, json.dumps(result))
        self.assertNotIn('event_sha256', json.dumps(result))
        self.assertEqual(pipe.send_metadata.call_count, 83)
        self.assertIsNone(self.actor.deadline)

    def test_invalid_raw_value_is_refused_by_app_owner_without_echo_or_input(self):
        for raw in (b'!' * 40, b'\xff' * 40, b'a' * 39):
            with self.subTest(size=len(raw)):
                self.actor.failed = False
                self.actor.command.reset_mock()
                pipe, _ = self.pipe()
                pipe.receive_frame.return_value = raw
                with self.assertRaises(ValueError) as failure:
                    password_exchange.exchange(self.actor, self.pending, pipe)
                self.assertEqual(str(failure.exception), 'private actor value has an invalid fixed shape')
                self.assertTrue(self.actor.failed)
                self.actor.command.assert_not_called()
                self.assertEqual(failure.exception.private_fixture['failure_type'], 'ValueError')

    def test_changed_target_prevents_input_and_poisoned_actor_cannot_resume(self):
        pipe, responses = self.pipe()
        responses[0] = copy.deepcopy(responses[0])
        responses[0]['target']['label'] = 'Confirm app password'
        with self.assertRaises(ValueError) as failure:
            password_exchange.exchange(self.actor, self.pending, pipe)
        self.actor.command.assert_not_called()
        self.assertTrue(self.actor.failed)
        facts = failure.exception.private_fixture
        self.assertFalse(facts['guest_confirmed'])
        self.assertEqual(facts['delivery']['acknowledged_count'], 0)
        self.assertNotIn(self.secret, json.dumps(facts))
        with self.assertRaises(ValueError):
            with password_actor.Fixture(self.actor, self.pending, self.secret):
                pass

    def test_after_input_failure_retains_partial_count_without_replaying_key(self):
        pipe, responses = self.pipe()
        responses[1] = {'operation': 'focused', 'ordinal': 1, 'target': self.pending['target']}
        with self.assertRaises(ValueError) as failure:
            password_exchange.exchange(self.actor, self.pending, pipe)
        self.assertEqual(failure.exception.private_fixture['delivery']['acknowledged_count'], 1)
        self.assertFalse(failure.exception.private_fixture['guest_confirmed'])
        self.assertEqual(self.actor.command.call_count, 2)
        self.assertTrue(self.actor.failed)

    def test_wrong_completion_never_becomes_a_confirmed_delivery(self):
        pipe, responses = self.pipe()
        responses[-1] = {'operation': 'accepted', 'transaction_id': 'f' * 32}
        with self.assertRaises(ValueError) as failure:
            password_exchange.exchange(self.actor, self.pending, pipe)
        self.assertFalse(failure.exception.private_fixture['guest_confirmed'])
        self.assertEqual(failure.exception.private_fixture['delivery']['acknowledged_count'], 41)
        self.assertTrue(self.actor.failed)

    def test_oversized_lifetime_refuses_before_requesting_private_bytes(self):
        pipe, _ = self.pipe()
        pipe.remaining.return_value = password_wire.MAX_SECONDS + 1
        with self.assertRaises(ValueError):
            password_exchange.exchange(self.actor, self.pending, pipe)
        pipe.receive_frame.assert_not_called()
        self.assertTrue(self.actor.failed)

    def test_invalid_metadata_is_not_retained_and_cannot_resume(self):
        pipe, _ = self.pipe()
        self.pending['forbidden'] = self.secret
        with self.assertRaises(ValueError) as failure:
            password_exchange.exchange(self.actor, self.pending, pipe)
        self.assertEqual(failure.exception.private_fixture,
                         {'guest_confirmed': False, 'failure_type': 'ValueError'})
        pipe.receive_frame.assert_not_called()
        self.assertTrue(self.actor.failed)

    def test_session_failure_retains_no_exception_arguments_or_retry_authority(self):
        with tempfile.TemporaryDirectory() as directory:
            session = password_exchange.Session(self.actor, self.pending['actor'], self.pending['binding'], Path(directory))
            pipe, _ = self.pipe()
            pipe.line.return_value = {'state': 'private-fixture-open'}
            with patch.object(password_exchange, 'exchange', side_effect=ValueError(self.secret)) as exchange:
                self.assertFalse(session.perform(self.pending, pipe))
                self.assertFalse(session.perform(self.pending, pipe))
                exchange.assert_called_once()
            session.close()
            facts = [json.loads(row) for row in (Path(directory) / 'native-password-input.jsonl').read_text().splitlines()]
            self.assertEqual(len(facts), 1)
            self.assertNotIn(self.secret, str(facts))
            self.assertEqual(facts[0]['failure_type'], 'ValueError')
            self.assertFalse(facts[0]['guest_confirmed'])
            self.assertTrue(self.actor.failed)
            self.assertFalse(any(isinstance(value, BaseException) for value in vars(session).values()))

    def test_closed_session_cannot_reopen_transport_or_deliver_again(self):
        with tempfile.TemporaryDirectory() as directory:
            session = password_exchange.Session(self.actor, self.pending['actor'], self.pending['binding'], Path(directory))
            session.close()
            pipe, _ = self.pipe()
            with patch.object(password_exchange, 'exchange') as exchange:
                self.assertFalse(session.perform(self.pending, pipe))
                exchange.assert_not_called()
            pipe.line.assert_not_called()
            self.assertFalse((Path(directory) / 'native-password-input.jsonl').exists())
