"""Monitor preparation uses only declared scales and actual compositor modes."""
import copy
import unittest
from linux_release.installed_monitors import configuration
from linux_release.installed_cases.records import CaseUnavailable


class MonitorPreparationTests(unittest.TestCase):
    def test_temporary_layout_uses_actual_supported_mode_and_refuses_missing_mixed_monitor(self):
        state = {'data': [7, [[['Virtual-1', 'QEMU', 'Monitor', 'one'],
                              [['1920x1440@60', 1920, 1440, 60.0, 1.0, [1.0, 1.5, 2.0], {}]], {}]], [], {}]}
        actual = configuration(state, [150])
        self.assertEqual(actual, ['7', '1', '1', '0', '0', '1.5', '0', 'true', '1', 'Virtual-1', '1920x1440@60', '0', '0'])
        with self.assertRaises(CaseUnavailable):
            configuration(state, [100, 150])
        for mutate in (lambda value: value['data'][1][0][1][0].__setitem__(1, True),
                       lambda value: value['data'][1][0][1][0].__setitem__(5, [True])):
            bad = copy.deepcopy(state)
            mutate(bad)
            with self.assertRaises(ValueError):
                configuration(bad, [150])
        with self.assertRaises(ValueError):
            configuration(state, [True])
