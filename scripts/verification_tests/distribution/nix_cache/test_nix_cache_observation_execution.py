"""Read-only public cache facts require fresh admission, controls, and every byte."""
import copy
import hashlib
from pathlib import Path
import unittest
from unittest.mock import patch

from scripts.nix_cache import observation
from scripts.nix_cache.publication import availability, encoded, record_name
from scripts.nix_cache.release_lane import request, trust_policy
from scripts.verification_tests.distribution.distribution_document_fixtures import cache_receipt


class ReadOnlyBackend:
    public = observation.PUBLIC_CACHE

    def __init__(self, report):
        self.report = report
        self.name = record_name(report['request'])
        self.receipt = availability(report)
        self.records = {self.name: encoded(self.receipt)}
        self.checked = []
        self.fail = None
        self.withdraw_after = False

    def read(self, name):
        return self.records.get(name)

    def verify_public(self, name, record):
        if name == self.fail:
            raise ValueError('actual public readback failed')
        self.checked.append((name, copy.deepcopy(record)))
        if self.withdraw_after:
            self.records[self.name + '.withdrawn'] = b'withdrawn'


class PublicObservationTests(unittest.TestCase):
    def setUp(self):
        root = Path(__file__).resolve().parents[4]
        selected = request('a' * 40, '1.2.3', 'x86_64-linux',
            ['/nix/store/' + 'a' * 32 + '-painted-wolf-code'], root / 'flake.lock')
        self.report = {**cache_receipt(selected, trust_policy()['public_keys']), 'status': 'passed'}
        self.backend = ReadOnlyBackend(self.report)
        self.local = patch.object(observation, 'verified_report', side_effect=lambda cache, report: report)
        self.admission = patch.object(observation, 'authorize', return_value=True)
        self.verify = self.local.start()
        self.authorize = self.admission.start()
        self.addCleanup(self.local.stop)
        self.addCleanup(self.admission.stop)

    def observe(self):
        return observation.observe(Path('/owned/cache'), self.report, selection={'fresh': 'inputs'},
                                   trust_policy={'protected': 'trust'}, backend=self.backend)

    def test_every_object_and_exact_record_observed_without_write_capability(self):
        result = self.observe()
        self.assertEqual([name for name, row in self.backend.checked],
                         [row['name'] for row in self.report['objects']] + [self.backend.name])
        record = self.backend.checked[-1][1]
        self.assertEqual(record['sha256'], hashlib.sha256(self.backend.records[self.backend.name]).hexdigest())
        self.assertEqual(result['admission_sha256'], hashlib.sha256(encoded(result['facts'])).hexdigest())
        self.assertEqual(self.authorize.call_count, 2)
        self.assertIsNot(self.verify.call_args.args[1], self.report)

    def test_origin_mode_prefix_and_fresh_admission_refuse_before_transport(self):
        for field, value in [('mode', 'rehearsal'), ('cache_prefix', 'foreign')]:
            with self.subTest(field=field):
                old = self.report['request'][field]
                self.report['request'][field] = value
                with self.assertRaises(ValueError):
                    self.observe()
                self.report['request'][field] = old
        self.backend.public = 'https://foreign.invalid/nix-cache'
        with self.assertRaises(ValueError):
            self.observe()
        self.backend.public = observation.PUBLIC_CACHE
        self.authorize.return_value = False
        with self.assertRaises(ValueError):
            self.observe()
        self.assertEqual(self.backend.checked, [])

    def test_missing_changed_or_withdrawn_control_refuses(self):
        self.backend.records[self.backend.name] = b'other'
        with self.assertRaises(ValueError):
            self.observe()
        self.backend.records[self.backend.name] = encoded(self.backend.receipt)
        self.backend.records[self.backend.name + '.withdrawn'] = b'withdrawn'
        with self.assertRaises(ValueError):
            self.observe()
        self.assertEqual(self.backend.checked, [])

    def test_stale_public_object_and_mid_observation_withdrawal_refuse(self):
        self.backend.fail = 'a' * 32 + '.narinfo'
        with self.assertRaises(ValueError):
            self.observe()
        self.backend.fail = None
        self.backend.withdraw_after = True
        with self.assertRaises(ValueError):
            self.observe()

    def test_rotation_selects_exact_retained_content_and_rechecks_admission(self):
        raw = encoded(self.backend.receipt)
        rotated = self.backend.name.removesuffix('.json') + '-rotation-' + hashlib.sha256(raw).hexdigest() + '.json'
        self.backend.records[self.backend.name] = b'prior signatures'
        self.backend.records[rotated] = raw
        self.assertEqual(self.observe()['facts']['record'], rotated)
        self.authorize.side_effect = [True, False]
        with self.assertRaises(ValueError):
            self.observe()
