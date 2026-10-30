"""Repository rehearsals use explicit bucket credentials without REST authority."""

import os
import tempfile
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release import r2_objects


class BucketScopedRepositoryTests(unittest.TestCase):
    def environment(self):
        return {"RELEASE_R2_TRANSPORT": "s3", "R2_S3_ENDPOINT": "https://s3.example.invalid",
                "R2_BUCKET": "test-bucket", "AWS_ACCESS_KEY_ID": "fixture-access",
                "AWS_SECRET_ACCESS_KEY": "fixture-secret", "RELEASE_TEST_DOWNLOAD_BASE_URL": "https://test.example.invalid"}

    def test_explicit_s3_rehearsal_does_not_require_cloudflare_management_authority(self):
        with patch.dict(os.environ, self.environment(), clear=True):
            self.assertEqual(r2_objects.rehearsal_environment(), "https://test.example.invalid")
            self.assertNotIn("CLOUDFLARE_API_TOKEN", os.environ)
        for field in ["AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "R2_S3_ENDPOINT", "R2_BUCKET"]:
            environment = self.environment()
            del environment[field]
            with self.subTest(field=field), patch.dict(os.environ, environment, clear=True):
                with self.assertRaises(ValueError):
                    r2_objects.rehearsal_environment()

    def test_private_configuration_is_loaded_by_the_shared_owner(self):
        module = types.ModuleType("scripts.release_storage.rehearsal")
        module.configuration = Mock(return_value={"fixture": True})
        module.environment = Mock(return_value=self.environment())
        with patch.dict(os.environ, {"HOME": "/unused"}, clear=True), \
                patch.dict(sys.modules, {"scripts.release_storage.rehearsal": module}):
            self.assertEqual(r2_objects.rehearsal_environment(), "https://test.example.invalid")
            module.configuration.assert_called_once()
            module.environment.assert_called_once()
            self.assertEqual(os.environ["RELEASE_R2_TRANSPORT"], "s3")

    def test_all_object_writes_and_cleanup_select_the_s3_owner(self):
        key = "release-system-tests/fixture/object"
        with patch.dict(os.environ, self.environment(), clear=True), patch.object(r2_objects.subprocess, "run") as run:
            r2_objects.put(key, Path("payload"))
            self.assertEqual(run.call_args.args[0][2:4], ["immutable", key])
            self.assertIn("public, max-age=31536000, immutable", run.call_args.args[0])
            r2_objects.put(key, Path("payload"), True)
            self.assertEqual(run.call_args.args[0][2:4], ["put", key])
            self.assertIn("no-cache, no-store, must-revalidate", run.call_args.args[0])
            r2_objects.delete_fixture_object(key)
            self.assertEqual(run.call_args.args[0][2:], ["delete", key])
            self.assertTrue(all(call.args[0][0] == "python3" for call in run.call_args_list))
            with self.assertRaises(ValueError):
                r2_objects.delete_fixture_object("linux/repositories/live")

    def test_activation_reads_use_bound_s3_bytes_and_preserve_failure(self):
        module = types.ModuleType("scripts.release_storage.s3")
        module.read_bytes = Mock(return_value=b'{"generation":"fixture"}')
        with patch.dict(os.environ, self.environment(), clear=True), \
                patch.dict(sys.modules, {"scripts.release_storage.s3": module}):
            self.assertEqual(r2_objects.current("key", Path("unused")), {"generation": "fixture"})
            module.read_bytes.assert_called_once_with("key")
            module.read_bytes.return_value = None
            self.assertIsNone(r2_objects.current("key", Path("unused")))
            module.read_bytes.side_effect = ValueError("authenticated read failed")
            with self.assertRaisesRegex(ValueError, "authenticated read failed"):
                r2_objects.current("key", Path("unused"))

    def test_present_nonobject_activation_refuses_native_halt_for_both_transports(self):
        from scripts.linux_release import repository_withdrawal
        from scripts.release_storage import s3
        from scripts.verification_tests.distribution.repository_fixture_support import halt_distribution
        plan = halt_distribution('1.2.3', None, channels=('stable',))
        for transport in ('s3', 'cloudflare'):
            for raw in (b'null', b'[]', b'42', b'"value"'):
                with self.subTest(transport=transport, raw=raw), tempfile.TemporaryDirectory() as temporary:
                    directory = Path(temporary)
                    def request(operation, *arguments):
                        if operation == 'list-objects-v2':
                            key = arguments[arguments.index('--prefix') + 1]
                            return {'Contents': [{'Key': key, 'Size': len(raw), 'ETag': 'fixture'}]}
                        self.assertEqual(operation, 'get-object')
                        Path(arguments[-1]).write_bytes(raw)
                        return {'ETag': 'fixture'}
                    def cloudflare(arguments, **kwargs):
                        Path(arguments[-1]).write_bytes(raw)
                        return '200'
                    with patch.dict(os.environ, {'RELEASE_R2_TRANSPORT': transport}), \
                         patch.object(s3, 'request', side_effect=request), \
                         patch.object(r2_objects.subprocess, 'check_output', side_effect=cloudflare), \
                         patch.object(r2_objects.subprocess, 'run') as effects:
                        with self.assertRaisesRegex(ValueError, 'JSON object'):
                            repository_withdrawal.prepare(plan)
                        effects.assert_not_called()
                    with patch.dict(os.environ, {'RELEASE_R2_TRANSPORT': transport}), \
                         patch.object(s3, 'request', return_value={'Contents': []}), \
                         patch.object(r2_objects.subprocess, 'check_output', return_value='404'):
                        self.assertIsNone(r2_objects.current('fixture/current.json', directory))
