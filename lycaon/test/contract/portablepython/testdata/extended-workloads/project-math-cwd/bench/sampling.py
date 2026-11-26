import math
from fractions import Fraction


def pass_k(c: int, n: int, k: int) -> Fraction:
    if n < 0 or c < 0 or k < 0 or c > n or k > n:
        raise ValueError(f"invalid pass_k arguments: c={c}, n={n}, k={k}")
    if c < k:
        return Fraction(0, 1)
    return Fraction(math.comb(c, k), math.comb(n, k))
