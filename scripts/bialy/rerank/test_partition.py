import argparse
import contextlib
import io
import json
import random
import tempfile
import unittest
from pathlib import Path

import partition


class Partition(unittest.TestCase):
    def test_explicit_validation_dumps_are_used_whole_and_never_trained_on(self):
        train, validation = partition.partition(range(10), ["v1", "v2"], 0.5, random.Random(7))
        self.assertEqual(sorted(train), list(range(10)))
        self.assertEqual(validation, ["v1", "v2"])

    def test_without_validation_dumps_a_seeded_share_is_held_out(self):
        first = partition.partition(range(10), [], 0.2, random.Random(7))
        again = partition.partition(range(10), [], 0.2, random.Random(7))
        self.assertEqual(first, again)
        self.assertEqual((len(first[0]), len(first[1])), (8, 2))
        self.assertEqual(sorted(first[0] + first[1]), list(range(10)))

    def test_a_row_limit_applies_to_training_rows(self):
        train, validation = partition.partition(range(10), ["v"], 0.1, random.Random(1), limit=3)
        self.assertEqual((len(train), validation), (3, ["v"]))

    def test_examples_come_from_the_dumps_the_arguments_name(self):
        parser = argparse.ArgumentParser()
        parser.add_argument("--dump", action="append", default=[])
        parser.add_argument("--val-fraction", type=float, default=0.1)
        parser.add_argument("--max-rows", type=int, default=0)
        partition.arguments(parser)
        args = parser.parse_args(["--dump", "train-a", "--dump", "train-b", "--val-dump", "held"])
        dumps = {"train-a": [1, 2], "train-b": [3], "held": [9]}
        with contextlib.redirect_stderr(io.StringIO()) as log:
            train, validation = partition.examples(args, random.Random(7), dumps.__getitem__, lambda rows, rng: sorted(rows))
        self.assertEqual((train, validation), ([1, 2, 3], [9]))
        self.assertIn("rows 4: 3 train examples, 1 validation examples", log.getvalue())

    def test_the_saved_epochs_measures_are_written_when_asked(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "metrics.json"
            value = partition.measures(path, 3, 512, 0.4, 0.7, 0.2, 100, 20)
            self.assertEqual(json.loads(path.read_text()), value)
            self.assertEqual(value["val_acc"], 0.7)
            self.assertEqual((value["epoch"], value["val_examples"]), (3, 20))
        self.assertEqual(partition.measures(None, 1, 512, 0.5, 0.6, 0.3, 10, 2)["val_loss"], 0.5)


if __name__ == "__main__":
    unittest.main()
