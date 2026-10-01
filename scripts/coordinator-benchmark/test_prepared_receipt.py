import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

import prepare
from snapshot import file_hash, identity


class PreparedReceiptTests(unittest.TestCase):
    def test_import_rejects_nonrelative_receipts_before_copying(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary) / 'prepared'
            source = root / 'source'
            source.mkdir(parents=True)
            (source / 'evaluation.json').write_text('{}')
            receipt = root / 'preflight' / 'validation.json'
            receipt.parent.mkdir()
            receipt.write_text(json.dumps({'cases': ['one']}))
            marker = {
                'version': 1, 'application': {}, 'evaluation': {}, 'cases': ['one'],
                'implementation_sha256': identity({}),
                'preflight': {'receipt': 'preflight/validation.json', 'sha256': file_hash(receipt)},
            }
            with patch.object(prepare, 'verify_application', return_value={}), \
                 patch.object(prepare, 'implementation_files', return_value={}):
                (root / 'prepared.json').write_text(json.dumps(marker))
                self.assertEqual(prepare.verify_prepared(root), marker)
                for path in (str(receipt), '../prepared/preflight/validation.json',
                             'preflight/../preflight/validation.json', 'validation.json'):
                    marker['preflight']['receipt'] = path
                    (root / 'prepared.json').write_text(json.dumps(marker))
                    with self.subTest(path=path), patch.object(prepare.shutil, 'copytree') as copy:
                        with self.assertRaisesRegex(ValueError, 'relative path'):
                            prepare.import_prepared(root, pathlib.Path(temporary) / 'imported')
                        copy.assert_not_called()


if __name__ == '__main__':
    unittest.main()
