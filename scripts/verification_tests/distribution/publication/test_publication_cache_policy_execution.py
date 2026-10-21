"""Public cache activation cannot be inferred from source qualification."""
import json
from pathlib import Path
import tempfile
import unittest

from linux_release.publication.trust import cache_policy as owner


class CachePolicyTests(unittest.TestCase):
    def load(self, declaration):
        with tempfile.TemporaryDirectory() as directory:
            path=Path(directory)/"policy.json"
            path.write_text(json.dumps(declaration))
            return owner.load(path)

    def test_disabled_has_no_cache_trust_or_delivery_claims(self):
        self.assertIsNone(self.load({"schema_version":1,"enabled":False}))
        for field in ("url","public_keys","trust_policy"):
            with self.subTest(field=field),self.assertRaises(ValueError):
                self.load({"schema_version":1,"enabled":False,field:[]})

    def test_enabled_returns_only_explicit_protected_production_trust(self):
        trust={"schema_version":1,"mode":"production","public_keys":["protected:key"]}
        self.assertEqual(self.load({"schema_version":1,"enabled":True,"trust_policy":trust}),trust)
        for invalid in ({}, {**trust,"mode":"rehearsal"}, {**trust,"public_keys":[]},
                        {**trust,"public_keys":["same","same"]}, {**trust,"schema_version":True}):
            with self.subTest(trust=invalid),self.assertRaises(ValueError):
                self.load({"schema_version":1,"enabled":True,"trust_policy":invalid})

    def test_activation_shape_and_version_are_strict(self):
        for declaration in ({"schema_version":True,"enabled":False},
                            {"schema_version":1,"enabled":0},
                            {"schema_version":1,"enabled":True},
                            {"schema_version":2,"enabled":False}):
            with self.subTest(declaration=declaration),self.assertRaises(ValueError):
                self.load(declaration)
