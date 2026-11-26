import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path("src").resolve()))
from provhash.stream import hash_tree  # noqa: E402

read_sizes: list[int] = []


class InstrumentedReader:
    def __init__(self, inner):
        self._inner = inner

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return self._inner.__exit__(*exc)

    def read(self, n: int = -1):
        if n <= 0:
            raise AssertionError(f"unbounded read requested: n={n}")
        data = self._inner.read(n)
        if data:
            read_sizes.append(len(data))
        return data


def instrumented_open(path, mode="rb"):
    return InstrumentedReader(open(path, mode))


digest = hash_tree(Path("inputs"), ["beta.bin", "alpha.bin"], chunk_size=16, opener=instrumented_open)
Path("provenance_result.json").write_text(
    json.dumps(
        {
            "sha256": digest,
            "read_chunk_sizes": read_sizes,
            "max_chunk_size": 16,
        },
        indent=2,
    )
    + "\n",
    encoding="utf-8",
)

suite = unittest.defaultTestLoader.discover("tests")
result = unittest.TextTestRunner(verbosity=1).run(suite)
if not result.wasSuccessful():
    sys.exit(1)
