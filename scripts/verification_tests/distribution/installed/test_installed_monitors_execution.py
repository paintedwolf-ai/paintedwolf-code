"""Monitor preparation uses only declared scales and actual compositor modes."""
import copy
import os
import unittest
from linux_release.campaign.monitors import configuration
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


class MonitorLayoutRefusalTests(unittest.TestCase):
    def setUp(self):
        self.state = {'data': [7, [[['Virtual-1', 'QEMU', 'Monitor', 'one'],
                                   [['1920x1440@60', 1920, 1440, 60.0, 1.0, [1.0, 1.5, 2.0], {}]], {}]], [], {}]}

    def test_malformed_compositor_identity_and_mode_are_refused(self):
        identity = copy.deepcopy(self.state)
        identity['data'][1][0][0] = ['Virtual-1', 'QEMU', 'Monitor']
        mode = copy.deepcopy(self.state)
        mode['data'][1][0][1][0][0] = 5
        for bad in (identity, mode):
            with self.assertRaises(ValueError):
                configuration(bad, [150])

    def test_unsupported_scale_and_oversized_layout_are_refused(self):
        with self.assertRaises(CaseUnavailable):
            configuration(self.state, [400])
        with self.assertRaises(ValueError):
            configuration(self.state, [50])
        wide = copy.deepcopy(self.state)
        wide['data'][1][0][1][0][1] = 16384
        second = copy.deepcopy(wide['data'][1][0])
        second[0][0] = 'Virtual-2'
        wide['data'][1].append(second)
        with self.assertRaises(ValueError):
            configuration(wide, [100])


class MonitorPreparationSessionTests(unittest.TestCase):
    def setUp(self):
        self.state = {'data': [7, [[['Virtual-1', 'QEMU', 'Monitor', 'one'],
                                   [['1920x1440@60', 1920, 1440, 60.0, 1.0, [1.0, 1.5, 2.0], {}]], {}]], [], {}]}
        self.binding, self.session = {'guest': 'owned'}, {'session': 'disposable'}

    def run_prepare(self, scales, exit_code=0, percentages=(150,)):
        from unittest.mock import patch
        from linux_release.campaign import monitors
        readings = [{'scales_percent': scale, 'state': self.state} for scale in scales]
        with patch.object(monitors, 'require_owned_guest') as owned, \
                patch.object(monitors, 'gnome_monitors', side_effect=readings) as read, \
                patch.object(monitors, 'query', return_value={'exit_code': exit_code}) as query:
            result = monitors.prepare(self.binding, self.session, list(percentages))
        owned.assert_called_once_with(self.binding)
        self.assertEqual(read.call_count, len(scales))
        return result, query

    def test_matching_scales_leave_the_compositor_untouched(self):
        result, query = self.run_prepare([[150]])
        self.assertFalse(result['changed'])
        self.assertIs(result['before'], result['after'])
        query.assert_not_called()

    def test_declared_scales_are_applied_through_the_display_configuration_call(self):
        result, query = self.run_prepare([[100], [150]])
        self.assertTrue(result['changed'])
        self.assertEqual((result['before']['scales_percent'], result['after']['scales_percent']), ([100], [150]))
        command = query.call_args.args[0]
        self.assertEqual(command[:2], ['busctl', '--address=unix:path=/run/user/' + str(os.getuid()) + '/bus'])
        self.assertIn('ApplyMonitorsConfig', command)
        self.assertEqual(command[-13:], ['7', '1', '1', '0', '0', '1.5', '0', 'true', '1', 'Virtual-1', '1920x1440@60', '0', '0'])

    def test_compositor_refusal_and_unestablished_scales_are_reported(self):
        with self.assertRaises(CaseUnavailable):
            self.run_prepare([[100]], exit_code=1)
        with self.assertRaises(ValueError):
            self.run_prepare([[100], [100]])
