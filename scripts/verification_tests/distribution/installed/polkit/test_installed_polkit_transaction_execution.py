"""A real Polkit wire sequence must bind its caller, agent and one pending check."""
import copy
import json
import unittest

from scripts.linux_release.installed_input import polkit_transaction as model
from scripts.linux_release.installed_input import polkit_wire

APP = {'pid': 401, 'parent_pid': 1, 'start_time_ticks': 71, 'uid': 30059}
SHELL = {'pid': 201, 'parent_pid': 1, 'start_time_ticks': 31, 'uid': 30059}
ATTEMPT = {'format_version': 1, 'challenge': {'challenge_id': '11111111-1111-4111-8111-111111111111',
    'purpose': 'reveal', 'window_label': 'main', 'step_away_generation': 8},
    'action_id': model.ACTION, 'authority': ':1.2', 'sender': ':1.3',
    'request_id': '22222222-2222-4222-8222-222222222222', 'phase': 'interactive', 'transport': 'pending',
    'subject': {key: APP[key] for key in ('pid', 'start_time_ticks', 'uid')}, 'authorization': None}
COOKIE = 'private-not-an-evidence-field'


def call(sender, destination, serial, interface, member, body):
    return {'kind': 'call', 'sender': sender, 'destination': destination, 'serial': serial,
            'interface': interface, 'member': member, 'path': '/agent', 'body': body}


def check():
    return call(':1.3', ':1.2', 11, model.AUTHORITY, 'CheckAuthorization', [
        ['unix-process', {'pid': 401, 'start-time': 71, 'uid': 30059}], model.ACTION, {}, 1,
        ATTEMPT['request_id']])


def begin():
    return call(':1.2', ':1.4', 12, model.AGENT, 'BeginAuthentication', [model.ACTION,
        'Localized prose is not a discriminator', '', {'polkit.caller-pid': '401', 'polkit.subject-pid': '401'},
        COOKIE, [['unix-user', {'uid': 30059}]]])


def returned(sender, destination, serial, body=()):
    return {'kind': 'return', 'sender': sender, 'destination': destination, 'reply_serial': serial, 'body': body}


def transaction():
    peers = {':1.3': APP, ':1.4': SHELL}
    return model.Transaction(ATTEMPT, APP, SHELL, lambda unique: copy.deepcopy(peers[unique]))


class PolkitTransactionTests(unittest.TestCase):
    def pending(self):
        result = transaction()
        result.observe(check())
        result.observe(begin())
        return result

    def response(self, result, cookie=COOKIE):
        result.observe(call(':1.8', ':1.2', 13, model.AUTHORITY, 'AuthenticationAgentResponse2',
                            [30059, cookie, ['unix-user', {'uid': 30059}]]))
        result.observe(returned(':1.2', ':1.8', 13))

    def test_causal_success_never_contains_cookie_prose_or_host_release_claim(self):
        result = self.pending()
        pending = result.pending()
        self.assertEqual(pending['agent']['process'], SHELL)
        self.assertNotIn(COOKIE, json.dumps(pending))
        self.response(result)
        result.observe(returned(':1.4', ':1.2', 12))
        result.observe(returned(':1.2', ':1.3', 11, [[True, False, {}]]))
        witness = result.witness()
        self.assertTrue(witness['system_authenticated'])
        self.assertFalse(witness['host_release_verified'])
        self.assertNotIn(COOKIE, json.dumps(witness))
        self.assertNotIn('Localized', json.dumps(witness))
        self.assertIsNone(result.cookie)
        with self.assertRaises(ValueError):
            result.pending()

    def test_cancellation_requires_exact_agent_error_then_false_authority_result(self):
        result = self.pending()
        cancelled = {**returned(':1.4', ':1.2', 12), 'kind': 'error', 'error_name': model.CANCELLED}
        result.observe(cancelled)
        result.observe(returned(':1.2', ':1.3', 11, [[False, False, {}]]))
        self.assertTrue(result.witness()['agent_cancelled'])
        self.assertFalse(result.witness()['system_authenticated'])

    def test_agent_return_without_accepted_private_cookie_response_refuses(self):
        result = self.pending()
        result.observe(call(':1.8', ':1.2', 13, model.AUTHORITY, 'AuthenticationAgentResponse2',
                            [30059, 'another-private-cookie', ['unix-user', {'uid': 30059}]]))
        with self.assertRaises(ValueError) as failure:
            result.observe(returned(':1.4', ':1.2', 12))
        self.assertNotIn(COOKIE, str(failure.exception))

    def test_foreign_concurrent_same_action_refuses_even_if_subject_was_forged(self):
        result = self.pending()
        other = check()
        other.update(sender=':1.99', serial=99)
        with self.assertRaises(ValueError):
            result.observe(other)
        with self.assertRaises(ValueError):
            result.pending()

    def test_a_preexisting_foreign_check_cannot_be_hidden_by_a_later_matching_check(self):
        result = transaction()
        other = check()
        other.update(sender=':1.99', serial=99)
        result.observe(other)
        with self.assertRaises(ValueError):
            result.observe(check())

    def test_completed_noninteractive_probe_is_not_a_pending_interactive_transaction(self):
        result = transaction()
        probe = check()
        probe['serial'], probe['body'][3], probe['body'][4] = 10, 0, 'another-id'
        result.observe(probe)
        result.observe(returned(':1.2', ':1.3', 10, [[False, True, {}]]))
        result.observe(check())
        result.observe(begin())
        self.assertEqual(result.pending()['check_serial'], 11)

    def test_missing_or_wrong_authority_details_or_foreign_agent_refuse(self):
        for mutation in ('caller', 'subject', 'agent', 'account', 'authority'):
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                result = transaction()
                result.observe(check())
                row = begin()
                if mutation == 'caller':
                    row['body'][3].pop('polkit.caller-pid')
                elif mutation == 'subject':
                    row['body'][3]['polkit.subject-pid'] = '999'
                elif mutation == 'agent':
                    result.peer = lambda _: APP
                elif mutation == 'account':
                    row['body'][5] = [['unix-user', {'uid': 0}]]
                else:
                    row['sender'] = ':1.99'
                result.observe(row)

    def test_replaced_birth_or_owner_loss_invalidates_pending_input(self):
        result = self.pending()
        result.peer = lambda unique: {**(APP if unique == ':1.3' else SHELL), 'start_time_ticks': 999}
        with self.assertRaises(ValueError):
            result.pending()
        result = self.pending()
        with self.assertRaises(ValueError):
            result.observe({'kind': 'owner-changed', 'name': ':1.4'})

    def test_machine_fields_are_closed_and_step_away_epoch_is_not_engine_generation(self):
        self.assertEqual(polkit_wire.validate(copy.deepcopy(ATTEMPT)), ATTEMPT)
        for field, value in [('phase', []), ('transport', {}), ('request_id', 'bad'), ('cookie', COOKIE)]:
            row = copy.deepcopy(ATTEMPT)
            row[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                polkit_wire.validate(row)
        row = copy.deepcopy(ATTEMPT)
        row['challenge']['generation'] = row['challenge'].pop('step_away_generation')
        with self.assertRaises(ValueError):
            polkit_wire.validate(row)
