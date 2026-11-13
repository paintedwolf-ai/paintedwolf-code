"""Which candidate dumps a code-rank head trains on and which it is validated on."""


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
