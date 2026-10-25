"""Regeneration guards the same canonical corpus all languages consume."""
import base64
import json
from pathlib import Path
import tempfile
import unittest

from verification_tests.distribution.manifest_fixture import ARCHITECTURES, FIXTURE, ROOT, PRODUCER, generate, relocate
from verification_tests.distribution.test_nixos_apparmor_execution import OWNER as APPARMOR


class ProducerReaderContractTests(unittest.TestCase):
    def test_real_producer_regenerates_every_shared_manifest_byte(self):
        for architecture in ARCHITECTURES:
            with self.subTest(architecture=architecture), tempfile.TemporaryDirectory() as temporary:
                actual = generate(Path(temporary), architecture)
                schema = json.loads((ROOT / 'packaging/linux/manifest/manifest.schema.json').read_bytes())
                document = json.loads(actual)
                self.assertEqual(schema['properties']['format_version']['const'], PRODUCER.FORMAT_VERSION)
                self.assertEqual(set(document), set(schema['required']))
                self.assertEqual(document['format_version'], schema['properties']['format_version']['const'])
                path = FIXTURE / (architecture + '.json')
                self.assertTrue(path.is_file(), 'Producer fixture to retain: ' + architecture + ':' + base64.b64encode(actual).decode())
                self.assertEqual(path.read_bytes(), actual, 'Regenerate the shared corpus after producer or format changes')

    def test_apparmor_admits_the_producer_seal_and_refuses_changed_identity(self):
        for architecture in ARCHITECTURES:
            with self.subTest(architecture=architecture), tempfile.TemporaryDirectory() as temporary:
                package = Path(temporary).resolve() / 'generation'
                root = package / 'lib/painted-wolf-code'
                engine = root / 'bin/pw'
                engine.parent.mkdir(parents=True)
                data = (FIXTURE / (architecture + '.elf')).read_bytes()
                engine.write_bytes(data)
                engine.chmod(0o755)
                canonical = json.loads((FIXTURE / (architecture + '.json')).read_bytes())
                document = relocate(canonical, root)
                path = root / 'manifest.json'
                path.write_text(json.dumps(document))
                self.assertIn(str(engine), APPARMOR.profile(package))
                for mutation in ['unknown version', 'missing seal', 'changed bytes']:
                    with self.subTest(mutation=mutation):
                        bad = relocate(canonical, root)
                        engine.write_bytes(data)
                        if mutation == 'unknown version':
                            bad['format_version'] = 99
                        elif mutation == 'missing seal':
                            del bad['payload']
                        else:
                            changed = bytearray(data)
                            changed[32] ^= 1
                            engine.write_bytes(changed)
                        path.write_text(json.dumps(bad))
                        with self.assertRaises(ValueError):
                            APPARMOR.profile(package)
