import pathlib
import subprocess
import unittest
from unittest.mock import patch
from runtime_resources import release_external_resource


class ResourceReleaseTests(unittest.TestCase):
    def test_release_uses_frozen_owner_and_propagates_failure(self):
        capture = pathlib.Path('/capture')
        source = pathlib.Path('/source')
        with patch('runtime_resources.subprocess.run') as run:
            release_external_resource(capture, source)
            self.assertEqual(run.call_args.args[0], ['/source/.bin/lycaon-debug', 'eval', 'tool-usage', '--release-resources', '/capture'])
            self.assertTrue(run.call_args.kwargs['check'])
            run.side_effect = subprocess.CalledProcessError(1, 'release')
            with self.assertRaises(subprocess.CalledProcessError):
                release_external_resource(capture, source)
