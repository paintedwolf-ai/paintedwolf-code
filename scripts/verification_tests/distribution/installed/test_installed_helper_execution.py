"""The installed helper smoke requires a filtered descendant that cannot write outside."""
import copy
import unittest

from linux_release.installed_cases import kernel


def witness():
    return {'child': {'seccomp': 2, 'outside_write_denied': True}, 'child_reaped': True}


class InstalledHelperBoundaryTests(unittest.TestCase):
    def test_filtered_descendant_without_outside_write_passes(self):
        self.assertTrue(kernel.helper_observation(witness()))

    def test_unfiltered_writing_unreaped_or_unknown_shapes_fail(self):
        for change in (lambda row: row['child'].update(seccomp=0), lambda row: row['child'].update(seccomp=True),
                       lambda row: row['child'].update(outside_write_denied=False),
                       lambda row: row.update(child_reaped=False), lambda row: row['child'].update(extra=1),
                       lambda row: row.update(parent={}), lambda row: row.update(child='raw')):
            row = copy.deepcopy(witness())
            change(row)
            with self.subTest(row=row):
                self.assertFalse(kernel.helper_observation(row))
