"""Controls for binding a failed producer to its screened retained output."""
import unittest

from portable_command.reporting import retained_failure_stdout


class FailedOutputTests(unittest.TestCase):
    def test_exact_owner_and_complete_content_are_required(self):
        producer = {
            'id': 'producer', 'program_id': 'program', 'native_command': 'external',
            'owner': 'processes', 'state': 'completed', 'status_code': 1, 'complete': 1,
            'publication_eligible': 0, 'owner_invoked': 1, 'settled_at': 'timestamp',
        }
        retained = {
            'program_id': 'program', 'execution_id': 'producer', 'stream': 'stdout',
            'reference': 'output:producer:stdout', 'blob_id': 'blob',
            'complete': 1, 'redacted': 0, 'preview_truncated': 0, 'preview': 'alpha',
        }
        view = {
            'ref': retained['reference'], 'stream': 'stdout', 'complete': True,
            'redacted': False, 'preview_truncated': False, 'size_bytes': 5, 'preview': 'alpha',
        }
        for label, execution, owner_row, output_view, expected in [
            ('exact', producer, retained, view, True),
            ('eligible producer', {**producer, 'publication_eligible': 1}, retained, view, False),
            ('unrelated owner row', producer, {**retained, 'execution_id': 'foreign'}, view, False),
            ('unrelated output reference', producer, retained, {**view, 'ref': 'foreign'}, False),
            ('truncated preview', producer, retained, {**view, 'preview_truncated': True}, False),
            ('different content', producer, retained, {**view, 'preview': 'other'}, False),
            ('partial stream', producer, retained, {**view, 'complete': False}, False),
        ]:
            with self.subTest(label=label):
                receipt = {'executions': [{
                    'id': 'producer', 'program_id': 'program', 'state': 'completed', 'status_code': 1,
                    'complete': True, 'publication_eligible': False, 'outputs': [output_view],
                }]}
                proof = retained_failure_stdout('program', [execution], [owner_row], receipt, b'alpha')
                self.assertIs(bool(proof), expected)
