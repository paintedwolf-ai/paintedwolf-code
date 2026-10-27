"""Public bytes stay bound to the independently admitted manifest identity."""
from io import BytesIO
import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release import r2_objects as owner


class Response(BytesIO):
    headers = {'Cache-Control': 'public, max-age=31536000, immutable'}


class PublicIdentityTests(unittest.TestCase):
    def identity(self):
        return {'sha256': hashlib.sha256(b'abc').hexdigest(), 'bytes': 3}

    def test_actual_response_must_match_independent_identity(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'payload'; path.write_bytes(b'abc')
            with patch.object(owner.urllib.request, 'urlopen', side_effect=lambda *args, **kwargs: Response(b'abc')):
                owner.public_bytes('https://example.invalid/payload', path, expected_identity=self.identity())
            with patch.object(owner.urllib.request, 'urlopen', side_effect=lambda *args, **kwargs: Response(b'bad')), \
                    patch.object(owner.time, 'sleep'):
                with self.assertRaisesRegex(ValueError, 'did not converge'):
                    owner.public_bytes('https://example.invalid/payload', path, expected_identity=self.identity())

    def test_temporary_local_mutation_cannot_redefine_the_public_expected_hash(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'payload'; path.write_bytes(b'bad')
            with patch.object(owner.urllib.request, 'urlopen') as request:
                with self.assertRaisesRegex(ValueError, 'independently admitted'):
                    owner.public_bytes('https://example.invalid/payload', path, expected_identity=self.identity())
                request.assert_not_called()

    def test_malformed_identity_refuses_before_any_network_effect(self):
        for expected in [{'sha256': 'a' * 64, 'bytes': True}, {'sha256': 'wrong', 'bytes': 3},
                         {'sha256': 'a' * 64, 'bytes': 3, 'accepted': True}]:
            with self.subTest(expected=expected), tempfile.TemporaryDirectory() as temporary:
                path = Path(temporary) / 'payload'; path.write_bytes(b'abc')
                with patch.object(owner.urllib.request, 'urlopen') as request:
                    with self.assertRaisesRegex(ValueError, 'typed admitted'):
                        owner.public_bytes('https://example.invalid/payload', path, expected_identity=expected)
                    request.assert_not_called()
