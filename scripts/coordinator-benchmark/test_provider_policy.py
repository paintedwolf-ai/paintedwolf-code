import copy
import json
from pathlib import Path
import unittest
from provider_policy import availability_policy


AVAILABILITY = json.loads(Path(__file__).with_name('benchmark.json').read_text())['infrastructure']['provider_availability']


class AvailabilityPolicyTests(unittest.TestCase):
    def test_profile_resolves_without_mutating_shipped_defaults(self):
        catalog={'providers':[{'id':'cloud','http_retry_profile':'normal'}],
                 'http_retry_profiles':{'normal':{'http_retry':{'max_retries':2,'statuses':[429]}}}}
        before=copy.deepcopy(catalog)
        provider={'id':'instance','kind':'cloud','http_retry_profile':'normal'}
        availability_policy(provider,catalog,AVAILABILITY)
        self.assertEqual(catalog,before)
        self.assertNotIn('http_retry_profile',provider)
        self.assertEqual(provider['http_retry']['max_retries'],2)
        self.assertEqual(provider['http_retry']['availability']['max_backoff_ms'],900000)

    def test_inline_policy_keeps_provider_specific_protocol_settings(self):
        provider={'id':'instance','http_retry':{'statuses':[529],'wait_headers':['provider-wait']}}
        availability_policy(provider,{}, AVAILABILITY)
        self.assertEqual(provider['http_retry']['statuses'],[429,500,502,503,504,529])
        self.assertEqual(provider['http_retry']['wait_headers'],['provider-wait'])

    def test_restrictive_local_policy_covers_outages_without_changing_capacity(self):
        configured = {'max_retries': 3, 'statuses': [429], 'wait_headers': ['Retry-After'],
                      'capacity': {'statuses': [503], 'wait_headers': ['capacity-reset']},
                      'transport': {'max_retries': 1, 'faults': ['empty_completion']}}
        before = copy.deepcopy(configured)
        provider = {'id': 'instance', 'http_retry': configured}
        availability_policy(provider, {}, AVAILABILITY)
        result = provider['http_retry']
        self.assertEqual(configured, before)
        self.assertEqual(result['statuses'], [429, 500, 502, 504])
        self.assertEqual(result['capacity'], before['capacity'])
        self.assertEqual(result['transport']['faults'], ['empty_completion', 'silent', 'unreachable'])
        self.assertEqual(result['transport']['max_retries'], 1)
        self.assertEqual(result['availability'], {'initial_ms': 5000, 'max_backoff_ms': 900000})

    def test_availability_does_not_enable_excluded_empty_response_retries(self):
        provider = {'id': 'instance', 'http_retry': {
            'statuses': [429], 'transport': {'faults': ['silent']}}}
        availability_policy(provider, {}, AVAILABILITY)
        self.assertEqual(provider['http_retry']['transport']['faults'], ['silent', 'unreachable'])
        self.assertNotIn('empty_completion', provider['http_retry']['transport']['faults'])
