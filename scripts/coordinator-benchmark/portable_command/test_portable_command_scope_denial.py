"""Controls for late write-scope denial qualification."""
import pathlib
import tempfile
import unittest

import yaml

from portable_command.contracts import ROOT
from portable_command.harness import resolve_case
from portable_command.provenance import materialize_denied_progress_scope
from portable_command.reporting import extract_recovery


class ScopeDenialTests(unittest.TestCase):
    def test_refused_owner_proves_no_effect_without_allowing_replay(self):
        payload = {
            'program_id': 'program', 'executed_children': 1,
            'completed_effects': 0, 'uncertain_effects': 0,
            'refusal': {'code': 'WRITE_SCOPE_DENIED', 'details': {
                'program_id': 'program', 'execution_id': 'touch',
                'command_execution': 'observed', 'program_replay_available': True,
            }},
        }
        row = {
            'id': 'touch', 'program_id': 'program', 'state': 'refused',
            'stop_code': 'WRITE_SCOPE_DENIED', 'settled_at': 'timestamp',
            'effect_certainty': 'none', 'owner_invoked': 1,
        }
        for name, children, expected in [
            ('exact refusal', [row], True),
            ('missing row', [], False),
            ('duplicate row', [row, row], False),
            ('missing owner', [{**row, 'owner_invoked': 0}], False),
            ('wrong child', [{**row, 'id': 'other'}], False),
            ('wrong program', [{**row, 'program_id': 'other'}], False),
            ('committed effect', [{**row, 'effect_certainty': 'completed'}], False),
            ('uncertain effect', [{**row, 'effect_certainty': 'unknown'}], False),
            ('unsettled', [{**row, 'settled_at': ''}], False),
        ]:
            with self.subTest(name=name):
                result = extract_recovery('progress-then-denial', payload, {},
                                          {'invocation_program_executions': children}, [])
                self.assertIs(result['zero_program_effects'], expected)
                self.assertIs(result['whole_program_resubmit_allowed'], False)

    def test_scope_binding_only_narrows_disposable_configuration(self):
        case = resolve_case('progress-then-denial')
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp) / 'config-root'
            binding = materialize_denied_progress_scope(case, root)
            path = pathlib.Path('config/packs/painted-wolf/platform/host/path-scopes.yaml')
            stock = yaml.safe_load((ROOT / 'lycaon' / path).read_text())['scopes'][binding['scope_id']]
            scope = yaml.safe_load((root / path).read_text())['scopes'][binding['scope_id']]
            self.assertEqual(binding['denied_path'], 'denied.txt')
            self.assertEqual(scope, {**stock, 'deny': [*stock.get('deny', []), 'denied.txt']})
            self.assertIsNone(materialize_denied_progress_scope({**case, 'id': 'another-case'}, root))
