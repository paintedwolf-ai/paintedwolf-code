import datetime
import email.utils
import io
import json
import pathlib
import tempfile
import unittest
import urllib.error
import urllib.request
from unittest.mock import patch
from bootstrap_policy import configure, open_request, retry_delay
from episode import disposition


class BootstrapPolicyTests(unittest.TestCase):
    def test_success_preserves_utility_and_selects_exact_coordinator_and_worker(self):
        original={'coordinator':{},'lite':{'provider_id':'utility','model':'small'},'agent_pool':{}}
        requests=[]
        def respond(request, timeout=None):
            requests.append(request)
            return io.BytesIO(json.dumps(original if len(requests)==1 else {'saved':True}).encode())
        with tempfile.TemporaryDirectory() as tmp, patch('urllib.request.urlopen',side_effect=respond):
            root=pathlib.Path(tmp)
            self.assertTrue(configure('http://localhost','fixture-token',{'provider_id':'cloud','model':'candidate'},
                                      {'provider_id':'cloud','model':'worker'},root))
            self.assertEqual([r.get_method() for r in requests],['GET','PATCH'])
            selected=json.loads(requests[1].data)
            self.assertEqual(selected['lite'],original['lite'])
            self.assertEqual(selected['coordinator']['model'],'candidate')
            self.assertEqual(selected['agent_pool']['models'][0]['model'],'worker')
            self.assertFalse((root/'setup-failure.json').exists())

    def test_policy_rejection_is_unmeasured_and_preserves_structured_response(self):
        error=urllib.error.HTTPError('http://localhost',400,'bad input',{},io.BytesIO(b'{"code":"invalid_request","message":"The model policy is not valid.","details":{"reason":"unverified capability"}}'))
        with tempfile.TemporaryDirectory() as tmp, patch('urllib.request.urlopen',side_effect=[io.BytesIO(b'{}'),error]):
            root=pathlib.Path(tmp)
            self.assertFalse(configure('http://localhost','fixture-token',{}, {},root))
            receipt=json.loads((root/'setup-failure.json').read_text())
            self.assertEqual(receipt['operation'],'apply')
            self.assertEqual(receipt['response']['code'],'invalid_request')
            self.assertEqual(receipt['failure'],{'kind':'application','code':'model_policy_rejected','retryable':False})
            self.assertEqual(disposition({'kind':'exited','returncode':1},None,receipt)[0],'blocked')

    def test_unavailable_setup_retries_before_any_model_execution(self):
        for failure in [urllib.error.URLError('connection unavailable'),
                        urllib.error.HTTPError('http://localhost',503,'unavailable',{},io.BytesIO(b'Unavailable'))]:
            with tempfile.TemporaryDirectory() as tmp, patch('urllib.request.urlopen',side_effect=failure) as request:
                root=pathlib.Path(tmp)
                self.assertFalse(configure('http://localhost','fixture-token',{}, {},root))
                receipt=json.loads((root/'setup-failure.json').read_text())
                self.assertEqual(receipt['operation'],'read')
                self.assertEqual(request.call_count,1)
                self.assertEqual(disposition({'kind':'exited','returncode':1},None,receipt),('retry','preparation_transport'))

    def test_catalog_failure_retries_setup_using_structured_identity(self):
        detail={'code':'provider_catalog_unavailable', 'message':'The provider catalog is unavailable.',
                'details':{'provider_id':'cloud','model':'candidate'}}
        error=urllib.error.HTTPError('http://localhost',503,'unavailable',{},io.BytesIO(json.dumps(detail).encode()))
        with tempfile.TemporaryDirectory() as tmp, patch('urllib.request.urlopen',side_effect=[io.BytesIO(b'{}'),error]):
            root=pathlib.Path(tmp)
            self.assertFalse(configure('http://localhost','fixture-token',{}, {},root))
            receipt=json.loads((root/'setup-failure.json').read_text())
            self.assertEqual(receipt['operation'],'apply')
            self.assertEqual(receipt['failure'],{'kind':'provider','code':'provider_catalog_unavailable','retryable':True})
            self.assertEqual(disposition({'kind':'exited','returncode':1},None,receipt),('retry','provider_catalog_unavailable'))

    def test_catalog_words_do_not_change_rejection_classification(self):
        detail={'code':'invalid_request', 'message':'provider_catalog_unavailable'}
        error=urllib.error.HTTPError('http://localhost',400,'bad request',{},io.BytesIO(json.dumps(detail).encode()))
        with tempfile.TemporaryDirectory() as tmp, patch('urllib.request.urlopen',side_effect=error):
            root=pathlib.Path(tmp)
            self.assertFalse(configure('http://localhost','fixture-token',{}, {},root))
            receipt=json.loads((root/'setup-failure.json').read_text())
            self.assertEqual(receipt['failure'],{'kind':'application','code':'model_policy_rejected','retryable':False})


class ApplicationHTTPTests(unittest.TestCase):
    def test_admission_rejections_preserve_the_exact_mutation(self):
        request = urllib.request.Request('http://localhost/v1/sessions', data=b'{"project_id":"one"}')
        errors = [urllib.error.HTTPError(request.full_url, 429, '', headers, io.BytesIO(b''))
                  for headers in [{'Retry-After': '7'}, {}, {}]]
        response = io.BytesIO(b'{"id":"created-once"}')
        with patch('urllib.request.urlopen', side_effect=[*errors, response]) as send, \
                patch('bootstrap_policy.time.sleep') as sleep:
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
                    patch('bootstrap_policy.time.sleep') as sleep:
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
                patch('bootstrap_policy.time.sleep') as sleep:
            self.assertEqual(open_request(urllib.request.Request('http://localhost')), 'accepted')
            self.assertEqual(sleep.call_count, 20)
            self.assertEqual(sleep.call_args.args, (60,))


if __name__ == '__main__':
    unittest.main()
