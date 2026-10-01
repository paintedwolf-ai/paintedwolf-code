import pathlib
import tempfile
import unittest
from unittest.mock import patch

import focused_outcomes
import smoke
import test_fixture_contracts
import test_focused_outcomes


class OutcomeControlTests(unittest.TestCase):
    def test_every_control_executes_in_the_pinned_runtime(self):
        seen = {}

        def run(runner, suite):
            seen['focused'] = test_focused_outcomes.local_execute
            seen['fixture'] = test_fixture_contracts.local_execute
            seen['cases'] = suite.countTestCases()
            seen['tempdir'] = tempfile.tempdir
            return unittest.TestResult()

        case = sorted(test_fixture_contracts.CONTRACTS)[0]
        with tempfile.TemporaryDirectory() as temporary, \
                patch.object(smoke, 'artifact_root', return_value=pathlib.Path(temporary)), \
                patch.object(test_focused_outcomes, 'local_execute'), \
                patch.object(test_fixture_contracts, 'local_execute'), \
                patch.object(unittest.TextTestRunner, 'run', run), \
                patch.object(focused_outcomes, 'execute', return_value=None) as execute:
            count = smoke.run_outcome_controls({case})
            self.assertEqual(seen['tempdir'], str(pathlib.Path(temporary) / 'coordinator-oracle-controls'))
        self.assertIs(seen['focused'], execute)
        self.assertIs(seen['fixture'], execute)
        self.assertEqual(count, seen['cases'])


if __name__ == '__main__':
    unittest.main()
