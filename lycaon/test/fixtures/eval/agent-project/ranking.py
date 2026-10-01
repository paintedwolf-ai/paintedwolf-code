DEFAULT_LIMIT = 8


def rank_profiles(records, limit=DEFAULT_LIMIT):
    """Return records ordered by score, then name, without mutating the input."""
    return sorted(records, key=lambda row: (row["score"], row["name"]))[:limit]
