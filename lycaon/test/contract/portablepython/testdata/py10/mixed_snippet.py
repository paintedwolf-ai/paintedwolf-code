def compute_total(items: list[int]) -> int:
    # sum positive items
    return sum(x for x in items if x > 0)
