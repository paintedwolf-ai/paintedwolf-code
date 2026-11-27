from pathlib import Path

stream_path = Path("src/provhash/stream.py")
new_impl = '''import hashlib
from pathlib import Path


def hash_tree(root: Path, rel_paths: list[str], chunk_size: int = 16, opener=open) -> str:
    h = hashlib.sha256()
    for rel in sorted(rel_paths):
        h.update(rel.encode("utf-8"))
        h.update(b"\\x00")
        with opener(root / rel, "rb") as fh:
            while True:
                chunk = fh.read(chunk_size)
                if not chunk:
                    break
                h.update(chunk)
    return h.hexdigest()
'''
stream_path.write_text(new_impl, encoding="utf-8")

test_path = Path("tests/test_stream.py")
extra_test = '''

class TestStreamChunked(unittest.TestCase):
    def test_chunked_tail(self) -> None:
        root = Path("inputs")
        digest = hash_tree(root, ["beta.bin", "alpha.bin"], chunk_size=16)
        self.assertEqual(len(digest), 64)
'''
test_path.write_text(test_path.read_text(encoding="utf-8") + extra_test, encoding="utf-8")
