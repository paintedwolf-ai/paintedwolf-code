"""Protected installs must route the Settings observer through the broker and still follow the desktop."""
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from linux_release.installed_cases import protected_appearance as case

SESSION_NS = 'user:[4026531837]'


def observer(parent: int, namespace: str = SESSION_NS) -> dict:
    return {'pid': 900, 'parent_pid': parent, 'start_time_ticks': 1, 'user_namespace': namespace,
            'executable': case.OBSERVER}


class RouteTests(unittest.TestCase):
    def test_only_a_single_observer_outside_the_shell_and_in_the_session_namespace_is_brokered(self):
        self.assertEqual(case.route([observer(parent=77)], 500, SESSION_NS),
                         {'observers': 1, 'brokered': True, 'session_namespace': True})
        self.assertFalse(case.route([observer(parent=500)], 500, SESSION_NS)['brokered'],
                         'the shell started it itself')
        self.assertFalse(case.route([observer(parent=77, namespace='user:[4026532999]')], 500, SESSION_NS)['session_namespace'])
        for rows in ([], [observer(77), observer(78)]):
            with self.subTest(count=len(rows)):
                self.assertEqual(case.route(rows, 500, SESSION_NS)['brokered'], False)


class ToggleTests(unittest.TestCase):
    def test_each_desktop_flips_one_observed_setting_and_restores_it_exactly(self):
        gnome = case.toggle_for('ubuntu:GNOME')
        self.assertEqual(gnome['kind'], 'gnome-high-contrast')
        self.assertEqual(gnome['changed']('false'), 'true')
        self.assertEqual(gnome['changed']('true'), 'false')
        self.assertEqual(gnome['set']('true')[-1], 'true')
        kde = case.toggle_for('KDE')
        self.assertEqual(kde['kind'], 'kde-accent')
        self.assertIn('--delete', kde['set'](''), 'an absent accent is restored as absent')
        self.assertEqual(kde['set']('1,2,3')[-1], '1,2,3')
        self.assertNotEqual(kde['changed']('214,42,128'), '214,42,128')
        self.assertIsNone(case.toggle_for('XFCE'))


class FakeText:
    def __init__(self, run):
        self.run = run

    def getAttributeRun(self, offset, include_defaults):
        return (self.run, 0, 1)


class FakeNode:
    def __init__(self, run=None):
        self.run = run

    def queryText(self):
        if self.run is None:
            raise NotImplementedError
        return FakeText(self.run)


class FakeUI:
    def __init__(self, frames):
        self.frames = list(frames)

    def descendants(self, frame):
        return self.frames.pop(0) if len(self.frames) > 1 else self.frames[0]


class AppearanceTests(unittest.TestCase):
    def test_painted_colours_come_from_text_attribute_runs_in_any_reported_shape(self):
        ui = FakeUI([[FakeNode(['fg-color:0,0,0', 'bg-color:255,255,255']), FakeNode(),
                      FakeNode({'fg-color': '20,20,20', 'font-size': '11'}), FakeNode('fg-color:1,1,1; weight:400')]])
        self.assertEqual(case.painted_colours(ui, None),
                         {'fg-color=0,0,0', 'bg-color=255,255,255', 'fg-color=20,20,20', 'fg-color=1,1,1'})

    def test_following_waits_for_a_change_and_gives_up_at_its_deadline(self):
        before = frozenset({'fg-color=90,90,90'})
        ui = FakeUI([[FakeNode(['fg-color:90,90,90'])], [FakeNode(['fg-color:0,0,0'])]])
        with patch.object(case.time, 'sleep'):
            self.assertEqual(case.follows(ui, None, before, changed=True), (True, frozenset({'fg-color=0,0,0'})))
        still = FakeUI([[FakeNode(['fg-color:90,90,90'])]])
        with patch.object(case, 'SETTLE_SECONDS', 0), patch.object(case.time, 'sleep'):
            self.assertEqual(case.follows(still, None, before, changed=True), (False, before))

    def test_other_cases_stay_unverified(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(case, 'retain', side_effect=lambda
                binding, capability, case_id, root, status, observations, *rest: {'status': status, **observations}):
            result = case.execute({}, 'accessibility', 'orca-navigation', Path(directory))
            self.assertEqual((result['status'], result['reason']), ('unverified', 'native-desktop-interaction-driver-required'))


if __name__ == '__main__':
    unittest.main()
