"""Generated Nix frontend pins must agree before hosted lane admission."""
import hashlib
import contextlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import ci_verification

from ci_policy.nix_bun import require_current


class NixBunIdentityTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        for directory in ('packaging/nix', 'lycaon-den'):
            (self.root / directory).mkdir(parents=True)
        self.lock = b'locked packages\n'
        self.expression = b'generated packages\n'
        self.revision = 'a' * 40
        (self.root / 'lycaon-den/bun.lock').write_bytes(self.lock)
        (self.root / 'packaging/nix/bun.nix').write_bytes(self.expression)
        (self.root / 'flake.lock').write_text(json.dumps({
            'nodes': {'bun2nix': {'locked': {'rev': self.revision}}}}))
        self.identity = {
            'source_sha256': hashlib.sha256(self.lock).hexdigest(),
            'expression_sha256': hashlib.sha256(self.expression).hexdigest(),
            'generator_revision': self.revision,
        }
        self.path = self.root / 'packaging/nix/bun-lock.json'
        self.publish()

    def publish(self):
        self.path.write_text(json.dumps(self.identity))

    def test_current_identity_needs_no_converter_or_network(self):
        require_current(self.root)

    def test_each_stale_pin_refuses_before_admission(self):
        for field in self.identity:
            with self.subTest(field=field):
                saved = self.identity[field]
                self.identity[field] = 'outdated'
                self.publish()
                with self.assertRaisesRegex(ValueError, field):
                    require_current(self.root)
                self.identity[field] = saved
                self.publish()

    def test_planner_refuses_stale_inputs_before_matrix_selection(self):
        self.identity['source_sha256'] = 'outdated'
        self.publish()
        output = io.StringIO()
        with mock.patch.object(ci_verification, '__file__', str(self.root / 'scripts/ci_verification.py')), \
                mock.patch('sys.argv', ['ci_verification.py', 'matrix', 'fast']), \
                mock.patch.object(ci_verification.hosted_plan, 'matrix') as matrix, \
                contextlib.redirect_stdout(output):
            self.assertEqual(ci_verification.main(), 1)
        matrix.assert_not_called()
        self.assertIn('::error file=packaging/nix/bun-lock.json::', output.getvalue())
        self.assertIn('./task codegen:dependency-inventory -- --nix-bun', output.getvalue())

    def test_unknown_shape_does_not_qualify(self):
        self.identity['unexpected'] = 'value'
        self.publish()
        with self.assertRaisesRegex(ValueError, 'unsupported shape'):
            require_current(self.root)

    def test_missing_inputs_do_not_qualify(self):
        (self.root / 'packaging/nix/bun.nix').unlink()
        with self.assertRaises(FileNotFoundError):
            require_current(self.root)


if __name__ == '__main__':
    unittest.main()
