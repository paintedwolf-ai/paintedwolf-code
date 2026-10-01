import sqlite3
import unittest
from smoke_workers import completion_wake


class CompletionWakeTests(unittest.TestCase):
    def test_only_new_expected_host_notifications_allow_another_closeout(self):
        allowed = {('host_kick', 'process.finished'), ('host_loop_wake', 'loop.wake')}
        with sqlite3.connect(':memory:') as db:
            db.execute('CREATE TABLE messages(session_id,ord,role,origin,kind,host_signal_id)')
            def append(order, origin, kind, signal='', role='user'):
                db.execute('INSERT INTO messages VALUES(?,?,?,?,?,?)', ('session', order, role, origin, kind, signal))
            append(1, 'host', 'host_loop_wake', 'loop.wake')
            self.assertFalse(completion_wake(db, 'session', allowed))
            append(2, 'model', 'completion_report', role='assistant')
            self.assertFalse(completion_wake(db, 'session', allowed))
            append(3, 'host', 'host_kick', 'process.finished')
            append(4, 'host', 'host_loop_wake', 'loop.wake')
            self.assertTrue(completion_wake(db, 'session', allowed))
            self.assertFalse(completion_wake(db, 'session', set()))
            for origin, kind, signal in [('host', 'host_kick', 'verification.required'),
                                         ('model', 'host_loop_wake', 'loop.wake'),
                                         ('host', 'workflow_boundary', '')]:
                with self.subTest(origin=origin, kind=kind, signal=signal):
                    append(5, origin, kind, signal)
                    self.assertFalse(completion_wake(db, 'session', allowed))
                    db.execute('DELETE FROM messages WHERE ord=5')
            append(5, 'model', 'completion_report', role='assistant')
            self.assertFalse(completion_wake(db, 'session', allowed))
