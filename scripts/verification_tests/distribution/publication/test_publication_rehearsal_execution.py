"""The live document fixture cannot escape its prefix or omit paginated cleanup."""
import unittest
from unittest.mock import patch
from scripts.linux_release.publication import rehearsal
from scripts.linux_release.publication.immutable import ImmutableOrigin

class DocumentRehearsalTests(unittest.TestCase):
    def test_exact_prefix_listing_consumes_every_page(self):
        prefix = "release-system-tests/document-fixture"
        pages = [{"Contents": [{"Key": prefix + "/one"}], "IsTruncated": True, "NextContinuationToken": "next"},
                 {"Contents": [{"Key": prefix + "/two"}], "IsTruncated": False}]
        with patch.object(rehearsal, "request", side_effect=pages) as request:
            self.assertEqual(rehearsal.objects(prefix), [prefix + "/one", prefix + "/two"])
            self.assertIn("next", request.call_args.args)
        pages[1]["Contents"][0]["Key"] = "foreign/one"
        with patch.object(rehearsal, "request", side_effect=pages), self.assertRaisesRegex(ValueError, "exact disposable"):
            rehearsal.objects(prefix)

    def test_repeated_token_and_duplicate_keys_refuse(self):
        prefix = "release-system-tests/document-fixture"
        page = {"Contents": [], "IsTruncated": True, "NextContinuationToken": "same"}
        with patch.object(rehearsal, "request", side_effect=[page, page]), self.assertRaisesRegex(ValueError, "pagination"):
            rehearsal.objects(prefix)
        page = {"Contents": [{"Key": prefix + "/one"}, {"Key": prefix + "/one"}]}
        with patch.object(rehearsal, "request", return_value=page), self.assertRaisesRegex(ValueError, "exact disposable"):
            rehearsal.objects(prefix)

    def test_transport_rehearsal_requires_isolation_and_production_requires_fixed_root(self):
        for arguments in ({"rehearsal": True, "storage_prefix": "releases"},
                          {"rehearsal": False, "origin": "https://foreign.invalid"},
                          {"rehearsal": False, "storage_prefix": "release-system-tests/fixture"},
                          {"rehearsal": 1, "storage_prefix": "release-system-tests/fixture"}):
            with self.subTest(arguments=arguments), self.assertRaises(ValueError):
                ImmutableOrigin(**arguments)

    def test_immutable_writer_freezes_only_independently_admitted_bytes(self):
        import hashlib
        import tempfile
        from pathlib import Path
        from scripts.linux_release.publication import immutable
        with tempfile.TemporaryDirectory() as temporary:
            source = Path(temporary) / "document.json"
            source.write_bytes(b"admitted")
            expected = {"sha256": hashlib.sha256(b"admitted").hexdigest(), "bytes": 8}
            backend = ImmutableOrigin(rehearsal=True, storage_prefix="release-system-tests/fixture", origin="https://test.invalid")
            def write(arguments, **kwargs):
                captured = Path(arguments[arguments.index("--file") + 1])
                self.assertNotEqual(captured, source)
                source.write_bytes(b"transient wrong input")
                self.assertEqual(captured.read_bytes(), b"admitted")
            with patch.object(immutable.subprocess, "run", side_effect=write) as transport:
                backend.put("releases/v1.2.3/distribution.v1.json", source, "application/json", expected_identity=expected)
                self.assertEqual(transport.call_count, 1)
            with patch.object(immutable.subprocess, "run") as transport, self.assertRaises(ValueError):
                backend.put("releases/v1.2.3/distribution.v1.json", source, "application/json", expected_identity=expected)
            transport.assert_not_called()

    def test_setup_capture_admits_private_bytes_before_yield_and_refuses_changed_report(self):
        import hashlib
        import tempfile
        from pathlib import Path
        from scripts.linux_release.publication import immutable
        from scripts.linux_release import repository_setup_admission
        with tempfile.TemporaryDirectory() as temporary:
            original = Path(temporary)
            names = ('repo.deb', 'SHA256SUMS', 'SHA256SUMS.asc', 'package-key.asc')
            expected = {}
            for name in names:
                expected[name] = ('fixture ' + name).encode()
                (original / name).write_bytes(expected[name])
            setup = {'family': 'deb', 'package': {'file': 'repo.deb'}, 'repository_generation': 'a' * 64,
                     'inventory_sha256': 'b' * 64,
                     'public_key_sha256': hashlib.sha256(expected['package-key.asc']).hexdigest()}
            inputs = dict(source_commit='c' * 40, version='1.2.3', channel='stable', signing_generation=1,
                          key_fingerprint='d' * 40)
            report = {key: value for key, value in setup.items() if key != 'public_key_sha256'}
            def admit(package, checksums, signature, catalog, **kwargs):
                self.assertNotEqual(package.parent, original)
                for name in names:
                    self.assertEqual((package.parent / name).read_bytes(), expected[name])
                    self.assertEqual((package.parent / name).stat().st_mode & 0o777, 0o400)
                self.assertFalse(kwargs['rehearsal'])
                (original / 'repo.deb').write_bytes(b'changed original')
                return report
            with patch.object(repository_setup_admission, 'admit_setup', side_effect=admit):
                with immutable.captured_setup(original, setup, inputs, 'https://protected.invalid', names) as frozen:
                    self.assertEqual((frozen / 'repo.deb').read_bytes(), expected['repo.deb'])
            with patch.object(repository_setup_admission, 'admit_setup', return_value={}), self.assertRaises(ValueError):
                with immutable.captured_setup(original, setup, inputs, 'https://protected.invalid', names):
                    self.fail('a changed admission report cannot yield transport inputs')
