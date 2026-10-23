"""A file tab tear-off is driven by fixed pointer steps and judged from compositor facts."""
import socket
import struct
import tempfile
import threading
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

from linux_release.installed_cases import tab_tear_off, wayland_globals
from linux_release.installed_input import protocol, qemu

IDENTITY = {'pid': 4242, 'parent_pid': 1, 'start_time_ticks': 99}


class PointerProtocolTests(unittest.TestCase):
    def test_a_tear_off_is_press_fixed_moves_and_release_with_escape_only_when_asked(self):
        drop = protocol.tear_off_operation('drop', (100, 40), (512, 576), (1024, 768))
        escape = protocol.tear_off_operation('escape', (100, 40), (512, 576), (1024, 768))
        for operation, ending in ((drop, None), (escape, ('esc',))):
            sequence = protocol.steps(operation)
            self.assertEqual(sequence[:2], (('@move', 100, 40), ('@button', 'down')))
            self.assertEqual(sequence[-1], ('@button', 'up'))
            self.assertEqual(sequence[2 + protocol.POINTER_INCREMENTS - 1], ('@move', 512, 576))
            self.assertEqual(sequence[-2] == ('esc',), ending is not None)
            self.assertEqual(protocol.pointer_display(operation), (1024, 768))
        self.assertIsNone(protocol.pointer_display('editor-replace-save-v1'))

    def test_only_validated_endings_and_in_display_coordinates_form_an_operation(self):
        for ending, start, end, display in (('fling', (1, 1), (2, 2), (1024, 768)),
                                            ('drop', (1024, 1), (2, 2), (1024, 768)),
                                            ('drop', (1, 1), (2, 768), (1024, 768)),
                                            ('drop', (-1, 1), (2, 2), (1024, 768)),
                                            ('drop', (1, 1), (2, 2), (100, 768))):
            with self.subTest(ending=ending, start=start, end=end, display=display), self.assertRaises(ValueError):
                protocol.tear_off_operation(ending, start, end, display)
        with self.assertRaises(ValueError):
            protocol.steps('file-tab-tear-off-v1-drop-1-1-2-2-1024x768; reboot')

    def test_pointer_requests_need_the_fullscreen_frame_proof_and_keyboard_requests_keep_theirs(self):
        binding = {'format_version': 1, 'execution_nonce': 'a' * 32, 'source_commit': 'b' * 40, 'driver_tree': 'c' * 40,
                   'artifact_id': 'candidate', 'environment_id': 'fedora-maintained-gnome', 'variant_id': 'fedora-44-scale-100'}
        operation = protocol.tear_off_operation('drop', (100, 40), (512, 576), (1024, 768))
        proof = {'launch': IDENTITY, 'frame': IDENTITY, 'frame_visible': True, 'frame_fullscreen': True}
        document = {'format_version': 1, 'binding': binding, 'transaction_id': 'd' * 32,
                    'operation': operation, 'ordinal': 0, 'focus': proof}
        self.assertEqual(protocol.request(document, binding), document)
        for change in ({'frame_fullscreen': False}, {'frame': {**IDENTITY, 'pid': 7}}, {'frame_visible': None}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                protocol.request({**document, 'focus': {**proof, **change}}, binding)
        with self.assertRaises(ValueError):
            protocol.request({**document, 'operation': 'editor-replace-save-v1'}, binding)


class PointerDeliveryTests(unittest.TestCase):
    def setUp(self):
        delay = patch.object(qemu.time, 'sleep')
        delay.start()
        self.addCleanup(delay.stop)

    def actor(self, display=(1024, 768)):
        lease = {'format_version': 1, 'vm_id': 'owned-test', 'uid': 1000, 'pid': 42, 'start_time_ticks': 17,
                 'argv_sha256': 'a' * 64, 'qmp': {'path': '/tmp/owned/socket', 'device': 1, 'inode': 2},
                 'ssh': {'host': '127.0.0.1', 'port': 45557, 'user': 'pwtest'}}
        actor = qemu.Actor(-1, Path('/unused'), {'vm_id': 'owned-test'}, lease, 'a' * 32)
        actor.validate = Mock()
        actor.display = Mock(return_value=display)
        actor.command = Mock(side_effect=lambda name, args=None:
                             {'id': 3, 'return': {'running': True, 'status': 'running'}}
                             if name == 'query-status' else {'id': 4, 'return': {}})
        return actor

    def test_pointer_steps_map_framebuffer_pixels_onto_qemu_absolute_axes(self):
        operation = protocol.tear_off_operation('escape', (1023, 0), (512, 384), (1024, 768))
        actor = self.actor()
        for ordinal in range(len(protocol.steps(operation))):
            actor.send(operation, ordinal, transaction_id='b' * 32)
        sent = [call.args for call in actor.command.call_args_list if call.args[0] != 'query-status']
        self.assertEqual(sent[0], ('input-send-event', {'events': [
            {'type': 'abs', 'data': {'axis': 'x', 'value': 0x7fff}},
            {'type': 'abs', 'data': {'axis': 'y', 'value': 0}}]}))
        self.assertEqual(sent[1], ('input-send-event', {'events': [{'type': 'btn', 'data': {'down': True, 'button': 'left'}}]}))
        self.assertEqual(sent[-2], ('send-key', {'keys': [{'type': 'qcode', 'data': 'esc'}], 'hold-time': 80}))
        self.assertEqual(sent[-1], ('input-send-event', {'events': [{'type': 'btn', 'data': {'down': False, 'button': 'left'}}]}))
        actor.display.assert_called_once_with()

    def test_a_drag_planned_for_another_display_size_never_presses(self):
        operation = protocol.tear_off_operation('drop', (10, 10), (500, 500), (1024, 768))
        actor = self.actor(display=(1280, 800))
        with self.assertRaises(ValueError):
            actor.send(operation, 0, transaction_id='b' * 32)
        self.assertFalse(any(call.args[0] == 'input-send-event' for call in actor.command.call_args_list))

    def test_screendump_headers_and_pointer_shapes_are_checked(self):
        self.assertEqual(qemu.framebuffer_size(b'P6\n1024 768\n255\n\x00'), (1024, 768))
        for raw in (b'P3\n1 1\n255\n', b'P6\n10\n', b'P6\nx 1\n255\n'):
            with self.assertRaises(ValueError):
                qemu.framebuffer_size(raw)
        with self.assertRaises(ValueError):
            qemu.pointer_events(('@button', 'middle'), (1024, 768))
        with self.assertRaises(ValueError):
            qemu.pointer_events(('@warp', 1, 2), (1024, 768))


def event(object_id: int, opcode: int, payload: bytes) -> bytes:
    return struct.pack('<II', object_id, (8 + len(payload)) << 16 | opcode) + payload


def announce(name: int, interface: str, version: int, registry: int) -> bytes:
    return event(registry, 0, struct.pack('<I', name) + wayland_globals.string(interface) + struct.pack('<I', version))


class Compositor(threading.Thread):
    """Answers the reader's registry, binding and sync requests with fixed events."""

    def __init__(self, connection, interfaces, outputs=1):
        super().__init__(daemon=True)
        self.connection, self.interfaces, self.outputs = connection, interfaces, outputs

    def run(self):
        globals_ = [(index + 1, name, version) for index, (name, version) in enumerate(self.interfaces)]
        globals_ += [(100 + index, 'wl_output', 4) for index in range(self.outputs)]
        replies = []
        for registry, callback in ((2, 3), (4, 5)):
            replies.append(b''.join(announce(name, interface, version, registry) for name, interface, version in globals_)
                           + event(callback, 0, struct.pack('<I', 0)))
        replies.append(event(6, 1, struct.pack('<Iiii', 0, 640, 480, 60000)) + event(6, 1, struct.pack('<Iiii', 1, 2048, 1536, 60000))
                       + event(8, 1, struct.pack('<ii', 1024, 768)) + event(9, 0, struct.pack('<I', 0)))
        try:
            for reply in replies:
                if not self.connection.recv(4096):
                    return
                self.connection.sendall(reply)
        except OSError:
            return


class WaylandGlobalsTests(unittest.TestCase):
    def observe(self, interfaces, outputs=1):
        client, server = socket.socketpair()
        self.addCleanup(client.close)
        self.addCleanup(server.close)
        Compositor(server, interfaces, outputs).start()
        client.settimeout(5)
        return wayland_globals.observe_on(client)

    def test_globals_and_the_single_outputs_current_and_logical_sizes_are_read(self):
        facts = self.observe([('xdg_toplevel_drag_manager_v1', 1), ('zxdg_output_manager_v1', 3), ('wl_seat', 9)])
        self.assertEqual(facts['globals'], {'xdg_toplevel_drag_manager_v1': 1, 'zxdg_output_manager_v1': 3, 'wl_output': 4})
        self.assertEqual((facts['outputs'], facts['mode'], facts['logical_size']), (1, [2048, 1536], [1024, 768]))

    def test_several_outputs_or_no_xdg_output_leave_geometry_unknown(self):
        self.assertIsNone(self.observe([('zxdg_output_manager_v1', 3)], outputs=2)['mode'])
        facts = self.observe([('wl_data_device_manager', 3)])
        self.assertEqual((facts['mode'], facts['logical_size']), (None, None))

    def test_protocol_errors_and_malformed_events_refuse(self):
        client, server = socket.socketpair()
        self.addCleanup(client.close)
        self.addCleanup(server.close)
        server.sendall(event(1, 0, struct.pack('<II', 1, 0) + wayland_globals.string('invalid object')))
        client.settimeout(5)
        with self.assertRaises(ValueError):
            wayland_globals.observe_on(client)
        with self.assertRaises(ValueError):
            wayland_globals.read_string(struct.pack('<I', 8) + b'abc', 0)
        with self.assertRaises(ValueError):
            wayland_globals.observe({})


class TearOffOutcomeTests(unittest.TestCase):
    def test_the_protocol_global_decides_whether_escape_must_keep_the_tab(self):
        self.assertEqual(tab_tear_off.expected_outcomes({'xdg_toplevel_drag_manager_v1': 1}),
                         {'mode': 'live', 'drop': True, 'escape': False})
        self.assertEqual(tab_tear_off.expected_outcomes({'wl_data_device_manager': 3}),
                         {'mode': 'fallback', 'drop': True, 'escape': True})

    def test_only_the_expected_window_and_strip_state_passes(self):
        live = tab_tear_off.expected_outcomes({'xdg_toplevel_drag_manager_v1': 1})
        torn, kept = {'torn_windows': 1, 'tab_in_strip': False}, {'torn_windows': 0, 'tab_in_strip': True}
        self.assertEqual(tab_tear_off.evaluate(live, {'escape': kept, 'drop': torn}), [])
        self.assertEqual(tab_tear_off.evaluate(live, {'escape': torn, 'drop': torn}),
                         ['escape: expected the tab to stay in its strip'])
        for broken in ({'torn_windows': 2, 'tab_in_strip': False}, {'torn_windows': 1, 'tab_in_strip': True},
                       {'torn_windows': 0, 'tab_in_strip': False}):
            with self.subTest(broken=broken):
                self.assertEqual(tab_tear_off.evaluate(live, {'escape': kept, 'drop': broken}),
                                 ['drop: expected the tab in one new window'])
        fallback = tab_tear_off.expected_outcomes({})
        self.assertEqual(tab_tear_off.evaluate(fallback, {'escape': torn, 'drop': torn}), [])
        self.assertEqual(tab_tear_off.evaluate(fallback, {'drop': torn}), ['escape: not observed'])

    def test_logical_window_points_scale_to_framebuffer_pixels_inside_the_display(self):
        self.assertEqual(tab_tear_off.physical((100.5, 20), [2048, 1536], [1024, 768]), (201, 40))
        self.assertEqual(tab_tear_off.physical((100, 20), [1536, 1152], [1024, 768]), (150, 30))
        with self.assertRaises(ValueError):
            tab_tear_off.physical((1024, 10), [1024, 768], [1024, 768])

    def test_other_cases_and_runs_without_physical_input_stay_unverified(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(tab_tear_off, 'retain', side_effect=lambda
                binding, capability, case, root, status, observations, *rest: {'status': status, **observations}):
            root = Path(directory)
            other = tab_tear_off.execute({}, 'windowing', 'geometry-recovery', root)
            self.assertEqual((other['status'], other['reason']), ('unverified', 'native-desktop-interaction-driver-required'))
            unpointed = tab_tear_off.execute({}, 'windowing', 'file-tab-tear-off', root)
            self.assertEqual((unpointed['status'], unpointed['reason']), ('unverified', 'fixed-physical-pointer-required'))


if __name__ == '__main__':
    unittest.main()
