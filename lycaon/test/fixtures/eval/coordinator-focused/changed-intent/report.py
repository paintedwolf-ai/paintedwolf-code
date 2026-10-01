def summarize(records, limit=2):
    return sorted(records, key=lambda r: r["name"])[:limit]
