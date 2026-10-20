"""Plasma monitor observations retain typed compositor authority and exact active scales."""
import copy
import json
import os
import unittest
from unittest.mock import patch

from scripts.linux_release import installed_plasma as plasma
from scripts.linux_release.installed_cases.records import CaseUnavailable


def output(identifier=1, scale=1):
    return {'id': identifier, 'connected': True, 'enabled': True, 'scale': scale,
            'currentModeId': 'actual-mode', 'modes': [{'id': 'actual-mode', 'size': {'width': 1920, 'height': 1080}}]}


class PlasmaConfigurationTests(unittest.TestCase):
    def test_actual_connected_modes_retain_mixed_scales(self):
        state = {'outputs': [output(), output(2, 1.5), output(3, 2)]}
        observed = plasma.configuration(state)
        self.assertEqual([100, 150, 200], observed['scales_percent'])
        self.assertEqual(state, observed['state'])

    def test_inactive_output_cannot_establish_a_required_scale(self):
        inactive = output(2, 2)
        inactive['enabled'] = False
        self.assertEqual([100], plasma.configuration({'outputs': [output(), inactive]})['scales_percent'])
        with self.assertRaises(CaseUnavailable):
            plasma.configuration({'outputs': [inactive]})

    def test_bad_active_mode_or_scale_refuses(self):
        for changes in ({'connected': False}, {'scale': True}, {'scale': float('nan')}, {'scale': 1.333},
                        {'scale': 0.5}, {'currentModeId': 'missing'}, {'modes': []},
                        {'modes': [{'id': 'actual-mode', 'size': {'width': True, 'height': 1080}}]}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                plasma.configuration({'outputs': [{**output(), **changes}]})

    def test_repeated_output_and_mode_identities_refuse(self):
        with self.assertRaises(ValueError):
            plasma.configuration({'outputs': [output(), output()]})
        repeated = output()
        repeated['modes'] *= 2
        with self.assertRaises(ValueError):
            plasma.configuration({'outputs': [repeated]})

    def test_unrelated_or_remote_session_cannot_supply_plasma_authority(self):
        with patch.object(plasma, 'query') as query:
            for session in ({'Desktop': 'GNOME'}, {'Desktop': 'KDE', 'Type': 'x11'},
                            {'Desktop': 'KDE', 'Type': 'wayland', 'Active': 'yes', 'Remote': 'yes', 'User': str(os.getuid())}):
                with self.assertRaises(CaseUnavailable):
                    plasma.owner(session)
            query.assert_not_called()

    def test_bus_owner_requires_typed_native_pid(self):
        session = {'Desktop': 'KDE', 'Type': 'wayland', 'Active': 'yes', 'Remote': 'no', 'User': str(os.getuid())}
        for value in ({'type': 's', 'data': ['123']}, {'type': 'u', 'data': [True]}, {'type': 'u', 'data': [1]}):
            with patch.object(plasma, 'query', return_value={'exit_code': 0, 'stdout': json.dumps(value)}):
                with self.assertRaises(ValueError):
                    plasma.owner(session)

    def test_fixed_kscreen_query_is_bracketed_by_the_same_owner_and_socket(self):
        authority = {'window': {'pid': 123}, 'socket_owner': {'pid': 122}}
        display = {'name': 'wayland-1', 'path': '/run/user/1000/wayland-1'}
        with patch.object(plasma, 'owner', return_value=authority) as owner, \
                patch.object(plasma, 'display', return_value=display), \
                patch.object(plasma, 'query', return_value={'exit_code': 0, 'stdout': json.dumps({'outputs': [output()]})}) as query:
            observed = plasma.observe({'Desktop': 'KDE'})
            self.assertEqual([100], observed['scales_percent'])
            self.assertEqual(authority, observed['state']['kwin'])
            self.assertEqual(2, owner.call_count)
            self.assertEqual(['kscreen-doctor', '-j'], query.call_args.args[0][-2:])
            self.assertIn('QT_QPA_PLATFORM=wayland', query.call_args.args[0])
            owner.side_effect = [authority, {'window': {'pid': 124}}]
            with self.assertRaisesRegex(ValueError, 'authority changed'):
                plasma.observe({'Desktop': 'KDE'})

    def test_changed_socket_refuses_after_read(self):
        with patch.object(plasma, 'owner', return_value={}), \
                patch.object(plasma, 'display', side_effect=[{'name': 'wayland-1'}, {'name': 'wayland-2'}]), \
                patch.object(plasma, 'query', return_value={'exit_code': 0, 'stdout': json.dumps({'outputs': [output()]})}):
            with self.assertRaisesRegex(ValueError, 'authority changed'):
                plasma.observe({'Desktop': 'KDE'})
