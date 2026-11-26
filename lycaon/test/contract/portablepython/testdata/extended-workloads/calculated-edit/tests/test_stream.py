import unittest
from pathlib import Path

from provhash.stream import hash_tree


class TestStreamBaseline(unittest.TestCase):
    def test_exists(self) -> None:
        self.assertTrue(callable(hash_tree))
