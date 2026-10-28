"""Private, forged, copied, and mispartitioned data cannot enter training."""
import json
import os
from pathlib import Path
import tempfile
import unittest

from admission import derive, seal, verify_file
from common import digest


def fixture_source(root, paths):
    key = root / 'source.key'
    if not key.exists():
        key.write_bytes(os.urandom(32))
        key.chmod(0o600)
    approved = {}
    for name, path in paths.items():
        for row in map(json.loads, path.read_text().splitlines()):
            approved[digest(row)] = {'root': row['split_group'], 'task_id': row['split_group'],
                                    'partition': 'system_test' if name == 'holdout' else name, 'sources': ['fixture']}
    sources = {'fixture': {'source_class': 'synthetic', 'training_allowed': True, 'parents': []}}
    seal(paths.values(), approved, sources, {'run_id': 'fixture', 'lineage_sha256': 'fixture', 'host_commit': 'a'*40}, root / 'source.json', key)
    return key


class Admission(unittest.TestCase):
    def test_editing_metadata_or_copying_private_rows_does_not_authorize_them(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / 'train.jsonl'
            row = {'split_group': 'task', 'text': 'synthetic'}
            path.write_text(json.dumps(row)+'\n')
            key = fixture_source(root, {'train': path})
            self.assertEqual(verify_file(path, key_path=key, expected_partition='train')[0], [row])
            with self.assertRaisesRegex(ValueError, 'partition'):
                verify_file(path, key_path=key, expected_partition='system_test')
            path.write_text(json.dumps(dict(row, text='private', training_allowed=True))+'\n')
            with self.assertRaisesRegex(ValueError, 'outside'):
                verify_file(path, key_path=key)
            manifest = json.loads((root / 'source.json').read_text())
            manifest['payload']['authority']['run_id'] = 'forged'
            (root / 'source.json').write_text(json.dumps(manifest))
            with self.assertRaisesRegex(ValueError, 'authentication'):
                verify_file(path, key_path=key)

    def test_private_ancestry_rejects_entire_publication(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / 'train.jsonl'
            row = {'split_group': 'task'}
            path.write_text(json.dumps(row)+'\n')
            key = fixture_source(root, {'train': path})
            approved = {digest(row): {'root': 'task', 'partition': 'train', 'sources': ['derived']}}
            sources = {'derived': {'source_class': 'factory_capture', 'training_allowed': True, 'parents': ['private']},
                       'private': {'source_class': 'private_dev', 'training_allowed': True}}
            with self.assertRaisesRegex(ValueError, 'not permitted'):
                seal([path], approved, sources, {'run_id': 'fixture', 'lineage_sha256': 'fixture', 'host_commit': 'a'*40}, root / 'forged.json', key)
            self.assertFalse((root / 'forged.json').exists())

    def test_derived_dataset_revalidates_parent_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / 'train.jsonl'
            path.write_text(json.dumps({'split_group': 'task'})+'\n')
            key = fixture_source(root, {'train': path})
            out = root / 'prepared'
            out.mkdir()
            (out / 'train.jsonl').write_bytes(path.read_bytes())
            derive([out / 'train.jsonl'], [path], out / 'source.json', key)
            verify_file(out / 'train.jsonl', key_path=key)
            path.write_text(json.dumps({'split_group': 'another-task'})+'\n')
            with self.assertRaisesRegex(ValueError, 'outside'):
                derive([out / 'train.jsonl'], [path], out / 'other.json', key)


if __name__ == '__main__':
    unittest.main()
