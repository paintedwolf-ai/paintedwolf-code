import datetime
import email.utils
import io
import unittest
import urllib.error
import urllib.request
from unittest.mock import patch

from application_http import open_request, retry_delay


class ApplicationHTTPTests(unittest.TestCase):
    def test_admission_rejections_preserve_the_exact_mutation(self):
        request = urllib.request.Request('http://localhost/v1/sessions', data=b'{"project_id":"one"}')
        errors = [urllib.error.HTTPError(request.full_url, 429, '', headers, io.BytesIO(b''))
                  for headers in [{'Retry-After': '7'}, {}, {}]]
        response = io.BytesIO(b'{"id":"created-once"}')
        with patch('urllib.request.urlopen', side_effect=[*errors, response]) as send, \
                patch('application_http.time.sleep') as sleep:
            self.assertIs(open_request(request), response)
            self.assertEqual([c.args[0] for c in sleep.call_args_list], [7, 2, 4])
            self.assertEqual(send.call_count, 4)
            for call in send.call_args_list:
                self.assertIs(call.args[0], request)
                self.assertEqual(call.kwargs, {'timeout': None})

    def test_ambiguous_mutations_and_other_rejections_are_not_replayed(self):
        for code in [400, 404, 408, 500, 503]:
            error = urllib.error.HTTPError('http://localhost', code, '', {}, io.BytesIO(b'diagnostic'))
            with self.subTest(code=code), patch('urllib.request.urlopen', side_effect=error) as send, \
                    patch('application_http.time.sleep') as sleep:
                with self.assertRaises(urllib.error.HTTPError) as raised:
                    open_request(urllib.request.Request('http://localhost', data=b'{}'))
                self.assertIs(raised.exception, error)
                self.assertEqual(error.read(), b'diagnostic')
                send.assert_called_once()
                sleep.assert_not_called()

    def test_transport_disconnect_is_not_replayed(self):
        with patch('urllib.request.urlopen', side_effect=urllib.error.URLError('disconnected')) as send:
            with self.assertRaises(urllib.error.URLError):
                open_request(urllib.request.Request('http://localhost', data=b'{}'))
            send.assert_called_once()

    def test_retry_after_uses_protocol_grammar_and_server_delay(self):
        self.assertEqual(retry_delay('3600', 4), 3600)
        self.assertEqual(retry_delay('0', 4), 1)
        for value in [None, '', '-4', 'nan', '2.5', 'retry in 10 seconds']:
            self.assertEqual(retry_delay(value, 4), 4)
        future = datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(seconds=120)
        self.assertTrue(118 < retry_delay(email.utils.format_datetime(future), 4) <= 120)
        self.assertEqual(retry_delay('Wed, 01 Jan 2020 00:00:00 GMT', 4), 1)

    def test_sustained_throttling_has_no_attempt_cutoff(self):
        errors = [urllib.error.HTTPError('http://localhost', 429, '', {}, io.BytesIO()) for _ in range(20)]
        with patch('urllib.request.urlopen', side_effect=[*errors, 'accepted']), \
                patch('application_http.time.sleep') as sleep:
            self.assertEqual(open_request(urllib.request.Request('http://localhost')), 'accepted')
            self.assertEqual(sleep.call_count, 20)
            self.assertEqual(sleep.call_args.args, (60,))
