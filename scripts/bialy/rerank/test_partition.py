import random
import unittest

from partition import partition


class Partition(unittest.TestCase):
    def test_explicit_validation_dumps_are_used_whole_and_never_trained_on(self):
        train, validation = partition(range(10), ["v1", "v2"], 0.5, random.Random(7))
        self.assertEqual(sorted(train), list(range(10)))
        self.assertEqual(validation, ["v1", "v2"])

    def test_without_validation_dumps_a_seeded_share_is_held_out(self):
        first = partition(range(10), [], 0.2, random.Random(7))
        again = partition(range(10), [], 0.2, random.Random(7))
        self.assertEqual(first, again)
        self.assertEqual((len(first[0]), len(first[1])), (8, 2))
        self.assertEqual(sorted(first[0] + first[1]), list(range(10)))

    def test_a_row_limit_applies_to_training_rows(self):
        train, validation = partition(range(10), ["v"], 0.1, random.Random(1), limit=3)
        self.assertEqual((len(train), validation), (3, ["v"]))


if __name__ == "__main__":
    unittest.main()
