def select(records, limit=8):
    return sorted(records, key=lambda r: (-r["score"], r["name"]))[:limit]
