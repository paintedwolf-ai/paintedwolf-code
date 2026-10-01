import pathlib
import sqlite3
import tempfile
import unittest
from unittest.mock import Mock, patch
from smoke import Application


class PreflightWaitTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.application = object.__new__(Application)
        self.application.directory = pathlib.Path(self.temporary.name)
        self.application.process = Mock()
        self.application.process.poll.return_value = None
        self.sleep = patch('smoke.time.sleep')
        self.sleep.start()
        self.addCleanup(self.sleep.stop)

    def test_slow_preparation_waits_for_observed_completion(self):
        observed = Mock(side_effect=[None]*1500 + [{'receipt': 'complete'}])
        self.assertEqual(self.application.wait_for(observed, 'preparation'), {'receipt': 'complete'})
        self.assertEqual(observed.call_count, 1501)

    def test_application_death_ends_wait(self):
        self.application.process.poll.return_value = 1
        with self.assertRaisesRegex(RuntimeError, 'application exited'):
            self.application.wait_for(lambda: None, 'preparation')

    def test_typed_submission_failure_ends_wait(self):
        with sqlite3.connect(self.application.directory/'store.db') as db:
            db.execute('CREATE TABLE prompt_submissions(session_id TEXT,status TEXT,error_code TEXT)')
            db.execute("INSERT INTO prompt_submissions VALUES('root','failed','fixture_error')")
        with self.assertRaisesRegex(RuntimeError, 'submission failed'):
            self.application.wait_for(lambda: None, 'preparation', 'root')

    def test_operator_interrupt_is_not_swallowed(self):
        with self.assertRaises(KeyboardInterrupt):
            self.application.wait_for(Mock(side_effect=KeyboardInterrupt), 'preparation')
