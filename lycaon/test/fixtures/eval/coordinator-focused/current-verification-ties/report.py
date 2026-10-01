def select(rows,limit=3):
    return sorted(rows,key=lambda r:-r["score"])[:limit]
