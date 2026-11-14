"""Which candidate dumps a code-rank head trains and validates on, and the measures it records."""
import json
import sys
from pathlib import Path


def arguments(parser):
    parser.add_argument("--val-dump", action="append", default=[], help="validation candidate dump JSONL from its own partition; repeatable")
    parser.add_argument("--metrics", help="write the saved epoch's validation measures here as JSON")


def partition(train_rows, validation_rows, fraction, rng, limit=0):
    """Training and validation rows. Explicit validation dumps come from their own
    partition and are used whole; without them a seeded share of the training dumps is held out."""
    rows = list(train_rows)
    rng.shuffle(rows)
    if limit:
        rows = rows[:limit]
    if validation_rows:
        return rows, list(validation_rows)
    split = int(len(rows) * (1 - fraction))
    return rows[:split], rows[split:]


def examples(args, rng, read, label):
    """Labelled training and validation examples from the dumps the arguments name."""
    train, validation = partition([row for path in args.dump for row in read(path)],
                                  [row for path in args.val_dump for row in read(path)], args.val_fraction, rng, args.max_rows)
    train_ex, val_ex = label(train, rng), label(validation, rng)
    print(f"rows {len(train) + len(validation)}: {len(train_ex)} train examples, {len(val_ex)} validation examples", file=sys.stderr)
    return train_ex, val_ex


def measures(path, epoch, max_len, loss, acc, mae, train_examples, val_examples):
    """The saved epoch's measures, written to `path` when one is given."""
    value = {"max_len": max_len, "val_loss": loss, "val_acc": acc, "val_mae": mae, "train_examples": train_examples,
             "val_examples": val_examples, "epoch": epoch}
    if path:
        Path(path).write_text(json.dumps(value, indent=2) + "\n")
    return value
