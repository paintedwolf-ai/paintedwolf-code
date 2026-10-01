import pathlib
import tempfile
import unittest
import yaml

from capture import describe, digest, validate_expected, verify_capture_files
from capture_storage import stage_runtime
from snapshot import file_hash, tree_hash


class CatalogIdentityTests(unittest.TestCase):
    def test_bundled_runtime_identity_excludes_metadata_but_includes_executables_and_links(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            (root / 'tool').write_bytes(b'tool')
            (root / 'different-tool').write_bytes(b'other tool')
            (root / 'alias').symlink_to('tool')
            original = tree_hash(root)
            (root / '.DS_Store').write_bytes(b'Finder metadata')
            self.assertEqual(tree_hash(root), original)
            (root / 'tool').write_bytes(b'changed tool')
            self.assertNotEqual(tree_hash(root), original)
            (root / 'tool').write_bytes(b'tool')
            (root / 'alias').unlink()
            (root / 'alias').symlink_to('different-tool')
            self.assertNotEqual(tree_hash(root), original)

    def test_retained_runtime_is_verified_again_before_grading(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            catalog = root / 'module/config'
            catalog.mkdir(parents=True)
            policy = catalog / 'policy.yaml'
            policy.write_text('required: true\n')
            engine = root / 'engine'
            engine.write_bytes(b'admitted engine')
            config = {'catalog_sha256': digest(catalog), 'engine_sha256': file_hash(engine)}
            verify_capture_files(root, config)
            (catalog / '.DS_Store').write_bytes(b'new metadata')
            verify_capture_files(root, config)
            engine.write_bytes(b'changed engine')
            with self.assertRaisesRegex(ValueError, 'engine differs'):
                verify_capture_files(root, config)
            engine.write_bytes(b'admitted engine')
            policy.write_text('required: false\n')
            with self.assertRaisesRegex(ValueError, 'catalog differs'):
                verify_capture_files(root, config)

    def test_request_controls_for_every_role_participate_in_configuration_admission(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            catalog = root / 'catalog'
            catalog.mkdir()
            engine = root / 'engine'
            engine.write_text('engine')
            policy = {'coordinator': {'provider_id': 'cloud', 'model': 'candidate'},
                      'lite': {'provider_id': 'cloud', 'model': 'utility'},
                      'agent_pool': {'selection': 'first', 'models': [{'provider_id': 'cloud', 'model': 'worker'}]}}
            (root / 'model-policy.yaml').write_text(yaml.safe_dump(policy))
            provider = {'id': 'cloud', 'kind': 'fixture', 'base_url': 'https://provider.invalid/v1',
                        'models': [{'id': name} for name in ('candidate', 'utility', 'worker')]}
            providers = root / 'providers.local.yaml'
            providers.write_text(yaml.safe_dump({'providers': [provider]}))
            admitted = describe(root, engine, catalog)
            plan = {'models': [{'provider': 'cloud', 'model': 'candidate', 'configuration': admitted}]}
            controls = {'reasoning_effort': 'high', 'max_tokens': 512, 'temperature': 0.2,
                        'context_length': 8192, 'think_style': 'none', 'thinking_always_on': True}
            for model in provider['models']:
                for key, value in controls.items():
                    with self.subTest(model=model['id'], control=key):
                        model[key] = value
                        providers.write_text(yaml.safe_dump({'providers': [provider]}))
                        changed = describe(root, engine, catalog)
                        self.assertNotEqual(changed['configuration_sha256'], admitted['configuration_sha256'])
                        with self.assertRaisesRegex(ValueError, 'execution refused'):
                            validate_expected(changed, plan)
                        del model[key]

    def test_pre_admission_comparison_requires_all_frozen_controls(self):
        result={'coordinator':{'provider':'cloud','model':'candidate'},'catalog_sha256':'catalog',
                'workers':[{'model':'fixed'}]}
        plan={'models':[{'provider':'cloud','model':'candidate','configuration':result}]}
        validate_expected(result,plan)
        for changed in ({**result,'catalog_sha256':'other'}, {**result,'workers':[]},
                        {**result,'coordinator':{'provider':'other','model':'candidate'}}):
            with self.subTest(changed=changed), self.assertRaisesRegex(ValueError,'execution refused'):
                validate_expected(changed,plan)

    def test_finder_metadata_does_not_change_catalog_identity_or_enter_runtime(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            catalog = root / 'catalog'
            (catalog / 'nested').mkdir(parents=True)
            (catalog / 'nested/policy.yaml').write_text('required: true\n')
            expected = digest(catalog)
            for folder in (catalog, catalog / 'nested'):
                (folder / '.DS_Store').write_bytes(b'Finder metadata')
            self.assertEqual(digest(catalog), expected)
            engine = root / 'engine'
            engine.write_bytes(b'engine')
            captured = root / 'capture'
            captured.mkdir()
            stage_runtime(engine, catalog, captured)
            self.assertEqual(digest(captured / 'module/config'), expected)
            self.assertEqual(list((captured / 'module/config').rglob('.DS_Store')), [])

    def test_catalog_edits_additions_removals_and_renames_change_identity(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            policy = root / 'policy.yaml'
            policy.write_text('required: true\n')
            initial = digest(root)
            policy.write_text('required: false\n')
            self.assertNotEqual(digest(root), initial)
            policy.write_text('required: true\n')
            extra = root / 'guidance.md'
            extra.write_text('Guidance')
            self.assertNotEqual(digest(root), initial)
            extra.unlink()
            self.assertEqual(digest(root), initial)
            policy.rename(root / 'renamed.yaml')
            self.assertNotEqual(digest(root), initial)
            (root / 'renamed.yaml').unlink()
            self.assertNotEqual(digest(root), initial)

    def test_catalog_symlinks_are_not_silently_followed(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            (root / 'policy.yaml').write_text('required: true\n')
            (root / 'alias.yaml').symlink_to('policy.yaml')
            with self.assertRaisesRegex(ValueError, 'regular files'):
                digest(root)
