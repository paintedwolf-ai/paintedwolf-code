"""A DOM dialog cannot replace the actual enumerated GTK window's input authority."""
import unittest
from types import SimpleNamespace
from unittest.mock import Mock
from linux_release.installed_input import accessibility


def node(bus, path, pid, parent=None):
    return Mock(app=SimpleNamespace(bus_name=bus), path=path,
                get_process_id=Mock(return_value=pid), get_parent=Mock(return_value=parent))


class AccessibleAncestorTests(unittest.TestCase):
    def test_embedded_dialog_is_traversed_to_actual_native_window(self):
        frame = node(':1.2', '/frame', 100)
        dialog = node(':1.3', '/dialog', 101, frame)
        entry = node(':1.3', '/entry', 101, dialog)
        selected, chain = accessibility.capture(entry, [({}, frame)])
        self.assertIs(selected, frame)
        self.assertEqual([row['path'] for row in chain], ['/entry', '/dialog', '/frame'])
        self.assertEqual([row['pid'] for row in chain], [101, 101, 100])

    def test_foreign_window_cycles_disappearance_and_changed_parent_refuse(self):
        frame = node(':1.2', '/frame', 100)
        entry = node(':1.3', '/entry', 101, frame)
        foreign = node(':1.4', '/frame', 100)
        with self.assertRaises(ValueError):
            accessibility.capture(entry, [({}, foreign)])
        entry.get_parent.return_value = entry
        with self.assertRaises(ValueError):
            accessibility.capture(entry, [({}, frame)])
        entry.get_parent.side_effect = [frame, foreign]
        with self.assertRaises(ValueError):
            accessibility.capture(entry, [({}, frame)])
        entry.get_parent.side_effect = [frame, None]
        with self.assertRaises(accessibility.InventoryChanged):
            accessibility.capture(entry, [({}, frame)])
        entry.get_parent.side_effect = [frame, FileNotFoundError()]
        with self.assertRaises(OSError):
            accessibility.capture(entry, [({}, frame)])

    def test_object_identity_replacement_and_unknown_shapes_refuse(self):
        frame = node(':1.2', '/frame', 100)
        entry = node(':1.3', '/entry', 101, frame)
        entry.get_process_id.side_effect = [101, 999]
        with self.assertRaises(ValueError):
            accessibility.capture(entry, [({}, frame)])
        for missing in (None, SimpleNamespace(app=None)):
            with self.assertRaises(accessibility.InventoryChanged):
                accessibility.identity(missing)
        for value in ({'pid': 100}, {'bus_name': 'title', 'path': '/frame', 'pid': 100},
                      {'bus_name': ':1.2', 'path': 'frame', 'pid': True}):
            with self.assertRaises(ValueError):
                accessibility.validate_identity(value)
