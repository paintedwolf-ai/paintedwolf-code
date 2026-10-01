"""Fixed-bank estimates with equal family, scenario, and fixture weights."""
import math
from math import comb
from fractions import Fraction

METHOD = 'weighted-hoeffding-fixed-bank'


def weights(operations, tier='gate'):
    families = {}
    for operation in operations:
        if operation['role'] == 'scored' and operation.get('tier', 'gate') == tier:
            families.setdefault(operation['family'], {}).setdefault(operation['scenario'], []).append(operation['id'])
    return {fixture: Fraction(1, len(families) * len(scenarios) * len(fixtures))
            for scenarios in families.values() for fixtures in scenarios.values() for fixture in fixtures}


def estimate(cases, operations, tier='gate'):
    weight = weights(operations, tier)
    selected = {case['id']: case for case in cases if case['id'] in weight}
    if selected.keys() != weight.keys() or any(c['measured'] <= 0 for c in selected.values()):
        raise ValueError('fixed-bank estimates require every scored fixture')
    score = float(sum(100 * w * Fraction(selected[key]['passed'], selected[key]['measured']) for key, w in weight.items()))
    squared = sum(w*w / selected[key]['measured'] for key, w in weight.items())
    # Two-sided Hoeffding bound for independent, bounded trial outcomes with
    # predetermined weights. It remains nonzero for an all-pass sample.
    radius = 100 * math.sqrt(math.log(40) * squared / 2)
    return {'score': score, 'interval_95': [max(0, score-radius), min(100, score+radius)], 'method': METHOD}


def pass_k(passed, measured, k):
    """Unbiased probability that k independent draws without replacement all pass (tau-bench pass^k)."""
    if measured < k or k < 1:
        return None
    return Fraction(comb(passed, k), comb(measured, k))


def reliability(cases, operations, tier, k):
    """Weighted pass^k and outcome consistency ((2p-1)^2) over the same fixed bank."""
    weight = weights(operations, tier)
    selected = {case['id']: case for case in cases if case['id'] in weight}
    if selected.keys() != weight.keys() or any(c['measured'] < k for c in selected.values()):
        return None
    strict = sum(100 * w * pass_k(selected[key]['passed'], selected[key]['measured'], k) for key, w in weight.items())
    consistency = sum(w * (2 * Fraction(selected[key]['passed'], selected[key]['measured']) - 1) ** 2 for key, w in weight.items())
    return {'k': k, 'pass_k': float(strict), 'consistency': float(100 * consistency)}
