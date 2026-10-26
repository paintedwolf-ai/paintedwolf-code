"""R2 cache reads and deletes honor the exact observed object."""

import json
from unittest import TestCase
from unittest.mock import Mock

from scripts.nix_cache.r2 import R2Transport


class CacheR2ObjectEdges(TestCase):
    def test_read_checks_the_locked_inventory_size(self):
        transport = R2Transport.__new__(R2Transport)
        transport.prefix = "nix-cache"
        row = {"name": "release.narinfo", "bytes": 4, "etag": "observed"}
        transport.listing = Mock(return_value=[row])

        def get_object(*arguments):
            self.assertEqual(arguments[:3], ("get-object", "--key", "nix-cache/release.narinfo"))
            self.assertEqual(arguments[-2:], ("--if-match", "observed"))
            with open(arguments[3], "wb") as target:
                target.write(b"data")
            return json.dumps({"ETag": "observed"})

        transport._aws = Mock(side_effect=get_object)
        self.assertEqual(transport.read("release.narinfo"), b"data")
        self.assertEqual(transport._read("release.narinfo"), (b"data", "observed"))
        def short_object(*arguments):
            with open(arguments[3], "wb") as target:
                target.write(b"bad")
            return json.dumps({"ETag": "observed"})

        transport._aws.side_effect = short_object
        with self.assertRaisesRegex(ValueError, "changed while reading"):
            transport.read("release.narinfo")

    def test_delete_refuses_a_changed_inventory(self):
        transport = R2Transport.__new__(R2Transport)
        transport.prefix = "nix-cache"
        transport.listing = Mock(return_value=[{"name": "release.narinfo", "etag": "new"}])
        transport._aws = Mock()
        with self.assertRaisesRegex(ValueError, "changed after its locked inventory"):
            transport.delete({"name": "release.narinfo", "etag": "old"})
        transport._aws.assert_not_called()
        transport.delete({"name": "release.narinfo", "etag": "new"})
        transport._aws.assert_called_once_with("delete-object", "--key", "nix-cache/release.narinfo")
