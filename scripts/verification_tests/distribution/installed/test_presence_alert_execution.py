"""Presence prerequisite reads only a pinned alert's attributes, never its text/value."""
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import presence_alert as observer
from linux_release.installed_cases.records import CaseUnavailable


class AlertObservationTests(unittest.TestCase):
    def setUp(self):
        self.root = {'pid': 12, 'parent_pid': 1, 'start_time_ticks': 9}
        self.app = SimpleNamespace(require_running=Mock(), require_engine=Mock(),
            observations={'launch_identity': self.root, 'engine_identity': {'pid': 13}})
        class Alert:
            @property
            def name(self):
                raise AssertionError('alert text must not be read')
            def queryText(self):
                raise AssertionError('alert text must not be read')
            def queryValue(self):
                raise AssertionError('secret value must not be read')
        self.node = Alert()
        self.node.getRole = Mock(return_value=7)
        self.node.get_process_id = Mock(return_value=12)
        self.node.get_attributes = Mock(return_value=['xml-roles:alert', 'data-code:presence_unavailable'])
        self.frame = SimpleNamespace(get_process_id=lambda: 12)
        self.chain = [{'bus_name': ':1.42', 'path': '/alert', 'pid': 12}]
        self.ui = SimpleNamespace(api=SimpleNamespace(ROLE_ALERT=7), visible=Mock(return_value=True),
            windows=Mock(return_value=[(self.root, self.frame)]), descendants=Mock(return_value=[self.node]))
        self.patches = [patch.object(observer, 'process_identity', return_value=self.root),
            patch.object(observer.accessibility, 'capture', return_value=(self.frame, self.chain)),
            patch.object(observer.lineage, 'capture', return_value=[self.root])]
        for entry in self.patches:
            entry.start()
            self.addCleanup(entry.stop)

    def test_exact_owned_attributes_are_retained_without_inferring_an_outcome(self):
        result = observer.alerts(self.app, self.ui, 20)
        self.assertEqual(result[0]['attributes'], self.node.get_attributes.return_value)
        self.assertNotIn('code', result[0])
        self.assertNotIn('text', result[0])
        self.assertEqual(result[0]['accessible_chain'], self.chain)

    def test_nonalert_controls_are_not_read_and_attribute_payloads_are_bounded(self):
        self.node.getRole.return_value = 4
        self.assertEqual(observer.alerts(self.app, self.ui, 20), [])
        self.node.get_attributes.assert_not_called()
        self.node.getRole.return_value = 7
        for attributes in ('text', ['x' * 2049], ['x'] * 65, [1], ['x' * 1024] * 17):
            self.node.get_attributes.return_value = attributes
            with self.subTest(kind=type(attributes).__name__), self.assertRaises(CaseUnavailable):
                observer.alerts(self.app, self.ui, 20)

    def test_replaced_accessible_chain_and_engine_are_refused(self):
        with patch.object(observer.accessibility, 'capture', side_effect=[(self.frame, self.chain), (self.frame, [])]):
            with self.assertRaises(ValueError):
                observer.alerts(self.app, self.ui, 20)
        calls = []
        def changed():
            calls.append(True)
            if len(calls) == 2:
                self.app.observations['engine_identity'] = {'pid': 14}
        self.app.require_engine.side_effect = changed
        with self.assertRaises(ValueError):
            observer.alerts(self.app, self.ui, 20)

    def test_missing_and_ambiguous_alerts_remain_unverified(self):
        with patch.object(observer, 'alerts', return_value=[{}, {}]):
            with self.assertRaises(CaseUnavailable):
                observer.await_alert(self.app, self.ui)
        with patch.object(observer.time, 'monotonic', side_effect=[0, 16]):
            with self.assertRaises(CaseUnavailable):
                observer.await_alert(self.app, self.ui)
