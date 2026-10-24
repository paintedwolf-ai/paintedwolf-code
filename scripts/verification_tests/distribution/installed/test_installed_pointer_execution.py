"""Pointer primitives preserve the closed registry and owned hardware transaction."""
from dataclasses import FrozenInstanceError
import hashlib
import os
from pathlib import Path
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_input import pointer, protocol, qemu

MODE = (1024, 768)
OPERATION = 'fixture-only-selected-pointer'
SEQUENCE = (pointer.PointerStep(('@move', 1023, 0), MODE),
            pointer.PointerStep(('@button', 'down'), MODE),
            pointer.PointerStep(('@button', 'up'), MODE))


class PointerCodecTests(unittest.TestCase):
    def test_pixels_and_left_button_have_exact_qmp_encoding(self):
        self.assertEqual(SEQUENCE[0].qmp_events(), [
            {'type': 'abs', 'data': {'axis': 'x', 'value': 0x7fff}},
            {'type': 'abs', 'data': {'axis': 'y', 'value': 0}}])
        self.assertEqual(SEQUENCE[1].qmp_events(), [
            {'type': 'btn', 'data': {'down': True, 'button': 'left'}}])
        self.assertEqual(SEQUENCE[2].qmp_events(), [
            {'type': 'btn', 'data': {'down': False, 'button': 'left'}}])
        self.assertEqual(pointer.sequence_display(SEQUENCE), MODE)
        with self.assertRaises(FrozenInstanceError):
            SEQUENCE[0].display = (1280, 800)

    def test_types_bounds_and_unknown_events_refuse_before_encoding(self):
        for event, mode in [(('@move', True, 0), MODE), (('@move', -1, 0), MODE),
                (('@move', 1024, 0), MODE), (('@move', 1, 768), MODE),
                (('@button', 'middle'), MODE), (('@button', []), MODE),
                (('@wheel', 1), MODE), (('@move', 1, 2), (True, 768)),
                (('@move', 1, 2), (319, 768)), (('@move', 1, 2), (1024, 16385)),
                (('@move', 1, 2), [1024, 768]), (['@move', 1, 2], MODE)]:
            with self.subTest(event=event, mode=mode), self.assertRaises(ValueError):
                pointer.PointerStep(event, mode)
        for sequence in [(SEQUENCE[1], SEQUENCE[2]), (SEQUENCE[0], SEQUENCE[2]),
                SEQUENCE[:2], (SEQUENCE[0], SEQUENCE[1], SEQUENCE[1], SEQUENCE[2]),
                (SEQUENCE[0], pointer.PointerStep(('@button', 'down'), (1280, 800)), SEQUENCE[2]),
                (('@move', 1, 2),), (None,)]:
            with self.subTest(sequence=sequence), self.assertRaises(ValueError):
                pointer.sequence_display(sequence)
        self.assertIsNone(pointer.sequence_display((('ctrl', 'a'),)))

    def test_binary_ppm_header_is_bounded_and_requires_qemu_pixel_format(self):
        self.assertEqual(pointer.framebuffer_size(b'P6\n1024 768\n255\n\x00'), MODE)
        for raw in (b'P3\n1024 768\n255\n', b'P6\n1024 768\n256\n', b'P6\n1 1\n255\n',
                    b'P6\nx 768\n255\n', b'P6\n1024\n', b'P6\n1024 999999\n255\n', b'x' * 129):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                pointer.framebuffer_size(raw)

    def test_no_pointer_operation_is_registered_and_keyboard_hash_is_unchanged(self):
        for operation in (OPERATION, 'click-1-2', 'file-tab-tear-off-v1-drop-1-1-2-2-1024x768'):
            with self.assertRaises(ValueError):
                protocol.steps(operation)
        self.assertEqual(protocol.event_sha256('editor-replace-save-v1', 0),
                         hashlib.sha256(b'["ctrl","a"]').hexdigest())
        with patch.object(protocol, 'steps', return_value=SEQUENCE):
            self.assertEqual(protocol.event_sha256(OPERATION, 0),
                             hashlib.sha256(b'["@move",1023,0]').hexdigest())


class PointerDeliveryTests(unittest.TestCase):
    def setUp(self):
        # This source fixture supplies an internal selected sequence; no operation is registered.
        for module in (qemu, protocol):
            selected = patch.object(module, 'steps', return_value=SEQUENCE)
            selected.start(); self.addCleanup(selected.stop)
        delay = patch.object(qemu.time, 'sleep'); delay.start(); self.addCleanup(delay.stop)

    def actor(self):
        value = {'vm_id': 'owned-test', 'uid': os.getuid()}
        actor = qemu.Actor(-1, Path('/unused'), {'vm_id': 'owned-test'}, value, 'a' * 32)
        actor.validate = Mock()
        actor.display = Mock(return_value=MODE)
        actor.command = Mock(side_effect=lambda name, args=None:
            {'id': 1, 'return': {'running': True, 'status': 'running'}}
            if name == 'query-status' else {'id': 2, 'return': {}})
        return actor

    def send(self, actor, ordinal, transaction='b' * 32):
        return actor.send(OPERATION, ordinal, transaction_id=transaction)

    def inputs(self, actor):
        return [row.args[1]['events'] for row in actor.command.call_args_list if row.args[0] == 'input-send-event']

    def test_selected_steps_get_independent_acknowledgments_and_mode_checks(self):
        actor = self.actor()
        for ordinal in range(3):
            receipt = self.send(actor, ordinal)
            self.assertEqual(receipt['ordinal'], ordinal)
            self.assertEqual(receipt['qmp_ack'], {'id': 2, 'return': {}})
            self.assertEqual(receipt['event_sha256'], protocol.event_sha256(OPERATION, ordinal))
        self.assertEqual(self.inputs(actor), [step.qmp_events() for step in SEQUENCE])
        self.assertEqual(actor.display.call_count, 2)
        self.assertFalse(actor.pointer_down)
        self.assertFalse(actor.pointer_cleanup['attempted'])

    def test_initial_mode_mismatch_never_moves_or_presses_and_poisons_actor(self):
        actor = self.actor(); actor.display.return_value = (1280, 800)
        with self.assertRaises(ValueError):
            self.send(actor, 0)
        with self.assertRaises(ValueError):
            self.send(actor, 0)
        self.assertEqual(self.inputs(actor), [])

    def test_lost_press_ack_releases_only_owned_button_and_never_resumes(self):
        actor = self.actor(); command = actor.command.side_effect
        def lost(name, args=None):
            if name == 'input-send-event' and args['events'] == SEQUENCE[1].qmp_events():
                raise TimeoutError('lost press acknowledgment')
            return command(name, args)
        actor.command.side_effect = lost
        self.send(actor, 0)
        with self.assertRaises(TimeoutError):
            self.send(actor, 1)
        self.assertEqual(self.inputs(actor), [step.qmp_events() for step in SEQUENCE])
        self.assertEqual(actor.pointer_cleanup, {'attempted': True, 'acknowledged': True})
        before = actor.command.call_count
        with self.assertRaises(ValueError):
            self.send(actor, 2)
        actor.close()
        self.assertEqual(actor.command.call_count, before)

    def test_invalid_ordinal_or_overlap_after_press_cleans_up_without_more_input(self):
        for ordinal, transaction in ((1, 'b' * 32), (0, 'c' * 32)):
            actor = self.actor(); self.send(actor, 0); self.send(actor, 1)
            with self.assertRaises(ValueError):
                self.send(actor, ordinal, transaction)
            self.assertEqual(self.inputs(actor)[-1], SEQUENCE[2].qmp_events())
            self.assertTrue(actor.failed)
            self.assertTrue(actor.pointer_cleanup['acknowledged'])

    def test_changed_lease_forbids_cleanup_input_and_retains_unverified_outcome(self):
        actor = self.actor(); self.send(actor, 0); self.send(actor, 1)
        actor.validate.side_effect = ValueError('lease replaced')
        with self.assertRaises(ValueError):
            self.send(actor, 2)
        self.assertEqual(len(self.inputs(actor)), 2)
        self.assertEqual(actor.pointer_cleanup, {'attempted': True, 'acknowledged': False})
        actor.close()
        self.assertEqual(len(self.inputs(actor)), 2)

    def test_mode_change_before_final_release_refuses_but_releases_owned_button(self):
        actor = self.actor(); self.send(actor, 0); self.send(actor, 1)
        actor.display.return_value = (1280, 800)
        with self.assertRaises(ValueError):
            self.send(actor, 2)
        self.assertEqual(self.inputs(actor)[-1], SEQUENCE[2].qmp_events())
        self.assertTrue(actor.pointer_cleanup['acknowledged'])

    def test_close_after_press_uses_same_checked_channel_and_only_one_release(self):
        actor = self.actor(); self.send(actor, 0); self.send(actor, 1)
        actor.close(); actor.close()
        self.assertEqual(self.inputs(actor), [step.qmp_events() for step in SEQUENCE])
        self.assertTrue(actor.closed)
        self.assertTrue(actor.pointer_cleanup['acknowledged'])

    def test_actual_private_screendump_header_is_read_without_retaining_pixels(self):
        actor = self.actor(); del actor.display
        paths = []
        def dump(name, args=None):
            self.assertEqual(name, 'screendump')
            path = Path(args['filename']); paths.append(path)
            self.assertEqual(path.parent.stat().st_mode & 0o777, 0o700)
            path.write_bytes(b'P6\n1024 768\n255\n' + b'\x00' * 128)
            return {'id': 3, 'return': {}}
        actor.command.side_effect = dump
        self.assertEqual(actor.display(), MODE)
        self.assertFalse(paths[0].parent.exists())
