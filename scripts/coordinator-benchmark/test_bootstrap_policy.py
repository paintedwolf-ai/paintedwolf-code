import io
import json
import pathlib
import tempfile
import unittest
import urllib.error
from unittest.mock import patch
from bootstrap_policy import configure
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
