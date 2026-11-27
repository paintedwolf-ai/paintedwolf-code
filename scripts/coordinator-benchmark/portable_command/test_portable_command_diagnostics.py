"""Unit tests for M13 dev command diagnostics verification and sanitized replay."""
import json
import pathlib
import sys
import tempfile
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
from portable_command.diagnostics import evaluate_sanitized_observation, load_jsonl, validate_redaction


def _clean_observation():
    return {
        'session_id': '11111111',
        'program_id': 'prog-m13-clean',
        'program_state': 'completed',
        'program_status_code': 0,
        'program_stop_code': '',
        'program_settled': True,
        'activity_revision': 3,
        'consumer_caught_up': True,
        'presentation': {
            'program_state': 'completed',
            'program_status_code': 0,
            'program_stop_code': '',
            'activity_revision': 3,
            'activities': [{'activity_id': 'act-1', 'state': 'completed', 'execution_ids': ['exec-1']}],
        },
        'executions': [
            {'execution_id': 'exec-1', 'operation_id': 'write', 'state': 'completed', 'admitted': True},
        ],
        'linked_operations': [
            {
                'execution_id': 'exec-1', 'operation_id': 'write', 'source_id': 'root-1:a.txt',
                'expected_operation_id': 'write', 'expected_source_id': 'root-1:a.txt',
                'expected_revision': 'rev-1', 'observed_revision': 'rev-1',
                'completed': True, 'complete_evidence': True,
            },
        ],
    }


class PortableCommandDiagnosticsTests(unittest.TestCase):
    def test_clean_observation_is_quiet(self):
        self.assertEqual(evaluate_sanitized_observation(_clean_observation()), [])

    def test_seeded_contradictions_and_gated_stop_rule(self):
        # 1. duplicate/conflicting terminal
        obs = _clean_observation()
        obs['terminal_records'] = [
            {'execution_id': 'exec-1', 'state': 'completed'},
            {'execution_id': 'exec-1', 'state': 'failed'},
        ]
        got = evaluate_sanitized_observation(obs)
        self.assertEqual([(f['rule_id'], f['disposition']) for f in got], [('dup_or_conflicting_terminal', 'contradiction')])

        # 2. missing child terminal
        obs = _clean_observation()
        obs['executions'].append({'execution_id': 'exec-2', 'operation_id': 'grep', 'state': 'running', 'admitted': True})
        got = evaluate_sanitized_observation(obs)
        self.assertEqual([(f['rule_id'], f['disposition']) for f in got], [('missing_child_terminal', 'contradiction')])

        # 3. stale terminal presentation
        obs = _clean_observation()
        obs['presentation']['program_state'] = 'running'
        got = evaluate_sanitized_observation(obs)
        self.assertEqual([(f['rule_id'], f['disposition']) for f in got], [('stale_terminal_presentation', 'contradiction')])

        # 4. operation/source contradiction
        obs = _clean_observation()
        obs['linked_operations'][0]['observed_revision'] = 'rev-other'
        got = evaluate_sanitized_observation(obs)
        self.assertEqual([(f['rule_id'], f['disposition']) for f in got], [('operation_source_contradiction', 'contradiction')])

        # 5. gated stop admission reports unsupported without qualified ack + comparable witness
        obs = _clean_observation()
        obs['stop_ack'] = {'qualified': False, 'ack_ordinal': 5, 'admission_witness_comparable': False}
        got = evaluate_sanitized_observation(obs)
        self.assertEqual([(f['rule_id'], f['disposition']) for f in got], [('stop_admission_after_ack', 'unsupported')])

    def test_lifecycle_lag_truncation_and_redaction_validation(self):
        obs = _clean_observation()
        obs['program_settled'] = False
        obs['program_state'] = 'running'
        got = evaluate_sanitized_observation(obs)
        self.assertEqual([(f['rule_id'], f['disposition']) for f in got], [('missing_child_terminal', 'incomplete')])
        self.assertEqual(validate_redaction(got), [])

        with tempfile.TemporaryDirectory() as td:
            log = pathlib.Path(td) / 'command-diagnostics.jsonl'
            log.write_text(json.dumps(got[0]) + '\n', encoding='utf-8')
            loaded = load_jsonl(log)
            self.assertEqual(len(loaded), 1)
            self.assertEqual(validate_redaction(loaded, forbidden_substrings=['sk-secret-canary']), [])


if __name__ == '__main__':
    unittest.main()
