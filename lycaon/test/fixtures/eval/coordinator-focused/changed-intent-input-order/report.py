def summarize(rows,limit=2):
    return sorted(rows,key=lambda r:r["name"])[:limit]
