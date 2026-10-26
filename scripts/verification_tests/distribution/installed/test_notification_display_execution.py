"""Only the declared captured renderer maps canonical wire text to exact accessible labels."""
import hashlib
from types import SimpleNamespace
import unittest
from unittest.mock import Mock

from linux_release import notification_display as display
from linux_release.installed_cases.records import CaseUnavailable


def captured(markup=True):
    return {'request_serial':8, 'request_sequence':1, 'reply_sequence':2,
        'body':True, 'body-markup':markup, 'actions':True}


class NotificationDisplayTests(unittest.TestCase):
    def test_one_pass_canonical_xml_entities_and_lf_preserve_distinct_wire_hash(self):
        wire = '&lt;task&gt; &amp; &quot;quoted&quot; &apos;word&apos; &amp;lt;\nnext'
        plain = '<task> & "quoted" \'word\' &lt; next'
        result = display.digests('A\nB', wire, captured())
        self.assertEqual(result['display_body_sha256'], hashlib.sha256(plain.encode()).hexdigest())
        self.assertEqual(result['display_title_sha256'], hashlib.sha256(b'A B').hexdigest())
        self.assertEqual(result['wire_body_sha256'], hashlib.sha256(wire.encode()).hexdigest())
        self.assertNotIn('quoted', str(result))

    def test_plain_capability_does_not_decode_markup_or_apply_extra_normalization(self):
        result = display.digests(' title ', '&lt;task&gt;\nnext', captured(False))
        self.assertEqual(result['display_body_sha256'], hashlib.sha256(b'&lt;task&gt; next').hexdigest())
        self.assertEqual(result['display_title_sha256'], hashlib.sha256(b' title ').hexdigest())

    def test_noncanonical_markup_unknown_numeric_and_unmodeled_controls_are_unverified(self):
        for wire in ('<b>task</b>', 'bare & value', '&unknown;', '&#60;', '&#x3c;', '"quoted"',
                "'word'", 'bare >', 'CR\rnext', 'control\x01', 'control\x80'):
            with self.subTest(wire=wire), self.assertRaises(CaseUnavailable):
                display.digests('title', wire, captured())

    def test_missing_body_or_actions_cannot_supply_click_label_authority(self):
        for key in ('body', 'actions'):
            with self.subTest(key=key), self.assertRaises(CaseUnavailable):
                display.digests('title', 'body', {**captured(), key:False})
        for key, value in (('body', 1), ('reply_sequence', 1), ('request_serial', True)):
            with self.subTest(key=key), self.assertRaises(ValueError):
                display.digests('title', 'body', {**captured(), key:value})

    def test_current_server_query_is_typed_unique_destination_and_same_deadline(self):
        reader = Mock()
        reader.call.return_value = SimpleNamespace(get_type_string=lambda:'(ssss)', unpack=lambda:display.SERVER)
        self.assertEqual(display.server_information(reader, ':1.7', 12), display.SERVER)
        self.assertEqual(reader.call.call_args.kwargs, {'user':True, 'destination':':1.7', 'deadline':12})
        self.assertEqual(reader.call.call_args.args[2], 'GetServerInformation')
        for signature, values in (('(ssss)', ['gnome-shell','GNOME','47.0','1.2']), ('(s)', display.SERVER)):
            reader.call.return_value = SimpleNamespace(get_type_string=lambda:signature, unpack=lambda:values)
            with self.assertRaises(CaseUnavailable):display.server_information(reader, ':1.7', 12)


if __name__ == '__main__':unittest.main()
