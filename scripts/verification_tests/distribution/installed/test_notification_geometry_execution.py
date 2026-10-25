"""Pointer mapping uses actual current Mutter mode, never inferred scale or guessed display dimensions."""
import copy
import unittest

from linux_release.installed_input.notification_geometry import mapping
from linux_release.installed_cases.records import CaseUnavailable


def state():
    identity=['Virtual-1','Vendor','Product','serial']
    return [19,[[identity,[['1024x768',1024,768,60.0,1.0,[1.0],{'is-current':True}]],{}]],
        [[0,0,1.0,0,True,[identity.copy()],{}]],{'layout-mode':2}]


class NotificationGeometryTests(unittest.TestCase):
    def test_maps_exact_current_single_scale100_monitor(self):
        self.assertEqual(mapping(state()),{'serial':19,'logical_extent':[0,0,1024,768],
            'pixel_size':[1024,768],'scale_percent':100,'transform':0,'layout_mode':2})

    def test_rejects_unmapped_layout_and_ambiguous_current_modes(self):
        for kind in ('multiple','fractional','rotated','offset','foreign-monitor','not-primary','no-current','two-current','physical-layout'):
            value=copy.deepcopy(state())
            if kind=='multiple':value[1].append(copy.deepcopy(value[1][0]))
            elif kind=='fractional':value[2][0][2]=1.25
            elif kind=='rotated':value[2][0][3]=1
            elif kind=='offset':value[2][0][0]=1024
            elif kind=='foreign-monitor':value[2][0][5][0][0]='foreign'
            elif kind=='not-primary':value[2][0][4]=False
            elif kind=='no-current':value[1][0][1][0][6]['is-current']=False
            elif kind=='two-current':value[1][0][1].append(copy.deepcopy(value[1][0][1][0]))
            else:value[3]['layout-mode']=1
            with self.subTest(kind=kind),self.assertRaises(CaseUnavailable):mapping(value)

    def test_invalid_pixel_dimensions_or_serial_are_not_guessed(self):
        for replacement in (0,True,1<<32):
            value=state();value[0]=replacement
            with self.subTest(serial=replacement),self.assertRaises(ValueError):mapping(value)
        for replacement in (True,0,20000):
            value=state();value[1][0][1][0][1]=replacement
            with self.subTest(width=replacement),self.assertRaises(ValueError):mapping(value)


if __name__=='__main__':unittest.main()
